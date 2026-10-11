package store

import (
	"hash/fnv"

	"github.com/pragmataW/graphmaster/internal/model"
)

// SymbolKey returns the 8-byte nodes.skey for a definition: FNV-64a of
// (path, qname, kind). It is stable across reindexing as long as the symbol
// keeps its file, qualified name and kind, so it can be shown to agents as a
// compact hex handle.
func SymbolKey(path, qname string, kind model.NodeKind) []byte {
	h := fnv.New64a()
	_, _ = h.Write([]byte(path))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(qname))
	_, _ = h.Write([]byte{0, byte(kind)})
	return h.Sum(nil)
}
