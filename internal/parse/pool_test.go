package parse

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

func genGoSource(i int) []byte {
	return fmt.Appendf(nil, `package p%d

type T%d struct{ n int }

func (t *T%d) M() int { return helper(t.n) }

func helper(n int) int { return n + %d }
`, i%7, i, i, i)
}

func sequentialFacts(t *testing.T, srcs [][]byte) []*model.Facts {
	t.Helper()
	e := NewEngine()
	defer e.Close()
	out := make([]*model.Facts, len(srcs))
	for i, s := range srcs {
		res, err := e.ParseLang(context.Background(), model.LangGo, s)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = res.Facts
	}
	return out
}

func TestPoolRunMatchesSequential(t *testing.T) {
	const n = 300
	srcs := make([][]byte, n)
	for i := range srcs {
		srcs[i] = genGoSource(i)
	}
	want := sequentialFacts(t, srcs)
	spec, _ := Lookup(model.LangGo)

	p := NewPool(4)
	defer p.Close()
	jobs := make(chan Job)
	out := make(chan Output, 8)
	go func() {
		defer close(jobs)
		for i, s := range srcs {
			jobs <- Job{Spec: spec, Src: s, Tag: i}
		}
	}()
	errc := make(chan error, 1)
	go func() { errc <- p.Run(context.Background(), jobs, out); close(out) }()

	seen := 0
	for o := range out {
		if o.Err != nil {
			t.Fatal(o.Err)
		}
		i := o.Tag.(int)
		if !reflect.DeepEqual(o.Result.Facts, want[i]) {
			t.Fatalf("job %d differs from sequential parse", i)
		}
		seen++
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if seen != n {
		t.Fatalf("got %d outputs, want %d", seen, n)
	}
}

func TestPoolConcurrentParse(t *testing.T) {
	p := NewPool(3)
	defer p.Close()
	spec, _ := Lookup(model.LangGo)
	srcs := make([][]byte, 40)
	for i := range srcs {
		srcs[i] = genGoSource(i)
	}
	want := sequentialFacts(t, srcs)

	var wg sync.WaitGroup
	errs := make(chan error, 16*len(srcs))
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range srcs {
				i := (k + g) % len(srcs)
				res, err := p.Parse(context.Background(), spec, srcs[i])
				if err != nil {
					errs <- err
					return
				}
				if !reflect.DeepEqual(res.Facts, want[i]) {
					errs <- fmt.Errorf("goroutine %d: src %d mismatch", g, i)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestPoolCancelAndClose(t *testing.T) {
	p := NewPool(2)
	if p.Size() != 2 {
		t.Fatalf("size %d", p.Size())
	}
	ctx, cancel := context.WithCancel(context.Background())
	jobs := make(chan Job) // never fed
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx, jobs, make(chan Output)) }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v", err)
	}
	p.Close()
	p.Close()
	spec, _ := Lookup(model.LangGo)
	if _, err := p.Parse(context.Background(), spec, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("Parse after Close: %v", err)
	}
	if NewPool(0).Size() < 1 {
		t.Fatal("default pool size")
	}
}
