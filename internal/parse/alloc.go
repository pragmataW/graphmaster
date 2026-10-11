package parse

/*
#include <stdlib.h>

// Provided by the tree-sitter core compiled into go-tree-sitter.
extern void ts_set_allocator(
	void *(*new_malloc)(size_t),
	void *(*new_calloc)(size_t, size_t),
	void *(*new_realloc)(void *, size_t),
	void (*new_free)(void *));

static void gm_use_libc_allocator(void) {
	ts_set_allocator(malloc, calloc, realloc, free);
}
*/
import "C"

// go-tree-sitter v0.25 routes every tree-sitter allocation through exported
// Go functions (C -> Go -> C), which costs a cgo callback per malloc/free and
// roughly halves parse throughput. Point the core straight at libc instead.
// Memory allocated earlier went through the same libc malloc, so frees stay
// compatible; go-tree-sitter's own Go-side frees also end in C.free.
func init() { C.gm_use_libc_allocator() }
