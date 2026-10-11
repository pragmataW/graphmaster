package misc

import (
	"errors"
	tsitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/x/y/v2"
)

// Grouped type declaration.
type (
	Celsius float64
	Pair    struct {
		List[int]
		*y.Thing
	}
)

// Method whose receiver type lives in another file of the package.
func (c *Config) Load(path string) error {
	return c.parse(path)
}

// Package-level function value: references inside belong to the variable.
var handler = func(name string) error {
	return errors.New(name)
}

// Error embeds the predeclared error interface (dropped) and a generic one.
type Error interface {
	error
	Wrapper[int]
	Code() int
}

func chains(p *tsitter.Parser) {
	p.Language().Name()
	(helper)()
	f := Celsius(3.5)
	_ = Map[int, string]([]int{1}, nil)
	_ = y.New().Do()
	_ = f
}

func helper() {}
