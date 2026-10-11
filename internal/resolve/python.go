package resolve

import (
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/parse"
)

// pyResolver adds Python scoping to the generic resolver. It relies on the
// parse layer's conventions (see internal/parse/lang_python.go):
//
//   - every from-import is one Import per name with Spec "module.name" and
//     Names [name]; references and qualifiers are already de-aliased to the
//     imported name, so from-imports bind nothing here and are reached
//     through Session.NamedImport (T1);
//   - "import a" binds a and "import a.b as c" binds c; a dotted import
//     without alias binds nothing, so a.b.f() matches the module "a.b" by
//     its fully qualified path (T1);
//   - top-level definitions are "owned" by their module's last segment
//     (OwnerName), which lets pricing.apply_discount() resolve through
//     "from . import pricing" with the framework's owner-chain check;
//   - builtins and the roots of out-of-repo imports are external;
//   - receiver typing (T3) reads the source with tree-sitter
//     (python_types.go): self/cls, super(), annotated parameters, local and
//     self.attr assignments, constructor calls and return annotations.
type pyResolver struct{ Generic }

func init() { Register(model.LangPython, pyResolver{}) }

// ImportBindings implements LangResolver.
func (pyResolver) ImportBindings(imp Import) []string {
	switch {
	case imp.Wildcard() || len(imp.Names) > 0:
		return nil
	case imp.Alias != "":
		return []string{imp.Alias}
	case !strings.Contains(imp.Spec, "."):
		return []string{imp.Spec}
	}
	return nil
}

// OwnerName implements LangResolver: a top-level definition belongs to its
// module, named by the module's last segment, unless the file also defines
// a type of that name (which would make its functions look like members).
func (pyResolver) OwnerName(s *Session, d *Def) string {
	if d.ParentID != 0 {
		return ""
	}
	f := s.File(d.FileID)
	if f == nil {
		return ""
	}
	mod := s.Module(f)
	seg := mod[strings.LastIndexByte(mod, '.')+1:]
	if seg == "" {
		return ""
	}
	for _, t := range s.DefsNamed(seg) {
		if t.FileID == d.FileID && t.ParentID == 0 && t.Kind.IsType() {
			return ""
		}
	}
	return seg
}

// ResolveType implements LangResolver.
func (r pyResolver) ResolveType(s *Session, from *File, qual, name string) TypeResult {
	if qual == "" {
		if parse.PythonBuiltin(name) && !r.definesLocally(s, from, name) {
			return TypeResult{External: true}
		}
	} else if r.externalRoot(s, from, qual) {
		return TypeResult{External: true}
	}
	return s.DefaultResolveType(from, qual, name)
}

// definesLocally reports whether file f defines or imports name, shadowing
// a builtin.
func (pyResolver) definesLocally(s *Session, f *File, name string) bool {
	for _, d := range s.DefsNamed(name) {
		if d.FileID == f.ID {
			return true
		}
	}
	_, ok := s.NamedImport(f, name)
	return ok
}

// externalRoot reports whether the first segment of qual is bound only by
// dotted imports pointing outside the repository ("import os.path" makes
// os.getcwd external).
func (pyResolver) externalRoot(s *Session, f *File, qual string) bool {
	root, _, _ := strings.Cut(normQual(qual), ".")
	found := false
	for _, im := range s.Imports(f) {
		if im.Alias != "" || len(im.Names) > 0 {
			continue
		}
		if r, _, _ := strings.Cut(im.Spec, "."); r != root {
			continue
		}
		if s.ImportTarget(f, im.Spec).Resolved() {
			return false
		}
		found = true
	}
	return found
}

// Receiver implements LangResolver.
func (pyResolver) Receiver(s *Session, site *Site) Receiver {
	return pyReceiver(s, site)
}
