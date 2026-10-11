package install

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/gofrs/flock"
)

// Options tunes Install and Uninstall.
type Options struct {
	// DryRun computes the report (with diffs) without writing anything.
	DryRun bool
}

// Engine applies and reverts plans, keeping install-state.json in sync.
type Engine struct {
	env Env
}

// NewEngine returns an engine bound to env.
func NewEngine(env Env) *Engine { return &Engine{env: env} }

// Env returns the engine's environment.
func (e *Engine) Env() Env { return e.env }

// State loads the current manifest.
func (e *Engine) State() (*State, error) {
	p, err := e.env.StatePath()
	if err != nil {
		return nil, err
	}
	return LoadState(p)
}

// Install brings every file in plans to its desired state. All files are
// parsed and edited in memory first; any error (corrupt JSON, invalid TOML,
// conflicting definitions) aborts before a single byte is written. Files an
// agent touched in an earlier install but no longer lists are reverted.
func (e *Engine) Install(opts Options, plans ...Plan) (*Report, error) {
	seen := map[string]bool{}
	for _, p := range plans {
		if err := p.Validate(); err != nil {
			return nil, err
		}
		if seen[p.Agent] {
			return nil, fmt.Errorf("agent %q has more than one plan", p.Agent)
		}
		seen[p.Agent] = true
	}
	return e.run(opts, func(st *State) ([]*work, error) {
		planned := map[string][]string{} // path -> agents
		var order []string
		add := func(path string) {
			if !slices.Contains(order, path) {
				order = append(order, path)
			}
		}
		for _, p := range plans {
			for _, fc := range p.Files {
				planned[fc.Path] = append(planned[fc.Path], p.Agent)
				add(fc.Path)
			}
		}
		stale := map[string][]string{} // path -> agents that dropped it
		for _, p := range plans {
			for _, rec := range st.FilesOf(p.Agent) {
				if !slices.Contains(planned[rec.Path], p.Agent) {
					stale[rec.Path] = append(stale[rec.Path], p.Agent)
					add(rec.Path)
				}
			}
		}

		var works []*work
		for _, path := range order {
			w, err := loadWork(path, st)
			if err != nil {
				return nil, err
			}
			if agents := stale[path]; len(agents) > 0 {
				if len(planned[path]) == 0 {
					err = w.planRemoval(agents)
				} else {
					w.dropAgents = agents
					err = w.removeSurgically(agents)
				}
				if err != nil {
					return nil, err
				}
			}
			for _, p := range plans {
				for _, fc := range p.Files {
					if fc.Path == path {
						w.agentOrder = append(w.agentOrder, p.Agent)
						if err := w.apply(p.Agent, fc); err != nil {
							return nil, err
						}
					}
				}
			}
			works = append(works, w)
		}
		return works, nil
	})
}

// Uninstall reverts the given agents (all recorded agents when none are
// named). A file still carrying graphmaster's post-install hash is restored
// byte-for-byte from its backup (or deleted if graphmaster created it);
// otherwise only graphmaster's own insertions are removed.
func (e *Engine) Uninstall(opts Options, agents ...string) (*Report, error) {
	return e.run(opts, func(st *State) ([]*work, error) {
		want := agents
		if len(want) == 0 {
			want = st.Agents()
		}
		paths := make([]string, 0, len(st.Files))
		for p := range st.Files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		var works []*work
		for _, path := range paths {
			var drop []string
			for _, a := range st.Files[path].agents() {
				if slices.Contains(want, a) {
					drop = append(drop, a)
				}
			}
			if len(drop) == 0 {
				continue
			}
			w, err := loadWork(path, st)
			if err != nil {
				return nil, err
			}
			if err := w.planRemoval(drop); err != nil {
				return nil, err
			}
			works = append(works, w)
		}
		return works, nil
	})
}

// run loads state under the install lock, plans every file, reports, and
// commits unless this is a dry run.
func (e *Engine) run(opts Options, planFn func(*State) ([]*work, error)) (*Report, error) {
	statePath, err := e.env.StatePath()
	if err != nil {
		return nil, err
	}
	if !opts.DryRun {
		unlock, err := lockState(filepath.Dir(statePath))
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	st, err := LoadState(statePath)
	if err != nil {
		return nil, err
	}
	works, err := planFn(st)
	if err != nil {
		return nil, err
	}
	rep := &Report{DryRun: opts.DryRun}
	for _, w := range works {
		w.finalize()
		rep.Files = append(rep.Files, w.result())
	}
	if opts.DryRun {
		return rep, nil
	}
	commitErr := e.commit(st, works)
	if err := st.save(statePath); err != nil {
		return rep, errors.Join(commitErr, fmt.Errorf("save %s: %w", statePath, err))
	}
	return rep, commitErr
}

func lockState(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	fl := flock.New(filepath.Join(dir, "install.lock"))
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("lock install state: %w", err)
	}
	return func() { _ = fl.Unlock() }, nil
}

func loadWork(path string, st *State) (*work, error) {
	b, exists, err := readOptional(path)
	if err != nil {
		return nil, err
	}
	w := &work{
		path:    path,
		orig:    b,
		exists:  exists,
		content: b,
		keep:    exists,
		rec:     st.Files[path],
		ins:     map[string][]Insertion{},
	}
	if w.rec != nil {
		w.format = w.rec.Format
	}
	return w, nil
}

// planRemoval decides how to remove agents' insertions from the file.
func (w *work) planRemoval(agents []string) error {
	w.dropAgents = agents
	if !w.exists {
		w.action, w.note = ActionMissing, "file no longer exists"
		return nil
	}
	all := true
	for _, a := range w.rec.agents() {
		if !slices.Contains(agents, a) {
			all = false
		}
	}
	if all && w.rec.Restorable && hashBytes(w.orig) == w.rec.PostHash {
		if !w.rec.Existed {
			w.content, w.keep = nil, false
			return nil
		}
		if b, ok := w.readBackup(); ok {
			w.content, w.restored = b, true
			return nil
		}
		w.note = "backup missing or altered; removing graphmaster entries instead"
	}
	if err := w.removeSurgically(agents); err != nil {
		return err
	}
	if all && w.rec.Format == FormatGenerated && w.rec.Existed {
		// The whole file was ours; put the user's original back if we can.
		if b, ok := w.readBackup(); ok {
			w.content, w.keep, w.restored = b, true, true
		}
	}
	if all && !w.rec.Existed && w.keep && isEmptyResult(w.rec.Format, w.content) {
		w.content, w.keep = nil, false
	}
	return nil
}

func (w *work) readBackup() ([]byte, bool) {
	if w.rec.Backup == "" {
		return nil, false
	}
	b, err := os.ReadFile(w.rec.Backup)
	if err != nil || hashBytes(b) != w.rec.PreHash {
		return nil, false
	}
	return b, true
}

// commit writes backups, files and manifest records for every work item.
// It stops at the first error; records of files already written are kept
// so a later uninstall can still revert them.
func (e *Engine) commit(st *State, works []*work) error {
	backupRoot := ""
	for _, w := range works {
		rec := st.Files[w.path]
		if rec != nil && !w.exists && len(w.ins) > 0 {
			rec = nil // the file vanished since the last install; start over
		}
		fresh := rec == nil
		if fresh {
			if len(w.ins) == 0 {
				continue
			}
			rec = &FileRecord{Path: w.path, Format: w.format, Existed: w.exists, Restorable: true}
			if w.exists {
				rec.PreHash = hashBytes(w.orig)
			}
		}
		if w.changed() {
			if fresh && w.exists {
				if backupRoot == "" {
					root, err := e.newBackupRoot()
					if err != nil {
						return err
					}
					backupRoot = root
				}
				dst := filepath.Join(backupRoot, filepath.FromSlash(trimRoot(w.path)))
				if err := writeFileAtomic(dst, w.orig, 0o600); err != nil {
					return fmt.Errorf("backup %s: %w", w.path, err)
				}
				rec.Backup = dst
			} else if !fresh && hashBytes(w.orig) != rec.PostHash {
				rec.Restorable = false // user edits since our last write
			}
			if err := w.write(rec, fresh); err != nil {
				return err
			}
			rec.PostHash = hashBytes(w.content)
		} else if fresh {
			// Entries were already present (e.g. state was lost): nothing was
			// written, so there is no backup to restore from.
			rec.PostHash, rec.Restorable = hashBytes(w.orig), false
		}
		w.updateRecord(rec)
		if len(rec.Insertions) == 0 {
			delete(st.Files, w.path)
			if !w.keep {
				removeEmptyDirs(rec.CreatedDirs)
			}
			continue
		}
		st.Files[w.path] = rec
	}
	return nil
}

func (w *work) write(rec *FileRecord, fresh bool) error {
	if !w.keep {
		if err := os.Remove(w.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if fresh && !w.exists {
		dirs, err := mkdirAllRecord(filepath.Dir(w.path))
		if err != nil {
			return err
		}
		rec.CreatedDirs = dirs
	}
	mode := w.mode
	if mode == 0 {
		mode = 0o644
	}
	if err := writeFileAtomic(w.path, w.content, mode); err != nil {
		return fmt.Errorf("write %s: %w", w.path, err)
	}
	return nil
}

// updateRecord replaces the insertions and containers of every agent this
// run touched.
func (w *work) updateRecord(rec *FileRecord) {
	touched := slices.Clone(w.dropAgents)
	touched = append(touched, w.agentOrder...)
	var ins []Insertion
	for _, in := range rec.Insertions {
		if !slices.Contains(touched, in.Agent) {
			ins = append(ins, in)
		}
	}
	for _, a := range w.agentOrder {
		ins = append(ins, w.ins[a]...)
	}
	rec.Insertions = ins

	var created []Container
	for _, c := range append(slices.Clone(rec.Created), w.created...) {
		if slices.Contains(w.dropAgents, c.Agent) && !slices.Contains(w.agentOrder, c.Agent) {
			continue
		}
		if !slices.Contains(created, c) {
			created = append(created, c)
		}
	}
	rec.Created = created
	if w.format != "" {
		rec.Format = w.format
	}
}

func (e *Engine) newBackupRoot() (string, error) {
	dir, err := e.env.BackupDir()
	if err != nil {
		return "", err
	}
	ts := e.env.now().UTC().Format("20060102T150405.000000000Z")
	root := filepath.Join(dir, ts)
	for i := 1; ; i++ {
		if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
			break
		}
		root = filepath.Join(dir, fmt.Sprintf("%s-%d", ts, i))
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return root, nil
}

// trimRoot turns an absolute path into a relative one for mirroring under
// the backup directory (drive letters on Windows become a directory).
func trimRoot(p string) string {
	p = filepath.ToSlash(p)
	if v := filepath.VolumeName(p); v != "" {
		p = filepath.ToSlash(v[:1]) + p[len(v):]
	}
	for len(p) > 0 && p[0] == '/' {
		p = p[1:]
	}
	return p
}
