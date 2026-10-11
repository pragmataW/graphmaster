package index

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/repo"
)

// maxCtxRead caps RepoCtx.ReadFile: manifests are small.
const maxCtxRead = 1 << 20

// Dependency key prefixes. Every answer RepoCtx gives is recorded as
// key -> signature so a later scan can tell whether module identities or
// import targets derived from it may have changed (see verifyDeps).
const (
	depCode = "c:" // code-file existence, answered from the candidate set
	depFile = "f:" // non-code file stat (existence and content)
	depDir  = "d:" // directory existence
	depList = "l:" // directory listing, signed by the directory's mtime
)

const sigMissing = "-"

// RepoCtx is the real model.RepoContext: code-file existence comes from the
// set of files being indexed (so answers match the graph), everything else
// (go.mod, tsconfig.json, composer.json, Cargo.toml, *.csproj, directories)
// from the filesystem with cached reads. It is safe for concurrent use.
//
// Every answer is recorded as a dependency (key -> signature). Answers
// given through a view returned by For are also attributed to that view's
// owner file, so a later change of the answer only reprocesses the owners;
// answers given by the RepoCtx itself have no owner and force a full
// rebuild when they change.
type RepoCtx struct {
	root *repo.Root
	code map[string]struct{}

	mu    sync.Mutex
	files map[string]fileProbe
	dirs  map[string]bool
	lists map[string][]string
	lsigs map[string]string // listing signatures
	deps  map[string]string
	fresh map[string]string // deps recorded since the last TakeNewDeps

	owned      map[DepOwner]bool
	freshOwned []DepOwner // attributions since the last TakeNewOwners
	views      map[string]*repoView
	global     *repoView
	unrecorded *repoView
}

// DepOwner attributes the dependency Key to the file Owner it was
// consulted for ("" = no owner).
type DepOwner struct{ Key, Owner string }

type fileProbe struct {
	exists  bool
	sig     string
	data    []byte
	readErr error
	read    bool
}

var (
	_ model.RepoContext = (*RepoCtx)(nil)
	_ model.RepoContext = (*repoView)(nil)
)

// NewRepoCtx returns a context for the worktree of root. code is the set of
// repo-relative code paths being indexed; it must not be modified later.
func NewRepoCtx(root *repo.Root, code map[string]struct{}) *RepoCtx {
	if code == nil {
		code = map[string]struct{}{}
	}
	c := &RepoCtx{
		root:  root,
		code:  code,
		files: map[string]fileProbe{},
		dirs:  map[string]bool{},
		lists: map[string][]string{},
		lsigs: map[string]string{},
		deps:  map[string]string{},
		fresh: map[string]string{},
		owned: map[DepOwner]bool{},
		views: map[string]*repoView{},
	}
	c.global = &repoView{c: c, record: true}
	c.unrecorded = &repoView{c: c}
	return c
}

// For returns a view of c whose answers are attributed to the file owner
// (repo-relative path), e.g. the file whose module identity or imports are
// being resolved.
func (c *RepoCtx) For(owner string) model.RepoContext {
	if owner == "" {
		return c.global
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.views[owner]
	if v == nil {
		v = &repoView{c: c, owner: owner, record: true}
		c.views[owner] = v
	}
	return v
}

// Unrecorded returns a view whose answers are not recorded at all. It is
// for derived data that is recomputed whenever any dependency or the file
// set changes (import targets, see linker).
func (c *RepoCtx) Unrecorded() model.RepoContext { return c.unrecorded }

// repoView answers on behalf of one owner.
type repoView struct {
	c      *RepoCtx
	owner  string
	record bool
}

func (v *repoView) FileExists(rel string) bool          { return v.c.fileExists(v, rel) }
func (v *repoView) ReadFile(rel string) ([]byte, error) { return v.c.readFile(v, rel) }
func (v *repoView) DirExists(rel string) bool           { return v.c.dirExists(v, rel) }
func (v *repoView) ListDir(dir string) []string         { return v.c.listDir(v, dir) }
func (c *RepoCtx) FileExists(rel string) bool           { return c.fileExists(c.global, rel) }
func (c *RepoCtx) ReadFile(rel string) ([]byte, error)  { return c.readFile(c.global, rel) }
func (c *RepoCtx) DirExists(rel string) bool            { return c.dirExists(c.global, rel) }
func (c *RepoCtx) ListDir(dir string) []string          { return c.listDir(c.global, dir) }

// clean normalizes rel; ok is false for paths outside the repository.
func clean(rel string) (string, bool) {
	c := model.CleanRel(rel)
	if c == "" && rel != "" && rel != "." && rel != "./" {
		// CleanRel maps both the root and escaping paths to "".
		if model.CleanRel(rel+"/x") == "" {
			return "", false
		}
	}
	return c, true
}

func (c *RepoCtx) fileExists(v *repoView, rel string) bool {
	p, ok := clean(rel)
	if !ok || p == "" {
		return false
	}
	if model.IsCodePath(p) {
		_, exists := c.code[p]
		c.mu.Lock()
		c.noteLocked(v, depCode+p, boolSig(exists))
		c.mu.Unlock()
		return exists
	}
	return c.probe(v, p, false).exists
}

// readFile reads at most 1 MiB. Code files (e.g. sources the resolver
// scans) are read straight from disk, neither cached nor recorded as
// dependencies: their changes are tracked by the file pipeline itself, and
// caching every source would hold the whole repository in memory.
func (c *RepoCtx) readFile(v *repoView, rel string) ([]byte, error) {
	p, ok := clean(rel)
	if !ok || p == "" {
		return nil, &fs.PathError{Op: "read", Path: rel, Err: fs.ErrNotExist}
	}
	if model.IsCodePath(p) {
		return readCapped(c.root.Abs(p), maxCtxRead)
	}
	pr := c.probe(v, p, true)
	if pr.readErr != nil {
		return nil, pr.readErr
	}
	return append([]byte(nil), pr.data...), nil
}

// probe stats (and optionally reads) a file once per context and records
// the dependency for v on every call.
func (c *RepoCtx) probe(v *repoView, p string, read bool) fileProbe {
	c.mu.Lock()
	pr, seen := c.files[p]
	if seen && (pr.read || !read) {
		c.noteLocked(v, depFile+p, pr.sig)
		c.mu.Unlock()
		return pr
	}
	c.mu.Unlock()
	abs := c.root.Abs(p)
	pr = fileProbe{sig: fileSig(abs)}
	pr.exists = pr.sig != sigMissing
	if read {
		pr.read = true
		if !pr.exists {
			pr.readErr = &fs.PathError{Op: "read", Path: p, Err: fs.ErrNotExist}
		} else {
			pr.data, pr.readErr = readCapped(abs, maxCtxRead)
		}
	}
	c.mu.Lock()
	c.files[p] = pr
	c.noteLocked(v, depFile+p, pr.sig)
	c.mu.Unlock()
	return pr
}

func (c *RepoCtx) dirExists(v *repoView, rel string) bool {
	p, ok := clean(rel)
	if !ok {
		return false
	}
	c.mu.Lock()
	exists, seen := c.dirs[p]
	c.mu.Unlock()
	if !seen {
		exists = dirSig(c.root.Abs(p)) == "1"
	}
	c.mu.Lock()
	c.dirs[p] = exists
	c.noteLocked(v, depDir+p, boolSig(exists))
	c.mu.Unlock()
	return exists
}

func (c *RepoCtx) listDir(v *repoView, dir string) []string {
	p, ok := clean(dir)
	if !ok {
		return nil
	}
	c.mu.Lock()
	names, seen := c.lists[p]
	if seen {
		c.noteLocked(v, depList+p, c.lsigs[p])
		c.mu.Unlock()
		return append([]string(nil), names...)
	}
	c.mu.Unlock()
	abs := c.root.Abs(p)
	sig := listSig(abs)
	if sig != sigMissing {
		entries, err := os.ReadDir(abs)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() && (p != "" || e.Name() != ".git") {
					names = append(names, e.Name())
				}
			}
		}
		sort.Strings(names)
	}
	c.mu.Lock()
	c.lists[p] = names
	c.lsigs[p] = sig
	c.noteLocked(v, depList+p, sig)
	c.mu.Unlock()
	return append([]string(nil), names...)
}

// noteLocked records dependency key with signature sig for view v.
func (c *RepoCtx) noteLocked(v *repoView, key, sig string) {
	if !v.record {
		return
	}
	if old, ok := c.deps[key]; !ok || old != sig {
		c.deps[key] = sig
		c.fresh[key] = sig
	}
	k := DepOwner{Key: key, Owner: v.owner}
	if !c.owned[k] {
		c.owned[k] = true
		c.freshOwned = append(c.freshOwned, k)
	}
}

// TakeNewDeps returns the dependencies recorded since the previous call,
// for incremental persistence.
func (c *RepoCtx) TakeNewDeps() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.fresh) == 0 {
		return nil
	}
	out := c.fresh
	c.fresh = map[string]string{}
	return out
}

// TakeNewOwners returns the dependency attributions recorded since the
// previous call.
func (c *RepoCtx) TakeNewOwners() []DepOwner {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.freshOwned
	c.freshOwned = nil
	return out
}

// Deps returns a copy of the recorded dependencies.
func (c *RepoCtx) Deps() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.deps))
	for k, v := range c.deps {
		out[k] = v
	}
	return out
}

func boolSig(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// fileSig signs a non-directory path by its stat (following symlinks).
func fileSig(abs string) string {
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() {
		return sigMissing
	}
	st := repo.StatFromInfo(fi)
	return repo.StatSig{MtimeNS: st.MtimeNS, Size: st.Size, Ino: st.Ino}.String()
}

func dirSig(abs string) string {
	fi, err := os.Stat(abs)
	return boolSig(err == nil && fi.IsDir())
}

func listSig(abs string) string {
	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() {
		return sigMissing
	}
	return strconv.FormatInt(fi.ModTime().UnixNano(), 10)
}

func readCapped(abs string, limit int64) ([]byte, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: larger than %d bytes", filepath.Base(abs), limit)
	}
	return b, nil
}

// verifyDeps reports the first recorded dependency whose current answer
// differs, or "" when all still hold.
func verifyDeps(root *repo.Root, deps map[string]string, code map[string]struct{}) string {
	if ch := changedDeps(root, deps, code); len(ch) > 0 {
		return ch[0]
	}
	return ""
}

// changedDeps returns every recorded dependency whose current answer
// differs, sorted.
func changedDeps[V any](root *repo.Root, deps map[string]string, code map[string]V) []string {
	out := append(changedFSDeps(root, deps), changedCodeDeps(deps, code)...)
	sort.Strings(out)
	return out
}

// changedCodeDeps checks code-file existence answers against the current
// candidate set (any map keyed by path). Keys of unknown kinds are
// reported as changed (conservative).
func changedCodeDeps[V any](deps map[string]string, code map[string]V) []string {
	var out []string
	for k, want := range deps {
		path, ok := strings.CutPrefix(k, depCode)
		if !ok {
			if !knownDepKind(k) {
				out = append(out, k)
			}
			continue
		}
		_, exists := code[path]
		if boolSig(exists) != want {
			out = append(out, k)
		}
	}
	return out
}

func knownDepKind(k string) bool {
	for _, p := range []string{depCode, depFile, depDir, depList} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// changedFSDeps re-probes filesystem dependencies in parallel and returns
// the changed keys.
func changedFSDeps(root *repo.Root, deps map[string]string) []string {
	type job struct{ key, want string }
	var jobs []job
	for k, want := range deps {
		if strings.HasPrefix(k, depFile) || strings.HasPrefix(k, depDir) || strings.HasPrefix(k, depList) {
			jobs = append(jobs, job{k, want})
		}
	}
	if len(jobs) == 0 {
		return nil
	}
	var (
		mu      sync.Mutex
		changed []string
		wg      sync.WaitGroup
		next    atomic.Int64
	)
	workers := min(64, (len(jobs)+63)/64)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(jobs) {
					return
				}
				j := jobs[i]
				abs := root.Abs(j.key[2:])
				var got string
				switch j.key[:2] {
				case depFile:
					got = fileSig(abs)
				case depDir:
					got = dirSig(abs)
				case depList:
					got = listSig(abs)
				}
				if got != j.want {
					mu.Lock()
					changed = append(changed, j.key)
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	return changed
}
