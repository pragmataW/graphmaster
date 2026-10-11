package parse

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// DumpFacts renders facts as stable, human-readable text for golden tests
// and debugging. Definitions are shown with their qualified name; references
// and inheritance edges name their enclosing definition.
func DumpFacts(f *model.Facts) string {
	var b strings.Builder
	qname := func(i int32) string {
		if i == model.NoParent {
			return "-"
		}
		return fmt.Sprintf("#%d %s", i, strings.Join(f.QNameParts(i), "."))
	}

	fmt.Fprintf(&b, "lang %v\npackage %s\ntestfile %t\n", f.Lang, strconv.Quote(f.Package), f.TestFile)

	fmt.Fprintf(&b, "\ndefs %d\n", len(f.Defs))
	for i := range f.Defs {
		d := &f.Defs[i]
		fmt.Fprintf(&b, "  #%d %s %s  %d:%d-%d:%d", i, d.Kind, strings.Join(f.QNameParts(int32(i)), "."),
			d.StartLine, d.StartCol, d.EndLine, d.EndCol)
		if d.Parent != model.NoParent {
			fmt.Fprintf(&b, "  parent=#%d", d.Parent)
		}
		if len(d.Scope) > 0 {
			fmt.Fprintf(&b, "  scope=%s", strings.Join(d.Scope, "."))
		}
		if d.Flags != 0 {
			fmt.Fprintf(&b, "  [%s]", d.Flags)
		}
		fmt.Fprintf(&b, "\n      sig %s\n", strconv.Quote(d.Sig))
	}

	fmt.Fprintf(&b, "\nrefs %d\n", len(f.Refs))
	for _, r := range f.Refs {
		fmt.Fprintf(&b, "  %d:%d %s %s", r.Line, r.Col, r.Kind, r.Name)
		if r.Qualifier != "" {
			fmt.Fprintf(&b, "  qual=%s", r.Qualifier)
		}
		if r.Receiver != "" {
			fmt.Fprintf(&b, "  recv=%s", r.Receiver)
		}
		fmt.Fprintf(&b, "  in %s\n", qname(r.Enclosing))
	}

	fmt.Fprintf(&b, "\nimports %d\n", len(f.Imports))
	for _, im := range f.Imports {
		fmt.Fprintf(&b, "  %d %s", im.Line, strconv.Quote(im.Spec))
		if im.Alias != "" {
			fmt.Fprintf(&b, "  alias=%s", im.Alias)
		}
		if len(im.Names) > 0 {
			fmt.Fprintf(&b, "  names=%s", strings.Join(im.Names, ","))
		}
		if im.Wildcard {
			b.WriteString("  wildcard")
		}
		b.WriteByte('\n')
	}

	fmt.Fprintf(&b, "\ninherits %d\n", len(f.Inherits))
	for _, in := range f.Inherits {
		target := in.Name
		if in.Qualifier != "" {
			target = in.Qualifier + "." + in.Name
		}
		fmt.Fprintf(&b, "  %d:%d %s %s %s\n", in.Line, in.Col, qname(in.Child), in.Kind, target)
	}

	fmt.Fprintf(&b, "\ntests %d\n", len(f.Tests))
	for _, t := range f.Tests {
		fmt.Fprintf(&b, "  %s -> %s\n", qname(t.Def), strconv.Quote(t.Target))
	}
	return b.String()
}
