// Package parse turns source files into language-neutral model.Facts using
// tree-sitter grammars and per-language extraction queries.
//
// Languages self-register (see Register) from lang_<x>.go files that embed a
// queries/<x>.scm query. The query follows a capture convention documented in
// queries/go.scm; the generic extractor in extract.go interprets it, and
// optional hooks (hooks.go) refine the result per language.
package parse

import (
	"context"
	"errors"
	"fmt"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/pragmataW/graphmaster/internal/model"
)

// Result is the outcome of parsing one file.
type Result struct {
	Facts *model.Facts
	// SyntaxErrors is set when the tree contains ERROR or MISSING nodes.
	// Facts are still extracted best-effort (model.StatusParseErr).
	SyntaxErrors bool
}

// langState is the per-language mutable state owned by one Engine.
type langState struct {
	c      *compiled
	parser *sitter.Parser
	cursor *sitter.QueryCursor
}

func (s *langState) close() {
	s.cursor.Close()
	s.parser.Close()
}

// Engine parses files. It owns one tree-sitter Parser and QueryCursor per
// language, created lazily, while the compiled Query is shared process-wide.
// An Engine is not safe for concurrent use; use a Pool for parallelism.
// Close must be called to release native memory.
type Engine struct {
	states map[model.Lang]*langState
	closed bool
}

// NewEngine returns an empty Engine.
func NewEngine() *Engine {
	return &Engine{states: map[model.Lang]*langState{}}
}

// ErrClosed is returned when using a closed Engine or Pool.
var ErrClosed = errors.New("parse: closed")

// Close releases all parsers and cursors. It is idempotent.
func (e *Engine) Close() {
	if e.closed {
		return
	}
	e.closed = true
	for l, s := range e.states {
		s.close()
		delete(e.states, l)
	}
}

func (e *Engine) state(spec model.LanguageSpec) (*langState, error) {
	c, err := compileSpec(spec)
	if err != nil {
		return nil, err
	}
	l := spec.Lang()
	if s, ok := e.states[l]; ok {
		if s.c == c {
			return s, nil
		}
		// A different spec/query for the same language (tests): rebuild.
		s.close()
		delete(e.states, l)
	}
	p := sitter.NewParser()
	if err := p.SetLanguage(c.lang); err != nil {
		p.Close()
		return nil, fmt.Errorf("parse: %v: %w", l, err)
	}
	s := &langState{c: c, parser: p, cursor: sitter.NewQueryCursor()}
	e.states[l] = s
	return s, nil
}

// Parse extracts facts from src using spec. ctx cancels a long parse.
func (e *Engine) Parse(ctx context.Context, spec model.LanguageSpec, src []byte) (Result, error) {
	if e.closed {
		return Result{}, ErrClosed
	}
	s, err := e.state(spec)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	// Plain Parse on purpose: in go-tree-sitter v0.25 ParseWithOptions never
	// releases its options handle (a per-call leak) and ParseCtx is
	// deprecated and racy. Inputs are capped at 1 MiB upstream, so a parse
	// is short; ctx is checked between the phases instead.
	tree := s.parser.Parse(src, nil)
	if tree == nil {
		s.parser.Reset()
		return Result{}, fmt.Errorf("parse: %v: parser returned no tree", spec.Lang())
	}
	defer tree.Close()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	root := tree.RootNode()
	facts := extract(spec, s.c, s.cursor, root, src)
	return Result{Facts: facts, SyntaxErrors: root.HasError()}, nil
}

// ParseLang is Parse for a registered language.
func (e *Engine) ParseLang(ctx context.Context, l model.Lang, src []byte) (Result, error) {
	spec, ok := Lookup(l)
	if !ok {
		return Result{}, fmt.Errorf("parse: language %v not registered", l)
	}
	return e.Parse(ctx, spec, src)
}
