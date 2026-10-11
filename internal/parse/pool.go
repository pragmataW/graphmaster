package parse

import (
	"context"
	"runtime"
	"sync"

	"github.com/pragmataW/graphmaster/internal/model"
)

// Job is one file to parse.
type Job struct {
	Spec model.LanguageSpec
	Src  []byte
	Tag  any // caller data passed through to Output (e.g. a file id)
}

// Output is the result of a Job.
type Output struct {
	Tag    any
	Result Result
	Err    error
}

// Pool is a fixed set of Engines, one per worker. Each Engine owns its
// parsers and query cursors, so workers never share mutable tree-sitter
// state; compiled queries are shared read-only. A Pool is safe for
// concurrent use.
type Pool struct {
	engines chan *Engine
	all     []*Engine
	size    int

	mu     sync.RWMutex // guards closed against in-flight borrows
	closed bool
	wg     sync.WaitGroup
}

// NewPool creates a pool of n engines; n <= 0 means GOMAXPROCS.
func NewPool(n int) *Pool {
	if n <= 0 {
		n = runtime.GOMAXPROCS(0)
	}
	p := &Pool{engines: make(chan *Engine, n), all: make([]*Engine, n), size: n}
	for i := range n {
		e := NewEngine()
		p.all[i] = e
		p.engines <- e
	}
	return p
}

// Size returns the number of engines (the useful parallelism).
func (p *Pool) Size() int { return p.size }

// acquire borrows an engine, blocking until one is free or ctx is done.
func (p *Pool) acquire(ctx context.Context) (*Engine, error) {
	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return nil, ErrClosed
	}
	p.wg.Add(1)
	p.mu.RUnlock()
	select {
	case e := <-p.engines:
		return e, nil
	case <-ctx.Done():
		p.wg.Done()
		return nil, ctx.Err()
	}
}

func (p *Pool) release(e *Engine) {
	p.engines <- e
	p.wg.Done()
}

// Parse parses one file on a borrowed engine.
func (p *Pool) Parse(ctx context.Context, spec model.LanguageSpec, src []byte) (Result, error) {
	e, err := p.acquire(ctx)
	if err != nil {
		return Result{}, err
	}
	defer p.release(e)
	return e.Parse(ctx, spec, src)
}

// Run parses jobs with up to Size() workers until jobs is closed or ctx is
// done, sending one Output per job to out. It does not close out. A worker
// keeps its engine for the whole run. It returns ctx.Err() if cancelled.
func (p *Pool) Run(ctx context.Context, jobs <-chan Job, out chan<- Output) error {
	var (
		wg      sync.WaitGroup
		errOnce sync.Once
		runErr  error
	)
	for range p.size {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, err := p.acquire(ctx)
			if err != nil {
				errOnce.Do(func() { runErr = err })
				return
			}
			defer p.release(e)
			p.work(ctx, e, jobs, out)
		}()
	}
	wg.Wait()
	if runErr != nil {
		return runErr
	}
	return ctx.Err()
}

func (p *Pool) work(ctx context.Context, e *Engine, jobs <-chan Job, out chan<- Output) {
	for {
		select {
		case <-ctx.Done():
			return
		case j, ok := <-jobs:
			if !ok {
				return
			}
			res, err := e.Parse(ctx, j.Spec, j.Src)
			select {
			case out <- Output{Tag: j.Tag, Result: res, Err: err}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// Close waits for borrowed engines to be returned and releases all native
// resources. Further use returns ErrClosed. It is idempotent.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()
	p.wg.Wait()
	for _, e := range p.all {
		e.Close()
	}
}
