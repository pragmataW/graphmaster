package server_test

import (
	"testing"

	"example.com/app/server"
)

func TestNewServer(t *testing.T) {
	s := server.NewServer(":0")
	if s == nil {
		t.Fatal("nil server")
	}
}

func TestServer_Start(t *testing.T) {
	t.Helper()
}

func BenchmarkMap(b *testing.B) {
	for b.Loop() {
		server.Map([]int{1}, func(i int) int { return i })
	}
}

func FuzzParse(f *testing.F) {
	f.Fuzz(func(t *testing.T, s string) {})
}

// TestMain is not a test: it takes *testing.M.
func TestMain(m *testing.M) { m.Run() }

// Testify is not a test: lowercase after the prefix.
func Testify(t *testing.T) {}

// helperT is not a test.
func helperT(t *testing.T) {}
