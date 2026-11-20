package testutil

import (
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// CanonicalJSON renders a Variant value as the canonical JSON of docs/testing.md, which the
// hash of a Variant column is taken over. The value is what Column.Variant returns: nil, bool,
// int64, float64, string, []byte, time.Time, a decimal (a fmt.Stringer), []any or
// map[string]any.
//
// There is no space, the keys of an object are sorted by their bytes, an integer is its
// digits and a float its 17 significant digits ("%.17g", with ".0" added when that looks like
// an integer; NaN, Infinity and -Infinity are bare). A string escapes only the quote, the
// backslash and the control characters (\u00XX). A binary is {"$binary":"<hex>"}, a time is
// {"$time":"<UTC, RFC 3339, as many fraction digits as needed>"} and a decimal is
// {"$decimal":"<text>"}.
func CanonicalJSON(v any) (string, error) {
	var b strings.Builder
	if err := writeJSON(&b, v); err != nil {
		return "", err
	}
	return b.String(), nil
}

func writeJSON(b *strings.Builder, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		b.WriteString(floatJSON(x))
	case string:
		quote(b, x)
	case []byte:
		b.WriteString(`{"$binary":"` + hex.EncodeToString(x) + `"}`)
	case time.Time:
		b.WriteString(`{"$time":"` + x.UTC().Format("2006-01-02T15:04:05.999999999Z") + `"}`)
	case fmt.Stringer:
		b.WriteString(`{"$decimal":"` + x.String() + `"}`)
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeJSON(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		return writeObject(b, x)
	default:
		return fmt.Errorf("canonical JSON of a %T", v)
	}
	return nil
}

func writeObject(b *strings.Builder, m map[string]any) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		quote(b, k)
		b.WriteByte(':')
		if err := writeJSON(b, m[k]); err != nil {
			return err
		}
	}
	b.WriteByte('}')
	return nil
}

func floatJSON(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	s := strconv.FormatFloat(f, 'g', 17, 64)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}

func quote(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := range len(s) {
		switch c := s[i]; {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 0x20:
			fmt.Fprintf(b, `\u%04x`, c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
}
