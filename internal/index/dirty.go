package index

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/parse"
	"github.com/pragmataW/graphmaster/internal/repo"
	"github.com/pragmataW/graphmaster/internal/store"
)

// Meta keys owned by this package (in addition to the store's well-known
// keys). Neither is part of the canonical dump.
const (
	metaConfigSig = "config_sig"
	metaDepPrefix = "dep:"
	// metaFilesGen changes in every batch that writes files rows, so a
	// process can keep a snapshot of the files table between scans.
	metaFilesGen = "files_gen"
)

// cand is a file that should be in the graph: a tracked or untracked,
// non-excluded, regular file with a supported extension.
type cand struct {
	tracked bool
	entry   *repo.IndexEntry // index entry when the index was decoded
	stat    repo.FileStat
}

// workItem is a file whose content must be (re)processed.
type workItem struct {
	path    string
	old     *store.File // nil for a new file
	tracked bool
}

// plan is the outcome of one scan: everything Freshen needs to decide
// whether to write and, if so, what.
type plan struct {
	start    time.Time
	meta     map[string]string
	deps     map[string]string
	lastScan time.Time
	state    string
	bulk     bool // a bulk build crashed before FinishBulk
	pending  bool // pending_names or the per-file resolution queue is not empty

	idxSig     repo.StatSig
	idxChanged bool

	cands map[string]cand
	db    map[string]*store.File

	work     []*workItem
	statOnly []*store.File // complete new rows
	deletes  []*store.File
	// hints are dirty_hints rows as stored (path -> ts); hinted holds
	// their normalized paths.
	hints  map[string]int64
	hinted map[string]bool

	dirChanged []repo.DirRow
	dirRemoved []string

	xvers       map[model.Lang]int // current extractor versions (0 = not indexed)
	xverChanged bool
	cfgSig      string
	cfgChanged  bool
	// reset is non-empty when the whole graph must be rebuilt.
	reset string
	// staleDeps are recorded dependencies whose answer changed; their
	// owners (depOwners, file ids) are reprocessed and the keys dropped.
	staleDeps []string
	depOwners map[int64]bool
}

// pendingFiles counts files whose graph or row is out of date.
func (p *plan) pendingFiles() int { return len(p.work) + len(p.statOnly) + len(p.deletes) }

// needsWrite reports whether the graph must be written under the lock.
func (p *plan) needsWrite(haveResolver bool) bool {
	return p.pendingFiles() > 0 || len(p.hints) > 0 || p.reset != "" || len(p.staleDeps) > 0 ||
		p.state != store.StateReady || p.bulk || p.xverChanged || p.cfgChanged ||
		(p.pending && haveResolver)
}

// needsCachePersist reports cache-only changes (directory listings, index
// signature) worth persisting opportunistically.
func (p *plan) needsCachePersist() bool {
	return p.idxChanged || len(p.dirChanged) > 0 || len(p.dirRemoved) > 0
}

// scan runs freshness steps 2-5 without taking the write lock.
func (ix *Indexer) scan(ctx context.Context, forceReset string) (*plan, error) {
	p := &plan{start: ix.opts.Now(), reset: forceReset}
	st := ix.Store()
	if err := p.loadMeta(ctx, st.DB()); err != nil {
		return nil, err
	}
	if p.state == "" && p.reset == "" && st.Rebuilt() {
		p.reset = "rebuilt: " + st.RebuildReason()
	}
	p.xvers = currentXVers(ix.cfg)
	p.cfgSig = configSig(ix.cfg)
	if old, ok := p.meta[metaConfigSig]; ok && old != p.cfgSig && p.reset == "" {
		p.reset = "configuration changed"
	}
	p.cfgChanged = p.meta[metaConfigSig] != p.cfgSig
	stored := storedXVers(p.meta)
	for _, l := range model.AllLangs() {
		if stored[l] != p.xvers[l] {
			p.xverChanged = true
		}
	}

	p.db = map[string]*store.File{}
	if p.reset == "" {
		db, err := ix.filesSnapshot(ctx, p.meta[metaFilesGen])
		if err != nil {
			return nil, err
		}
		p.db = db
	}
	// Filesystem dependencies are verified while discovery runs; both
	// are bound by metadata I/O.
	fsDeps := make(chan []string, 1)
	go func() { fsDeps <- changedFSDeps(ix.loc.Root, p.deps) }()
	if err := ix.discover(ctx, p); err != nil {
		<-fsDeps
		return nil, err
	}
	changed := append(<-fsDeps, changedCodeDeps(p.deps, p.cands)...)
	if p.reset == "" && len(changed) > 0 {
		sort.Strings(changed)
		if err := p.planDepChanges(ctx, st.DB(), changed); err != nil {
			return nil, err
		}
	}
	if p.reset != "" {
		p.db = map[string]*store.File{}
	}
	hints, err := loadHints(ctx, st.DB())
	if err != nil {
		return nil, err
	}
	p.hints = hints
	p.hinted = make(map[string]bool, len(hints))
	for raw := range hints {
		p.hinted[ix.hintPath(raw)] = true
	}
	if err := ix.classify(ctx, p, stored); err != nil {
		return nil, err
	}
	return p, nil
}

// planDepChanges handles recorded dependencies whose answer changed: the
// files they were consulted for are reprocessed (from cache) instead of
// rebuilding the whole graph. An answer consulted without an owner (file
// id 0) still forces a full rebuild.
func (p *plan) planDepChanges(ctx context.Context, db *sql.DB, changed []string) error {
	owners := map[int64]bool{}
	for _, k := range changed {
		rows, err := db.QueryContext(ctx, `SELECT file_id FROM dep_owners WHERE key = ?`, k)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			if id == 0 {
				rows.Close()
				p.reset = "dependency changed: " + k
				return nil
			}
			owners[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	p.staleDeps, p.depOwners = changed, owners
	return nil
}

func (p *plan) loadMeta(ctx context.Context, db *sql.DB) error {
	p.meta = map[string]string{}
	p.deps = map[string]string{}
	rows, err := db.QueryContext(ctx, `SELECT key, value FROM meta`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		if d, ok := strings.CutPrefix(k, metaDepPrefix); ok {
			p.deps[d] = v
		} else {
			p.meta[k] = v
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	p.state = p.meta[store.MetaState]
	p.bulk = p.meta[store.MetaBulk] == "1"
	if ns, err := strconv.ParseInt(p.meta[store.MetaLastScanNS], 10, 64); err == nil {
		p.lastScan = time.Unix(0, ns)
	}
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pending_names)`).Scan(&p.pending); err != nil {
		return err
	}
	for k := range p.meta {
		if strings.HasPrefix(k, metaPendingFile) || strings.HasPrefix(k, metaPendingDependents) ||
			strings.HasPrefix(k, metaPendingRemoved) {
			p.pending = true
			break
		}
	}
	return nil
}

func loadHints(ctx context.Context, db *sql.DB) (map[string]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT path, ts FROM dirty_hints`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var (
			p  string
			ts int64
		)
		if err := rows.Scan(&p, &ts); err != nil {
			return nil, err
		}
		out[p] = ts
	}
	return out, rows.Err()
}

// hintPath normalizes a hinted path: absolute paths inside the worktree
// become repo-relative.
func (ix *Indexer) hintPath(raw string) string {
	if filepath.IsAbs(raw) {
		if rel, ok := ix.loc.Root.Rel(raw); ok {
			return rel
		}
		return ""
	}
	return model.CleanRel(raw)
}

// discover builds the candidate set (steps 2-4): tracked files from the
// git index (or, when .git/index is unchanged, from the files table),
// untracked files from the walk, then a parallel lstat sweep.
func (ix *Indexer) discover(ctx context.Context, p *plan) error {
	root := ix.loc.Root
	sig, err := repo.StatSigOf(root.IndexPath())
	if err != nil {
		return err
	}
	p.idxSig = sig
	p.idxChanged = p.meta[store.MetaGitIndexStat] != sig.String()

	gc := repo.LoadGitConfig(root)
	ig, err := repo.NewIgnore(root, gc, ix.excludes())
	if err != nil {
		return err
	}

	p.cands = make(map[string]cand, len(p.db)+64)
	var tracked *repo.TrackedSet
	decoded := false
	decode := func() error {
		idx, err := repo.ReadIndex(ctx, root)
		if err != nil {
			return err
		}
		ix.lastIndex = idx
		tracked = repo.NewTrackedSet(idx.Entries, gc.IgnoreCase)
		for path, c := range p.cands {
			if c.tracked {
				delete(p.cands, path)
			}
		}
		files := idx.Files()
		for i := range files {
			e := &files[i]
			if e.Stat.Mode == repo.ModeSymlink || !model.IsCodePath(e.Path) || ig.ExcludedByConfig(e.Path) {
				continue
			}
			p.cands[e.Path] = cand{tracked: true, entry: e}
		}
		decoded = true
		return nil
	}
	if !p.idxChanged && len(p.db) > 0 {
		// Step 2 shortcut: the index is unchanged, so the tracked files
		// are exactly the tracked rows we stored.
		for path, f := range p.db {
			if f.Tracked {
				p.cands[path] = cand{tracked: true}
			}
		}
		tracked = ix.trackedFromSnapshot(p.db, gc.IgnoreCase, p.meta[metaFilesGen] != "")
	} else if err := decode(); err != nil {
		return err
	}

	// Step 3: untracked files via the persisted directory cache.
	ix.dirMu.Lock()
	res, err := repo.Walk(ctx, repo.WalkOptions{
		Root: root, Tracked: tracked, Ignore: ig, Cache: ix.dirCache, ScanStart: p.start,
	})
	if err == nil {
		ix.notePendingDirs(res.ChangedDirs, res.RemovedDirs)
		p.dirChanged, p.dirRemoved = ix.pendingDirRows()
	}
	ix.dirMu.Unlock()
	if err != nil {
		return err
	}
	var untracked []string
	for _, u := range res.Untracked {
		if !u.Symlink && model.IsCodePath(u.Path) {
			untracked = append(untracked, u.Path)
		}
	}
	if !decoded {
		// A file we have never seen may be tracked even though the index
		// file did not change (e.g. restored from elsewhere): ask the index.
		for _, u := range untracked {
			if _, known := p.db[u]; !known {
				if err := decode(); err != nil {
					return err
				}
				break
			}
		}
	}
	for _, u := range untracked {
		if decoded && tracked.Contains(u) {
			continue
		}
		if _, isTracked := p.cands[u]; !isTracked {
			p.cands[u] = cand{}
		}
	}

	// Step 4: parallel lstat sweep. lstat does not scale past a few
	// workers on APFS, so a small pool is as fast as a large one.
	paths := make([]string, 0, len(p.cands))
	for path := range p.cands {
		paths = append(paths, path)
	}
	stats, err := repo.StatPaths(ctx, root, paths, statWorkers)
	if err != nil {
		return err
	}
	for i, path := range paths {
		ps := stats[i]
		if !ps.Exists || ps.Err != nil || (ps.Stat.Mode != repo.ModeRegular && ps.Stat.Mode != repo.ModeExecutable) {
			if ps.Err != nil {
				ix.opts.Logf("index: lstat %s: %v", path, ps.Err)
			}
			delete(p.cands, path)
			continue
		}
		c := p.cands[path]
		c.stat = ps.Stat
		p.cands[path] = c
	}
	return nil
}

// filesSnapshot returns the files table keyed by path. The map is shared
// between scans while the generation is unchanged and must not be
// modified.
func (ix *Indexer) filesSnapshot(ctx context.Context, gen string) (map[string]*store.File, error) {
	ix.snapMu.Lock()
	defer ix.snapMu.Unlock()
	if gen != "" && gen == ix.snapGen && ix.snapFiles != nil {
		return ix.snapFiles, nil
	}
	if gen != "" {
		if db, ok := readSnapshot(ix.loc.Dir, gen); ok {
			ix.snapGen, ix.snapFiles, ix.snapTracked = gen, db, nil
			return db, nil
		}
	}
	db := map[string]*store.File{}
	if err := ix.Store().ForEachFile(ctx, func(f *store.File) error {
		db[f.Path] = f
		return nil
	}); err != nil {
		return nil, err
	}
	ix.snapGen, ix.snapFiles, ix.snapTracked = gen, db, nil
	if gen != "" {
		if err := writeSnapshot(ix.loc.Dir, gen, db); err != nil {
			ix.opts.Logf("index: write files snapshot: %v", err)
		}
	}
	return db, nil
}

func (ix *Indexer) dropSnapshot() {
	ix.snapMu.Lock()
	ix.snapGen, ix.snapFiles, ix.snapTracked = "", nil, nil
	ix.snapMu.Unlock()
}

// trackedFromSnapshot builds (once per snapshot) the tracked set implied
// by the stored rows, for the unchanged-index shortcut of step 2.
// cacheable is set when db is the shared snapshot (see filesSnapshot).
func (ix *Indexer) trackedFromSnapshot(db map[string]*store.File, ignoreCase, cacheable bool) *repo.TrackedSet {
	ix.snapMu.Lock()
	defer ix.snapMu.Unlock()
	if cacheable && ix.snapTracked != nil && ix.snapTrackedIC == ignoreCase {
		return ix.snapTracked
	}
	entries := make([]repo.IndexEntry, 0, len(db))
	for path, f := range db {
		if f.Tracked {
			entries = append(entries, repo.IndexEntry{Path: path, Stat: repo.FileStat{Mode: f.Mode}})
		}
	}
	ts := repo.NewTrackedSet(entries, ignoreCase)
	if cacheable {
		ix.snapTracked, ix.snapTrackedIC = ts, ignoreCase
	}
	return ts
}

// statWorkers bounds the lstat sweep (see repo.StatPaths).
const statWorkers = 12

// excludes returns the config excludes plus the state directory itself.
func (ix *Indexer) excludes() []string {
	return append(ix.cfg.AllExcludes(), "/"+DirName+"/")
}

// classify implements step 5: compare each candidate with its stored row
// and decide add / modify / stat-only / delete.
func (ix *Indexer) classify(ctx context.Context, p *plan, stored map[model.Lang]int) error {
	force := map[model.Lang]bool{}
	for _, l := range model.AllLangs() {
		if stored[l] != p.xvers[l] {
			force[l] = true
		}
	}
	type keyJob struct {
		path string
		old  *store.File
		c    cand
	}
	// Owners of changed dependencies are reprocessed even when unchanged.
	owned := map[string]bool{}
	if len(p.depOwners) > 0 {
		for path, f := range p.db {
			if p.depOwners[f.ID] {
				owned[path] = true
			}
		}
	}
	var jobs []keyJob
	for path, c := range p.cands {
		old := p.db[path]
		hinted := p.hinted[path]
		switch {
		case old == nil:
			p.work = append(p.work, &workItem{path: path, tracked: c.tracked})
		case force[old.Lang] || owned[path]:
			p.work = append(p.work, &workItem{path: path, old: old, tracked: c.tracked})
		case hinted || !statClean(old, c.stat, p.lastScan):
			jobs = append(jobs, keyJob{path, old, c})
		case old.Tracked != c.tracked:
			row := *old
			row.Tracked = c.tracked
			p.statOnly = append(p.statOnly, &row)
		}
	}
	for path, f := range p.db {
		if _, ok := p.cands[path]; !ok {
			p.deletes = append(p.deletes, f)
		}
	}

	// Content keys, hashed in parallel where the index cannot vouch.
	keys := make([]repo.BlobID, len(jobs))
	newStats := make([]repo.FileStat, len(jobs))
	errs := make([]error, len(jobs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0)*2)
	for i, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			keys[i], newStats[i], errs[i] = ix.contentKey(j.path, j.c, p.start)
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	for i, j := range jobs {
		if errs[i] != nil {
			// Vanished or unreadable since the sweep: let the pipeline
			// decide (it deletes files that are gone).
			p.work = append(p.work, &workItem{path: j.path, old: j.old, tracked: j.c.tracked})
			continue
		}
		if !bytes.Equal(j.old.Blob, keys[i][:]) {
			p.work = append(p.work, &workItem{path: j.path, old: j.old, tracked: j.c.tracked})
			continue
		}
		row := *j.old
		row.Tracked = j.c.tracked
		setStat(&row, newStats[i], p.start)
		if !rowEqual(&row, j.old) {
			p.statOnly = append(p.statOnly, &row)
		}
		// Hinted but unchanged files need no row write; the hint itself
		// is cleared under the lock.
	}
	sort.Slice(p.work, func(a, b int) bool { return p.work[a].path < p.work[b].path })
	sort.Slice(p.statOnly, func(a, b int) bool { return p.statOnly[a].Path < p.statOnly[b].Path })
	sort.Slice(p.deletes, func(a, b int) bool { return p.deletes[a].Path < p.deletes[b].Path })
	return nil
}

// rowEqual compares the columns a stat-only update can change.
func rowEqual(a, b *store.File) bool {
	return a.MtimeNS == b.MtimeNS && a.Size == b.Size && a.Ino == b.Ino && a.Mode == b.Mode &&
		a.Tracked == b.Tracked && bytes.Equal(a.Blob, b.Blob)
}

// statClean is the racy-git rule: the stored stat equals the current one
// and the stored mtime is safely older than the scan that recorded it.
// Racy rows are stored with a zero mtime (see setStat), so they never
// compare equal and are always re-checked by content.
func statClean(old *store.File, cur repo.FileStat, lastScan time.Time) bool {
	stored := repo.FileStat{MtimeNS: old.MtimeNS, Size: old.Size, Ino: old.Ino, Mode: old.Mode}
	if lastScan.IsZero() {
		return false
	}
	return repo.IsClean(stored, cur, lastScan)
}

// setStat stores st in f, smudging the mtime when it is too close to the
// scan start to prove later that the content is unchanged (git's racy-git
// smudging).
func setStat(f *store.File, st repo.FileStat, scanStart time.Time) {
	f.MtimeNS, f.Size, f.Ino, f.Mode = st.MtimeNS, st.Size, st.Ino, st.Mode
	if repo.IsRacy(st.MtimeNS, scanStart) {
		f.MtimeNS = 0
	}
}

// contentKey returns the git blob id of a candidate: from the index when
// its cached stat proves the file clean, from the in-memory memo when the
// file was hashed with the same stat, otherwise by hashing the file.
func (ix *Indexer) contentKey(path string, c cand, scanStart time.Time) (repo.BlobID, repo.FileStat, error) {
	if c.entry != nil && ix.lastIndex != nil && ix.lastIndex.Clean(c.entry, c.stat) {
		return c.entry.Blob, c.stat, nil
	}
	if m, ok := ix.memoGet(path); ok && m.stat == c.stat {
		return m.blob, c.stat, nil
	}
	id, st, err := repo.HashPath(ix.loc.Root.Abs(path))
	if err != nil {
		return repo.BlobID{}, repo.FileStat{}, err
	}
	if !repo.IsRacy(st.MtimeNS, scanStart) {
		ix.memoPut(path, keyMemo{stat: st, blob: id})
	}
	return id, st, nil
}

// keyMemo remembers a content key computed during an unlocked scan so the
// re-scan under the lock does not hash the same file again.
type keyMemo struct {
	stat repo.FileStat
	blob repo.BlobID
}

const maxKeyMemo = 1 << 16

func (ix *Indexer) memoGet(path string) (keyMemo, bool) {
	ix.memoMu.Lock()
	defer ix.memoMu.Unlock()
	m, ok := ix.memo[path]
	return m, ok
}

func (ix *Indexer) memoPut(path string, m keyMemo) {
	ix.memoMu.Lock()
	defer ix.memoMu.Unlock()
	if len(ix.memo) >= maxKeyMemo {
		clear(ix.memo)
	}
	ix.memo[path] = m
}

// currentXVers returns the extractor version of every enabled, registered
// language (absent = not indexed).
func currentXVers(cfg *config.Config) map[model.Lang]int {
	out := map[model.Lang]int{}
	for _, l := range model.AllLangs() {
		if spec, ok := parse.Lookup(l); ok && cfg.LangEnabled(l) {
			out[l] = spec.ExtractorVersion()
		}
	}
	return out
}

func storedXVers(meta map[string]string) map[model.Lang]int {
	out := map[model.Lang]int{}
	for k, v := range meta {
		name, ok := strings.CutPrefix(k, store.MetaExtractorVerPf)
		if !ok {
			continue
		}
		l, err := model.ParseLang(name)
		n, err2 := strconv.Atoi(v)
		if err == nil && err2 == nil {
			out[l] = n
		}
	}
	return out
}

// configSig captures the settings that change file statuses or the
// candidate set: the size cap and the exclude list.
func configSig(cfg *config.Config) string {
	return fmt.Sprintf("max=%d;ex=%s", int64(cfg.MaxFileSize), strings.Join(cfg.AllExcludes(), "\x00"))
}
