package index

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
)

// snapshotName is a derived copy of the files table, labelled with the
// files_gen it was read at. Loading 100k rows from SQLite costs ~130ms;
// decoding the snapshot a fraction of that, which matters for short-lived
// hook processes. A snapshot is used only when its label equals the
// current files_gen. Since files_gen is written in the same transaction as
// every files change, and the label is read before the rows, equal labels
// imply identical content.
const snapshotName = "files.snap"

const snapshotMagic = "GMFILES1"

func encodeSnapshot(gen string, files map[string]*store.File) []byte {
	var b bytes.Buffer
	b.Grow(len(files) * 72)
	b.WriteString(snapshotMagic)
	var tmp [binary.MaxVarintLen64]byte
	putU := func(v uint64) { b.Write(tmp[:binary.PutUvarint(tmp[:], v)]) }
	putI := func(v int64) { b.Write(tmp[:binary.PutVarint(tmp[:], v)]) }
	putS := func(s string) { putU(uint64(len(s))); b.WriteString(s) }
	putS(gen)
	putU(uint64(len(files)))
	for _, f := range files {
		putS(f.Path)
		putI(f.ID)
		putU(uint64(f.Lang))
		putI(f.Size)
		putI(f.MtimeNS)
		putU(f.Ino)
		putU(uint64(f.Mode))
		putS(string(f.Blob))
		tracked := uint64(0)
		if f.Tracked {
			tracked = 1
		}
		putU(tracked)
		putU(uint64(f.Status))
		putI(int64(f.LOC))
		putI(f.PkgID)
	}
	return b.Bytes()
}

var errBadSnapshot = errors.New("index: malformed files snapshot")

// snapReader decodes the snapshot format without per-field allocations:
// strings are sliced from one copy of the data.
type snapReader struct {
	b   []byte
	s   string // same bytes as b
	off int
	bad bool
}

func (r *snapReader) u() uint64 {
	v, n := binary.Uvarint(r.b[r.off:])
	if n <= 0 {
		r.bad = true
		return 0
	}
	r.off += n
	return v
}

func (r *snapReader) i() int64 {
	v, n := binary.Varint(r.b[r.off:])
	if n <= 0 {
		r.bad = true
		return 0
	}
	r.off += n
	return v
}

func (r *snapReader) str() (string, int) {
	n := r.u()
	if r.bad || n > uint64(len(r.b)-r.off) {
		r.bad = true
		return "", r.off
	}
	start := r.off
	r.off += int(n)
	return r.s[start:r.off], start
}

func decodeSnapshot(data []byte) (gen string, files map[string]*store.File, err error) {
	if !bytes.HasPrefix(data, []byte(snapshotMagic)) {
		return "", nil, errBadSnapshot
	}
	r := &snapReader{b: data, s: string(data), off: len(snapshotMagic)}
	gen, _ = r.str()
	n := r.u()
	if r.bad || n > uint64(len(data)) {
		return "", nil, errBadSnapshot
	}
	files = make(map[string]*store.File, n)
	all := make([]store.File, n)
	for k := range all {
		f := &all[k]
		f.Path, _ = r.str()
		f.ID = r.i()
		f.Lang = model.Lang(uint8(r.u()))
		f.Size = r.i()
		f.MtimeNS = r.i()
		f.Ino = r.u()
		f.Mode = uint32(r.u())
		if blob, at := r.str(); blob != "" {
			f.Blob = data[at : at+len(blob) : at+len(blob)]
		}
		f.Tracked = r.u() == 1
		f.Status = model.FileStatus(uint8(r.u()))
		f.LOC = int(r.i())
		f.PkgID = r.i()
		if r.bad {
			return "", nil, errBadSnapshot
		}
		files[f.Path] = f
	}
	if r.off != len(data) {
		return "", nil, errBadSnapshot
	}
	return gen, files, nil
}

// readSnapshot loads dir/files.snap if it is labelled gen.
func readSnapshot(dir, gen string) (map[string]*store.File, bool) {
	f, err := os.Open(filepath.Join(dir, snapshotName))
	if err != nil {
		return nil, false
	}
	defer f.Close()
	// Cheap label check before reading the whole file.
	br := bufio.NewReader(f)
	head := make([]byte, len(snapshotMagic))
	if _, err := io.ReadFull(br, head); err != nil || string(head) != snapshotMagic {
		return nil, false
	}
	n, err := binary.ReadUvarint(br)
	if err != nil || n != uint64(len(gen)) {
		return nil, false
	}
	label := make([]byte, n)
	if _, err := io.ReadFull(br, label); err != nil || string(label) != gen {
		return nil, false
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, false
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, false
	}
	got, files, err := decodeSnapshot(data)
	if err != nil || got != gen {
		return nil, false
	}
	return files, true
}

// writeSnapshot atomically replaces dir/files.snap. Errors are ignored by
// callers: the snapshot is only an accelerator.
func writeSnapshot(dir, gen string, files map[string]*store.File) error {
	tmp, err := os.CreateTemp(dir, snapshotName+".tmp*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(encodeSnapshot(gen, files))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, snapshotName)); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}
