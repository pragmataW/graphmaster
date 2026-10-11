package repo

import (
	"io/fs"
	"os"
	"time"
)

// Git file modes as stored in the index and in tree objects.
const (
	ModeRegular    uint32 = 0o100644
	ModeExecutable uint32 = 0o100755
	ModeSymlink    uint32 = 0o120000
	ModeGitlink    uint32 = 0o160000
	ModeDir        uint32 = 0o040000
)

// RacyWindow is the safety margin of the racy-git rule: a file whose mtime
// is within this window of the scan that recorded its stat may have been
// modified again without a visible mtime change, so it is never trusted.
const RacyWindow = 2 * time.Second

// FileStat is the stat signature used for change detection. Mode is a git
// mode (ModeRegular, ModeExecutable, ModeSymlink, ...), not an os.FileMode.
type FileStat struct {
	MtimeNS int64
	Size    int64
	Ino     uint64
	Mode    uint32
}

// Lstat returns the FileStat of path without following symlinks.
func Lstat(path string) (FileStat, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return FileStat{}, err
	}
	return StatFromInfo(fi), nil
}

// StatFromInfo converts an fs.FileInfo obtained via Lstat.
func StatFromInfo(fi fs.FileInfo) FileStat {
	return FileStat{
		MtimeNS: fi.ModTime().UnixNano(),
		Size:    fi.Size(),
		Ino:     inode(fi),
		Mode:    GitMode(fi.Mode()),
	}
}

// GitMode maps an os.FileMode to the git mode git would record for it.
// Unsupported types (devices, sockets, ...) map to 0.
func GitMode(m fs.FileMode) uint32 {
	switch {
	case m.IsRegular():
		if m&0o111 != 0 {
			return ModeExecutable
		}
		return ModeRegular
	case m&fs.ModeSymlink != 0:
		return ModeSymlink
	case m.IsDir():
		return ModeDir
	default:
		return 0
	}
}

// IsClean implements the racy-git rule: a file is clean only if its current
// stat equals the stored one AND the stored mtime is older than
// scanStart-RacyWindow, where scanStart is when the stored stat was taken.
func IsClean(stored, current FileStat, scanStart time.Time) bool {
	return stored == current && !IsRacy(stored.MtimeNS, scanStart)
}

// IsRacy reports whether mtimeNS is too close to (or after) ref for a stat
// comparison to prove the content unchanged.
func IsRacy(mtimeNS int64, ref time.Time) bool {
	return mtimeNS >= ref.Add(-RacyWindow).UnixNano()
}
