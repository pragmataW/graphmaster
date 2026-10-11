package index

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
)

// apply writes a plan under the write lock (PLAN steps 8-10).
func (ix *Indexer) apply(ctx context.Context, p *plan) (res *Result, err error) {
	st := ix.Store()
	res = &Result{Rebuilt: p.reset}
	if p.bulk {
		// A bulk build crashed before FinishBulk: restore the secondary
		// indexes and search tables, then continue incrementally.
		ix.opts.Logf("index: recovering interrupted bulk build")
		if err := st.FinishBulk(ctx); err != nil {
			return nil, fmt.Errorf("index: bulk recovery: %w", err)
		}
	}
	full := len(p.db) == 0 && len(p.work) > 0
	res.FullBuild = full
	rc := NewRepoCtx(ix.loc.Root, p.codeSet())

	commits := 0
	w, err := st.NewWriter(ctx, store.WriterOptions{
		Bulk:     full,
		MaxFiles: ix.opts.BatchFiles,
		MaxDelay: ix.opts.BatchDelay,
		OnCommit: func(int) { commits++; ix.crashCheck(commits) },
	})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = w.Abort()
		}
		if cerr := w.Close(); err == nil && cerr != nil {
			err = cerr
		}
	}()

	// Every batch that commits files rows also changes files_gen, so
	// snapshots of the files table held by other processes are dropped.
	gen := newGenToken()
	ix.dropSnapshot()
	if err = w.SetMeta(metaFilesGen, gen+".0"); err != nil {
		return nil, err
	}
	building := full || p.state != store.StateReady
	if p.reset != "" {
		if err = resetGraph(w, p); err != nil {
			return nil, err
		}
	}
	if building {
		if err = w.SetMeta(store.MetaState, store.StateBuilding); err != nil {
			return nil, err
		}
		if err = w.SetMeta(store.MetaProgress, "0"); err != nil {
			return nil, err
		}
	}
	if err = w.Flush(); err != nil { // readers see "building" right away
		return nil, err
	}

	total, done, lastPct := p.pendingFiles(), 0, 0
	fileDone := func() error {
		if err := persistDeps(w, rc); err != nil {
			return err
		}
		done++
		if err := w.SetMeta(metaFilesGen, gen+"."+strconv.Itoa(done)); err != nil {
			return err
		}
		if building && total > 0 {
			if pct := done * 100 / total; pct != lastPct {
				lastPct = pct
				if err := w.SetMeta(store.MetaProgress, strconv.Itoa(pct)); err != nil {
					return err
				}
			}
		}
		if err := w.FileDone(); err != nil {
			return err
		}
		if ix.opts.Progress != nil {
			ix.opts.Progress(done, total)
		}
		return nil
	}

	if err = writeDirRows(w, p); err != nil {
		return nil, err
	}
	for path, ts := range p.hints {
		if _, err = w.Exec(`DELETE FROM dirty_hints WHERE path = ? AND ts <= ?`, path, ts); err != nil {
			return nil, err
		}
	}

	link := newLinker(w, rc)
	// Graphs written before link dependencies were recorded relink once.
	link.relinkAll = p.meta[metaLinkDeps] != linkDepsVersion
	if len(p.staleDeps) > 0 {
		// Drop the changed answers; their owners, reprocessed (and so
		// relinked) below, record the current ones.
		for _, k := range p.staleDeps {
			if _, err = w.Exec(`DELETE FROM meta WHERE key = ?`, metaDepPrefix+k); err != nil {
				return nil, err
			}
			if _, err = w.Exec(`DELETE FROM dep_owners WHERE key = ?`, k); err != nil {
				return nil, err
			}
		}
	}
	g := &graphWriter{w: w, rc: rc, link: link}
	if h, ok := ix.opts.Resolver.(IncrementalHints); ok {
		g.hints = h
	}
	for _, f := range p.deletes {
		if err = g.deleteFile(f); err != nil {
			return nil, err
		}
		res.Deleted++
		if err = fileDone(); err != nil {
			return nil, err
		}
	}
	for _, f := range p.statOnly {
		if _, err = w.UpsertFile(f); err != nil {
			return nil, err
		}
		res.StatOnly++
		if err = fileDone(); err != nil {
			return nil, err
		}
	}

	var cachePuts []store.CacheEntry
	flushCache := func() {
		if len(cachePuts) == 0 {
			return
		}
		if cerr := ix.cache.PutBatch(ctx, cachePuts); cerr != nil {
			ix.opts.Logf("index: cache put: %v", cerr)
		}
		cachePuts = cachePuts[:0]
	}
	err = ix.runPipeline(ctx, p.work, p.start, func(fr *fileResult) error {
		if fr.cachePut != nil {
			if cachePuts = append(cachePuts, *fr.cachePut); len(cachePuts) >= cacheFlushSize {
				flushCache()
			}
		}
		if fr.parsed {
			res.Parsed++
		}
		if fr.cacheHit {
			res.CacheHits++
		}
		old := fr.item.old
		switch {
		case fr.gone && old == nil:
			// Created and removed again since the scan.
		case fr.gone:
			if err := g.deleteFile(old); err != nil {
				return err
			}
			res.Deleted++
		default:
			if old == nil {
				if err := link.fileAdded(fr.row.Path); err != nil {
					return err
				}
				res.Added++
			} else {
				res.Modified++
			}
			if fr.row.Status == model.StatusParseErr {
				res.ParseErrors++
			}
			if err := g.writeFile(fr.row, old, fr.facts); err != nil {
				return err
			}
		}
		return fileDone()
	})
	flushCache()
	if err != nil {
		return nil, err
	}
	if err = link.finish(); err != nil {
		return nil, err
	}
	if p.reset == "" && (p.xverChanged || p.cfgChanged) {
		if err = writeVersionMeta(w, p); err != nil {
			return nil, err
		}
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	ix.persisted(p)

	if full {
		if err = st.FinishBulk(ctx); err != nil {
			return nil, err
		}
	}
	if err = ix.resolve(ctx, st, rc, full); err != nil {
		return nil, err
	}
	return res, ix.finalize(ctx, st, p, rc)
}

// resolve runs the configured Resolver over the queued names. A full build
// uses FullResolver when the resolver provides it.
func (ix *Indexer) resolve(ctx context.Context, st *store.Store, rc *RepoCtx, full bool) error {
	r := ix.opts.Resolver
	if r == nil {
		return nil
	}
	var err error
	if fr, ok := r.(FullResolver); ok && full {
		err = fr.ResolveAll(ctx, st, rc)
	} else {
		err = r.ResolvePending(ctx, st, rc)
	}
	if err != nil {
		return fmt.Errorf("index: resolve: %w", err)
	}
	return nil
}

// newGenToken returns a random token identifying one writing run.
func newGenToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// finalize records the completed scan and marks the graph ready.
func (ix *Indexer) finalize(ctx context.Context, st *store.Store, p *plan, rc *RepoCtx) (err error) {
	w, err := st.NewWriter(ctx, store.WriterOptions{})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = w.Abort()
		}
		if cerr := w.Close(); err == nil {
			err = cerr
		}
	}()
	if err = persistDeps(w, rc); err != nil {
		return err
	}
	for _, kv := range [][2]string{
		{store.MetaLastScanNS, strconv.FormatInt(p.start.UnixNano(), 10)},
		{store.MetaGitIndexStat, p.idxSig.String()},
		{metaConfigSig, p.cfgSig},
		{store.MetaState, store.StateReady},
	} {
		if err = w.SetMeta(kv[0], kv[1]); err != nil {
			return err
		}
	}
	_, err = w.Exec(`DELETE FROM meta WHERE key = ?`, store.MetaProgress)
	return err
}
