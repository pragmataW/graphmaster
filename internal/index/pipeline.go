package index

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/parse"
	"github.com/pragmataW/graphmaster/internal/repo"
	"github.com/pragmataW/graphmaster/internal/store"
)

// cacheFlushSize bounds the parse-cache entries buffered before a PutBatch.
const cacheFlushSize = 256

// cachedFacts wraps parse.EncodeFacts with a flags byte, because a cache
// hit must also restore the syntax-error status of the parse.
const (
	cacheFormatV1      = 1
	cacheFlagSyntaxErr = 1
)

// fileResult is one processed work item, produced by a worker and consumed
// by the single writer goroutine.
type fileResult struct {
	item     *workItem
	gone     bool // vanished since the scan
	row      store.File
	facts    *model.Facts
	parsed   bool
	cacheHit bool
	cachePut *store.CacheEntry
	err      error // fatal for the run (cancellation, I/O on the cache)
}

// runPipeline processes items with a guarded worker pool and hands the
// results, one at a time, to handle on the calling goroutine. The channel
// between them is bounded, so a slow writer applies back-pressure.
func (ix *Indexer) runPipeline(ctx context.Context, items []*workItem, scanStart time.Time, handle func(*fileResult) error) error {
	if len(items) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	workers := max(1, min(ix.opts.Workers, len(items)))
	jobs := make(chan *workItem)
	results := make(chan *fileResult, 2*workers)

	go func() {
		defer close(jobs)
		for _, it := range items {
			select {
			case jobs <- it:
			case <-ctx.Done():
				return
			}
		}
	}()
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			eng := parse.NewEngine()
			defer func() { eng.Close() }()
			for it := range jobs {
				fr, broken := ix.process(ctx, eng, it, scanStart)
				if broken {
					// A panic may leave tree-sitter state inconsistent.
					eng.Close()
					eng = parse.NewEngine()
				}
				select {
				case results <- fr:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	var firstErr error
	for fr := range results {
		if firstErr != nil {
			continue // drain so workers can exit
		}
		if fr.err == nil {
			fr.err = handle(fr)
		}
		if fr.err != nil {
			firstErr = fr.err
			cancel()
		}
	}
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// process reads, filters, and parses (or loads from cache) one file. It
// never panics: a panicking parse marks the file parse_err and reports
// broken so the worker replaces its engine.
func (ix *Indexer) process(ctx context.Context, eng *parse.Engine, it *workItem, scanStart time.Time) (fr *fileResult, broken bool) {
	fr = &fileResult{item: it}
	if err := ctx.Err(); err != nil {
		fr.err = err
		return fr, false
	}
	abs := ix.loc.Root.Abs(it.path)
	st, err := repo.Lstat(abs)
	if err != nil || (st.Mode != repo.ModeRegular && st.Mode != repo.ModeExecutable) {
		fr.gone = true
		return fr, false
	}
	lang := model.LangFromPath(it.path)
	fr.row = store.File{Path: it.path, Lang: lang, Tracked: it.tracked}
	setStat(&fr.row, st, scanStart)

	maxSize := int64(ix.cfg.MaxFileSize)
	var content []byte
	if st.Size > maxSize {
		blob, err := hashStream(abs, st.Size)
		if err != nil {
			return ix.unreadable(fr, err), false
		}
		fr.row.Blob = blob[:]
	} else {
		content, err = os.ReadFile(abs)
		if err != nil {
			return ix.unreadable(fr, err), false
		}
		blob := repo.BlobSHA1(content)
		fr.row.Blob = blob[:]
	}
	status := repo.Classify(it.path, st.Size, content, maxSize)
	spec, registered := parse.Lookup(lang)
	if status == model.StatusOK && (!registered || !ix.cfg.LangEnabled(lang)) {
		status = model.StatusUnsupported
	}
	fr.row.Status = status
	if status != model.StatusOK {
		return fr, false
	}
	fr.row.LOC = countLines(content)

	xver := spec.ExtractorVersion()
	facts, synErr, hit := ix.cacheGet(ctx, fr.row.Blob, lang, xver)
	if hit {
		fr.cacheHit = true
	} else {
		res, perr, panicked := ix.safeParse(ctx, eng, spec, it.path, content)
		broken = panicked
		if err := ctx.Err(); err != nil {
			fr.err = err
			return fr, broken
		}
		fr.parsed = true
		if perr != nil {
			ix.opts.Logf("index: parse %s: %v", it.path, perr)
			fr.row.Status = model.StatusParseErr
			return fr, broken
		}
		facts, synErr = res.Facts, res.SyntaxErrors
		fr.cachePut = &store.CacheEntry{Blob: fr.row.Blob, Lang: lang, XVer: xver, Facts: encodeCached(facts, synErr)}
	}
	if synErr {
		fr.row.Status = model.StatusParseErr
	}
	// TestFile depends on the path, so it is never part of cached facts.
	facts.TestFile = facts.TestFile || parse.IsTestPath(it.path)
	fr.facts = facts
	return fr, broken
}

// safeParse runs the parser, converting a panic into an error.
func (ix *Indexer) safeParse(ctx context.Context, eng *parse.Engine, spec model.LanguageSpec, path string, src []byte) (res parse.Result, err error, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			err, panicked = fmt.Errorf("parser panic: %v", r), true
		}
	}()
	if ix.parseHook != nil {
		ix.parseHook(path)
	}
	res, err = eng.Parse(ctx, spec, src)
	if err == nil && res.Facts == nil {
		err = errors.New("parser returned no facts")
	}
	return res, err, false
}

func (ix *Indexer) cacheGet(ctx context.Context, blob []byte, lang model.Lang, xver int) (*model.Facts, bool, bool) {
	b, ok, err := ix.cache.Get(ctx, blob, lang, xver)
	if err != nil {
		ix.opts.Logf("index: cache get: %v", err)
		return nil, false, false
	}
	if !ok {
		return nil, false, false
	}
	facts, synErr, err := decodeCached(b)
	if err != nil {
		ix.opts.Logf("index: cache entry: %v", err)
		return nil, false, false
	}
	return facts, synErr, true
}

func encodeCached(f *model.Facts, synErr bool) []byte {
	flags := byte(0)
	if synErr {
		flags |= cacheFlagSyntaxErr
	}
	return parse.AppendFacts([]byte{cacheFormatV1, flags}, f)
}

func decodeCached(b []byte) (*model.Facts, bool, error) {
	if len(b) < 2 || b[0] != cacheFormatV1 {
		return nil, false, errors.New("unknown cache entry format")
	}
	f, err := parse.DecodeFacts(b[2:])
	if err != nil {
		return nil, false, err
	}
	return f, b[1]&cacheFlagSyntaxErr != 0, nil
}

func hashStream(abs string, size int64) (repo.BlobID, error) {
	f, err := os.Open(abs)
	if err != nil {
		return repo.BlobID{}, err
	}
	defer f.Close()
	return repo.BlobSHA1Reader(f, size)
}

// unreadable handles a read failure: a vanished file leaves the graph; any
// other error keeps a row without content (status unsupported) so the file
// is not retried until its stat changes.
func (ix *Indexer) unreadable(fr *fileResult, err error) *fileResult {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		fr.gone = true
		return fr
	}
	ix.opts.Logf("index: read %s: %v", fr.item.path, err)
	fr.row.Status = model.StatusUnsupported
	fr.row.Blob = nil
	return fr
}

func countLines(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n := bytes.Count(b, []byte{'\n'})
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}
