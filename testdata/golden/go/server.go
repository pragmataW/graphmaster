// Package server is a golden fixture covering Go definitions and references.
package server

import "fmt"

import (
	"context"
	stdlog "log"
	. "strings"
	_ "embed"

	"example.com/app/internal/store"
	"gopkg.in/yaml.v3"
)

// Version and friends are grouped constants.
const (
	Version = "1.0"
	maxConn = 10
)

const Single int = iota

var (
	DefaultServer *Server
	counter       int
)

var a, B = 1, 2

// Handler is a function type.
type Handler func(ctx context.Context) error

// ID is a type alias.
type ID = string

// Start is declared before its receiver type to exercise attachment.
func (s *Server) Start(ctx context.Context) error {
	s.log.Printf("starting %s", s.addr)
	if err := s.db.Open(ctx); err != nil {
		return fmt.Errorf("open: %w", err)
	}
	go func() {
		helper(s.addr)
	}()
	return nil
}

// Server embeds a local type and a qualified pointer type.
type Server struct {
	Base
	*store.Client
	addr     string
	log, Log *stdlog.Logger
	db       store.DB `json:"db"`
}

// Base is embedded by Server.
type Base struct{ Name string }

// Store is an interface with an embedded interface.
type Store interface {
	fmt.Stringer
	Reader
	Get(ctx context.Context, id ID) (*Item, error)
	put(item Item) error
}

// Reader is embedded by Store.
type Reader interface {
	Read(p []byte) (int, error)
}

// Item is generic-free data.
type Item struct {
	Key   string
	Value []byte
}

// Number is a constraint interface with a union, which is not embedding.
type Number interface {
	~int | ~float64
}

// List is a generic container.
type List[T any] struct {
	items []T
}

// Push uses the receiver's type parameter.
func (l *List[T]) Push(v T) {
	l.items = append(l.items, v)
}

// Map is a generic function.
func Map[T, U any](in []T, f func(T) U) []U {
	out := make([]U, 0, len(in))
	for _, v := range in {
		out = append(out, f(v))
	}
	return out
}

func (s Server) String() string { return Join([]string{s.addr}, ",") }

func helper(addr string) {
	type local struct{ n int }
	l := local{n: len(addr)}
	_ = l
	v, _ := yaml.Marshal(Item{Key: addr})
	stdlog.Println(string(v))
	srv := NewServer(addr)
	srv.Start(context.Background())
	_ = DefaultServer.addr
}

// NewServer builds a Server.
func NewServer(addr string) *Server {
	return &Server{addr: addr, db: store.Open()}
}
