package repo

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // the index trailer is SHA-1
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// ErrUnsupportedIndex marks index files this decoder cannot interpret
// (unknown version, split or sparse index, other required extensions).
var ErrUnsupportedIndex = errors.New("unsupported git index")

const (
	indexHeaderLen   = 12
	indexEntryFixed  = 62 // stat fields (40) + sha (20) + flags (2)
	indexTrailerLen  = sha1.Size
	flagNameMask     = 0x0fff
	flagExtended     = 0x4000
	extSkipWorktree  = 0x4000
	extIntentToAdd   = 0x2000
	indexStageShift  = 12
	indexStageMask   = 0x3
	maxIndexEntryLen = 1 << 20
)

// DecodeIndex parses an index file (versions 2-4). It is a single-pass,
// allocation-light decoder: all paths share one backing string. Optional
// extensions are skipped; required ones yield ErrUnsupportedIndex.
//
// go-git's plumbing/format/index decoder is used as the reference
// implementation in tests, but it allocates ~16 objects per entry and
// hashes with sha1cd, which is too slow for the 100k-entry budget.
func DecodeIndex(data []byte) (*Index, error) {
	if len(data) < indexHeaderLen+indexTrailerLen || !bytes.Equal(data[:4], []byte("DIRC")) {
		return nil, fmt.Errorf("%w: bad header", ErrUnsupportedIndex)
	}
	body, trailer := data[:len(data)-indexTrailerLen], data[len(data)-indexTrailerLen:]
	if !allZero(trailer) { // index.skipHash writes a zero trailer
		if sum := sha1.Sum(body); !bytes.Equal(sum[:], trailer) { //nolint:gosec
			return nil, fmt.Errorf("%w: checksum mismatch", ErrUnsupportedIndex)
		}
	}
	version := binary.BigEndian.Uint32(data[4:8])
	if version < 2 || version > 4 {
		return nil, fmt.Errorf("%w: version %d", ErrUnsupportedIndex, version)
	}
	count := int(binary.BigEndian.Uint32(data[8:12]))
	if count > len(body)/indexEntryFixed {
		return nil, fmt.Errorf("%w: entry count %d exceeds file size", ErrUnsupportedIndex, count)
	}

	d := indexDecoder{data: body, pos: indexHeaderLen, version: version}
	idx := &Index{Version: version, Entries: make([]IndexEntry, count)}
	if err := d.entries(idx.Entries); err != nil {
		return nil, err
	}
	if err := d.extensions(); err != nil {
		return nil, err
	}
	return idx, nil
}

type indexDecoder struct {
	data    []byte
	pos     int
	version uint32
	// v2/v3: names are substrings of one copy of data.
	all string
	// v4: names are rebuilt into an arena; offsets recorded per entry.
	arena []byte
	prev  []byte
}

func (d *indexDecoder) entries(out []IndexEntry) error {
	var spans []int // v4 only: arena end offset per entry
	if d.version == 4 {
		spans = make([]int, len(out))
		d.arena = make([]byte, 0, len(d.data)/2)
	} else {
		d.all = string(d.data)
	}
	for i := range out {
		if err := d.entry(&out[i], i, spans); err != nil {
			return fmt.Errorf("index entry %d: %w", i, err)
		}
	}
	if d.version == 4 {
		names := string(d.arena)
		start := 0
		for i := range out {
			out[i].Path = names[start:spans[i]]
			start = spans[i]
		}
	}
	return nil
}

func (d *indexDecoder) entry(e *IndexEntry, i int, spans []int) error {
	b := d.data[d.pos:]
	if len(b) < indexEntryFixed {
		return fmt.Errorf("%w: truncated entry", ErrUnsupportedIndex)
	}
	be := binary.BigEndian
	mtimeSec, mtimeNsec := be.Uint32(b[8:12]), be.Uint32(b[12:16])
	e.Stat = FileStat{
		MtimeNS: int64(mtimeSec)*1e9 + int64(mtimeNsec),
		Ino:     uint64(be.Uint32(b[20:24])),
		Mode:    be.Uint32(b[24:28]),
		Size:    int64(be.Uint32(b[36:40])),
	}
	copy(e.Blob[:], b[40:60])
	flags := be.Uint16(b[60:62])
	e.Stage = uint8(flags>>indexStageShift) & indexStageMask
	off := indexEntryFixed
	if flags&flagExtended != 0 {
		if d.version < 3 || len(b) < off+2 {
			return fmt.Errorf("%w: bad extended flags", ErrUnsupportedIndex)
		}
		ext := be.Uint16(b[off : off+2])
		e.SkipWorktree = ext&extSkipWorktree != 0
		e.IntentToAdd = ext&extIntentToAdd != 0
		off += 2
	}

	if d.version == 4 {
		return d.nameV4(b, off, i, spans)
	}
	nameLen := int(flags & flagNameMask)
	if nameLen == flagNameMask { // long name: NUL-terminated
		n := bytes.IndexByte(b[off:], 0)
		if n < 0 {
			return fmt.Errorf("%w: unterminated name", ErrUnsupportedIndex)
		}
		nameLen = n
	}
	if off+nameLen >= len(b) {
		return fmt.Errorf("%w: truncated name", ErrUnsupportedIndex)
	}
	start := d.pos + off
	e.Path = d.all[start : start+nameLen]
	// Entries are NUL padded to a multiple of 8 bytes (at least one NUL).
	d.pos += (off + nameLen + 8) &^ 7
	return nil
}

// nameV4 decodes a prefix-compressed path: a varint count of bytes to strip
// from the previous path, then a NUL-terminated suffix. No padding.
func (d *indexDecoder) nameV4(b []byte, off, i int, spans []int) error {
	strip, n := decodeIndexVarint(b[off:])
	if n == 0 || strip > uint64(len(d.prev)) {
		return fmt.Errorf("%w: bad v4 strip length", ErrUnsupportedIndex)
	}
	off += n
	end := bytes.IndexByte(b[off:], 0)
	if end < 0 || end > maxIndexEntryLen {
		return fmt.Errorf("%w: unterminated name", ErrUnsupportedIndex)
	}
	keep := d.prev[:len(d.prev)-int(strip)]
	startArena := len(d.arena)
	d.arena = append(d.arena, keep...)
	d.arena = append(d.arena, b[off:off+end]...)
	d.prev = d.arena[startArena:]
	spans[i] = len(d.arena)
	d.pos += off + end + 1
	return nil
}

// decodeIndexVarint decodes git's offset varint (varint.c). n is 0 on error.
func decodeIndexVarint(b []byte) (val uint64, n int) {
	if len(b) == 0 {
		return 0, 0
	}
	c := b[0]
	val = uint64(c & 0x7f)
	n = 1
	for c&0x80 != 0 {
		if n >= len(b) || n > 9 {
			return 0, 0
		}
		val++
		c = b[n]
		n++
		val = val<<7 + uint64(c&0x7f)
	}
	return val, n
}

// extensions walks the extension blocks, rejecting required ones.
func (d *indexDecoder) extensions() error {
	for d.pos < len(d.data) {
		if len(d.data)-d.pos < 8 {
			return fmt.Errorf("%w: truncated extension header", ErrUnsupportedIndex)
		}
		sig := d.data[d.pos : d.pos+4]
		size := int(binary.BigEndian.Uint32(d.data[d.pos+4 : d.pos+8]))
		if sig[0] < 'A' || sig[0] > 'Z' {
			return fmt.Errorf("%w: required extension %q", ErrUnsupportedIndex, sig)
		}
		if size < 0 || size > len(d.data)-d.pos-8 {
			return fmt.Errorf("%w: extension %q overflows", ErrUnsupportedIndex, sig)
		}
		d.pos += 8 + size
	}
	return nil
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func timeFromNS(ns int64) time.Time { return time.Unix(0, ns) }
