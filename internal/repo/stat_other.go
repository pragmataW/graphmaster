//go:build !unix

package repo

import "io/fs"

// inode is unavailable without a unix stat; change detection then relies on
// mtime, size and mode only.
func inode(fs.FileInfo) uint64 { return 0 }
