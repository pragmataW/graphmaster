package repo

import (
	"crypto/sha1" //nolint:gosec // git object ids are SHA-1 by definition
	"fmt"
	"io"
	"os"
	"strconv"
)

// BlobID is a git object id (SHA-1).
type BlobID [20]byte

// BlobSHA1 returns the git blob id of content: sha1("blob <n>\x00" + content).
func BlobSHA1(content []byte) BlobID {
	h := sha1.New() //nolint:gosec
	writeBlobHeader(h, int64(len(content)))
	h.Write(content)
	var id BlobID
	h.Sum(id[:0])
	return id
}

// BlobSHA1Reader hashes exactly size bytes from r as a git blob.
func BlobSHA1Reader(r io.Reader, size int64) (BlobID, error) {
	h := sha1.New() //nolint:gosec
	writeBlobHeader(h, size)
	n, err := io.Copy(h, io.LimitReader(r, size))
	if err != nil {
		return BlobID{}, err
	}
	if n != size {
		return BlobID{}, fmt.Errorf("blob hash: short read %d of %d bytes", n, size)
	}
	var id BlobID
	h.Sum(id[:0])
	return id, nil
}

// HashPath returns the git blob id of the file at path together with the
// stat taken before reading. Symlinks hash their target string, as git does.
// No clean/smudge filters or EOL conversion are applied.
func HashPath(path string) (BlobID, FileStat, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return BlobID{}, FileStat{}, err
	}
	st := StatFromInfo(fi)
	switch st.Mode {
	case ModeSymlink:
		target, err := os.Readlink(path)
		if err != nil {
			return BlobID{}, st, err
		}
		return BlobSHA1([]byte(target)), st, nil
	case ModeRegular, ModeExecutable:
		f, err := os.Open(path)
		if err != nil {
			return BlobID{}, st, err
		}
		defer f.Close()
		id, err := BlobSHA1Reader(f, st.Size)
		return id, st, err
	default:
		return BlobID{}, st, fmt.Errorf("hash %s: not a regular file or symlink", path)
	}
}

func writeBlobHeader(w io.Writer, size int64) {
	var buf [32]byte
	b := append(buf[:0], "blob "...)
	b = strconv.AppendInt(b, size, 10)
	b = append(b, 0)
	_, _ = w.Write(b)
}
