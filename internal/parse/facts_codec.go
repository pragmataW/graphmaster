package parse

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/pragmataW/graphmaster/internal/model"
)

// Binary encoding of model.Facts for the parse cache (cache.db).
//
// Layout: magic "GF", format version, then sections in field order. Integers
// are uvarints; values that may go backwards (parent/enclosing indices,
// positions relative to the previous record) are zigzag varints. Strings are
// interned: uvarint(0) introduces a new string (uvarint length + bytes) and
// appends it to the table, uvarint(n>0) refers to table entry n-1. Any
// model.Facts value round-trips exactly, except that empty slices decode as
// nil.

const (
	codecMagic0  = 'G'
	codecMagic1  = 'F'
	codecVersion = 1
)

// ErrCorruptFacts is wrapped by every DecodeFacts error.
var ErrCorruptFacts = errors.New("parse: corrupt facts encoding")

// EncodeFacts returns the binary encoding of f.
func EncodeFacts(f *model.Facts) []byte {
	return AppendFacts(make([]byte, 0, 64+16*(len(f.Defs)+len(f.Refs))), f)
}

// AppendFacts appends the binary encoding of f to dst.
func AppendFacts(dst []byte, f *model.Facts) []byte {
	e := encoder{buf: dst, table: map[string]uint64{}}
	e.buf = append(e.buf, codecMagic0, codecMagic1, codecVersion)
	e.uint(uint64(f.Lang))
	e.str(f.Package)
	e.bool(f.TestFile)

	e.uint(uint64(len(f.Defs)))
	var prevLine, prevByte uint32
	for i := range f.Defs {
		d := &f.Defs[i]
		e.uint(uint64(d.Kind))
		e.str(d.Name)
		e.int(int64(d.Parent))
		e.strList(d.Scope)
		e.delta(prevLine, d.StartLine)
		e.uint(uint64(d.StartCol))
		e.delta(d.StartLine, d.EndLine)
		e.uint(uint64(d.EndCol))
		e.delta(prevByte, d.StartByte)
		e.delta(d.StartByte, d.EndByte)
		e.str(d.Sig)
		e.uint(uint64(d.Flags))
		prevLine, prevByte = d.StartLine, d.StartByte
	}

	e.uint(uint64(len(f.Refs)))
	prevLine = 0
	for i := range f.Refs {
		r := &f.Refs[i]
		e.uint(uint64(r.Kind))
		e.str(r.Name)
		e.str(r.Qualifier)
		e.str(r.Receiver)
		e.int(int64(r.Enclosing))
		e.delta(prevLine, r.Line)
		e.uint(uint64(r.Col))
		prevLine = r.Line
	}

	e.uint(uint64(len(f.Imports)))
	prevLine = 0
	for i := range f.Imports {
		im := &f.Imports[i]
		e.str(im.Spec)
		e.str(im.Alias)
		e.strList(im.Names)
		e.bool(im.Wildcard)
		e.delta(prevLine, im.Line)
		prevLine = im.Line
	}

	e.uint(uint64(len(f.Inherits)))
	prevLine = 0
	for i := range f.Inherits {
		in := &f.Inherits[i]
		e.int(int64(in.Child))
		e.uint(uint64(in.Kind))
		e.str(in.Name)
		e.str(in.Qualifier)
		e.delta(prevLine, in.Line)
		e.uint(uint64(in.Col))
		prevLine = in.Line
	}

	e.uint(uint64(len(f.Tests)))
	for i := range f.Tests {
		e.int(int64(f.Tests[i].Def))
		e.str(f.Tests[i].Target)
	}
	return e.buf
}

type encoder struct {
	buf   []byte
	table map[string]uint64 // string -> table index + 1
}

func (e *encoder) uint(v uint64) { e.buf = binary.AppendUvarint(e.buf, v) }
func (e *encoder) int(v int64)   { e.buf = binary.AppendVarint(e.buf, v) }

func (e *encoder) bool(b bool) {
	if b {
		e.buf = append(e.buf, 1)
	} else {
		e.buf = append(e.buf, 0)
	}
}

// delta writes cur relative to prev as a zigzag varint.
func (e *encoder) delta(prev, cur uint32) { e.int(int64(cur) - int64(prev)) }

func (e *encoder) str(s string) {
	if ref, ok := e.table[s]; ok {
		e.uint(ref)
		return
	}
	e.table[s] = uint64(len(e.table) + 1)
	e.uint(0)
	e.uint(uint64(len(s)))
	e.buf = append(e.buf, s...)
}

func (e *encoder) strList(ss []string) {
	e.uint(uint64(len(ss)))
	for _, s := range ss {
		e.str(s)
	}
}

// DecodeFacts decodes data produced by EncodeFacts. It never panics on
// malformed input; errors wrap ErrCorruptFacts. The result is not validated
// semantically (see model.Facts.Validate).
func DecodeFacts(data []byte) (f *model.Facts, err error) {
	d := decoder{buf: data}
	if len(data) < 3 || data[0] != codecMagic0 || data[1] != codecMagic1 {
		return nil, fmt.Errorf("%w: bad magic", ErrCorruptFacts)
	}
	if data[2] != codecVersion {
		return nil, fmt.Errorf("%w: unsupported version %d", ErrCorruptFacts, data[2])
	}
	d.pos = 3

	f = &model.Facts{}
	f.Lang = model.Lang(d.u8())
	f.Package = d.str()
	f.TestFile = d.bool()

	if n := d.count(); n > 0 {
		f.Defs = make([]model.Def, n)
		var prevLine, prevByte uint32
		for i := range f.Defs {
			x := &f.Defs[i]
			x.Kind = model.NodeKind(d.u8())
			x.Name = d.str()
			x.Parent = d.i32()
			x.Scope = d.strList()
			x.StartLine = d.delta(prevLine)
			x.StartCol = d.u32()
			x.EndLine = d.delta(x.StartLine)
			x.EndCol = d.u32()
			x.StartByte = d.delta(prevByte)
			x.EndByte = d.delta(x.StartByte)
			x.Sig = d.str()
			x.Flags = model.DefFlags(d.u16())
			prevLine, prevByte = x.StartLine, x.StartByte
		}
	}

	if n := d.count(); n > 0 {
		f.Refs = make([]model.Ref, n)
		var prevLine uint32
		for i := range f.Refs {
			r := &f.Refs[i]
			r.Kind = model.RefKind(d.u8())
			r.Name = d.str()
			r.Qualifier = d.str()
			r.Receiver = d.str()
			r.Enclosing = d.i32()
			r.Line = d.delta(prevLine)
			r.Col = d.u32()
			prevLine = r.Line
		}
	}

	if n := d.count(); n > 0 {
		f.Imports = make([]model.Import, n)
		var prevLine uint32
		for i := range f.Imports {
			im := &f.Imports[i]
			im.Spec = d.str()
			im.Alias = d.str()
			im.Names = d.strList()
			im.Wildcard = d.bool()
			im.Line = d.delta(prevLine)
			prevLine = im.Line
		}
	}

	if n := d.count(); n > 0 {
		f.Inherits = make([]model.Inherit, n)
		var prevLine uint32
		for i := range f.Inherits {
			in := &f.Inherits[i]
			in.Child = d.i32()
			in.Kind = model.EdgeKind(d.u8())
			in.Name = d.str()
			in.Qualifier = d.str()
			in.Line = d.delta(prevLine)
			in.Col = d.u32()
			prevLine = in.Line
		}
	}

	if n := d.count(); n > 0 {
		f.Tests = make([]model.TestMark, n)
		for i := range f.Tests {
			f.Tests[i].Def = d.i32()
			f.Tests[i].Target = d.str()
		}
	}

	if d.err != nil {
		return nil, d.err
	}
	if d.pos != len(d.buf) {
		return nil, fmt.Errorf("%w: %d trailing bytes", ErrCorruptFacts, len(d.buf)-d.pos)
	}
	return f, nil
}

// decoder reads sequentially; after the first error every read returns the
// zero value, so callers check err once at the end.
type decoder struct {
	buf   []byte
	pos   int
	err   error
	table []string
}

func (d *decoder) fail(format string, args ...any) {
	if d.err == nil {
		d.err = fmt.Errorf("%w: %s at byte %d", ErrCorruptFacts, fmt.Sprintf(format, args...), d.pos)
	}
}

func (d *decoder) uint() uint64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Uvarint(d.buf[d.pos:])
	if n <= 0 {
		d.fail("bad uvarint")
		return 0
	}
	d.pos += n
	return v
}

func (d *decoder) int() int64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Varint(d.buf[d.pos:])
	if n <= 0 {
		d.fail("bad varint")
		return 0
	}
	d.pos += n
	return v
}

func (d *decoder) bounded(limit uint64, what string) uint64 {
	v := d.uint()
	if v > limit {
		d.fail("%s %d out of range", what, v)
		return 0
	}
	return v
}

func (d *decoder) u8() uint8   { return uint8(d.bounded(math.MaxUint8, "byte value")) }
func (d *decoder) u16() uint16 { return uint16(d.bounded(math.MaxUint16, "uint16 value")) }
func (d *decoder) u32() uint32 { return uint32(d.bounded(math.MaxUint32, "uint32 value")) }

func (d *decoder) i32() int32 {
	v := d.int()
	if v < math.MinInt32 || v > math.MaxInt32 {
		d.fail("int32 value %d out of range", v)
		return 0
	}
	return int32(v)
}

func (d *decoder) bool() bool {
	switch d.uint() {
	case 0:
		return false
	case 1:
		return true
	}
	d.fail("bad bool")
	return false
}

// delta reads a zigzag offset from prev and checks the result fits uint32.
func (d *decoder) delta(prev uint32) uint32 {
	v := int64(prev) + d.int()
	if v < 0 || v > math.MaxUint32 {
		d.fail("position %d out of range", v)
		return 0
	}
	return uint32(v)
}

// count reads a collection or string length. Every element takes at least
// one byte, so a length larger than the input left after it is corrupt; this
// bounds allocations on hostile input.
func (d *decoder) count() int {
	v := d.uint()
	if rest := uint64(len(d.buf) - d.pos); v > rest {
		d.fail("length %d exceeds remaining %d bytes", v, rest)
		return 0
	}
	return int(v)
}

func (d *decoder) str() string {
	ref := d.uint()
	if d.err != nil {
		return ""
	}
	if ref > 0 {
		if ref > uint64(len(d.table)) {
			d.fail("string ref %d out of range", ref)
			return ""
		}
		return d.table[ref-1]
	}
	n := d.count()
	if d.err != nil {
		return ""
	}
	s := string(d.buf[d.pos : d.pos+n])
	d.pos += n
	d.table = append(d.table, s)
	return s
}

func (d *decoder) strList() []string {
	n := d.count()
	if n == 0 {
		return nil
	}
	out := make([]string, n)
	for i := range out {
		out[i] = d.str()
	}
	return out
}
