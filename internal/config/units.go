package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Mode is the steering mode.
type Mode string

// Steering modes.
const (
	ModeStrict   Mode = "strict"   // deny every code-scoped grep/glob
	ModeBalanced Mode = "balanced" // deny first symbol-like grep, then allow
	ModeOff      Mode = "off"      // never steer
)

// ParseMode parses a mode name case-insensitively.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case ModeStrict, ModeBalanced, ModeOff:
		return m, nil
	}
	return "", fmt.Errorf("invalid mode %q (want strict, balanced or off)", s)
}

// UnmarshalText implements encoding.TextUnmarshaler with validation.
func (m *Mode) UnmarshalText(b []byte) error {
	v, err := ParseMode(string(b))
	if err != nil {
		return err
	}
	*m = v
	return nil
}

// Duration is a time.Duration that (un)marshals as a Go duration string
// such as "1.5s" or "750ms".
type Duration time.Duration

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(strings.TrimSpace(string(b)))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", b, err)
	}
	*d = Duration(v)
	return nil
}

// ByteSize is a size in bytes. In TOML it is either an integer or a string
// with a unit: B, KB/MB/GB/TB (powers of 1000) or KiB/MiB/GiB/TiB (powers
// of 1024), case-insensitive.
type ByteSize int64

var byteUnits = []struct {
	suffix string
	mult   int64
}{
	// Longest suffixes first so "kib" wins over "b".
	{"kib", 1 << 10}, {"mib", 1 << 20}, {"gib", 1 << 30}, {"tib", 1 << 40},
	{"kb", 1e3}, {"mb", 1e6}, {"gb", 1e9}, {"tb", 1e12},
	{"b", 1},
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (b *ByteSize) UnmarshalText(text []byte) error {
	s := strings.ToLower(strings.TrimSpace(string(text)))
	mult := int64(1)
	for _, u := range byteUnits {
		if strings.HasSuffix(s, u.suffix) {
			s, mult = strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), u.mult
			break
		}
	}
	n, err := strconv.ParseUint(s, 10, 63)
	if err != nil || (mult > 1 && int64(n) > (1<<63-1)/mult) {
		return fmt.Errorf("invalid size %q", text)
	}
	*b = ByteSize(int64(n) * mult)
	return nil
}

// MarshalText renders exact binary multiples with a unit, else plain bytes.
func (b ByteSize) MarshalText() ([]byte, error) {
	v := int64(b)
	for _, u := range []struct {
		name string
		mult int64
	}{{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}} {
		if v != 0 && v%u.mult == 0 {
			return []byte(strconv.FormatInt(v/u.mult, 10) + u.name), nil
		}
	}
	return []byte(strconv.FormatInt(v, 10)), nil
}
