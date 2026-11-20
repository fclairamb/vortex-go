package testutil

import (
	"math"
	"strings"
	"testing"
	"time"
)

type dec string

func (d dec) String() string { return string(d) }

func TestCanonicalJSON(t *testing.T) {
	when := time.Date(2026, 11, 20, 1, 2, 3, 500_000_000, time.FixedZone("x", 3600))
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"null", nil, `null`},
		{"true", true, `true`},
		{"false", false, `false`},
		{"int", int64(-42), `-42`},
		{"float integral", 3.0, `3.0`},
		{"float", 0.1, `0.10000000000000001`},
		{"float exponent", 1e300, `1.0000000000000001e+300`},
		{"nan", math.NaN(), `NaN`},
		{"inf", math.Inf(1), `Infinity`},
		{"-inf", math.Inf(-1), `-Infinity`},
		{"string", "a\"b\\c\né", `"a\"b\\c\u000a` + "é" + `"`},
		{"binary", []byte{0, 0xab}, `{"$binary":"00ab"}`},
		{"time", when, `{"$time":"2026-11-20T00:02:03.5Z"}`},
		{"decimal", dec("1.50"), `{"$decimal":"1.50"}`},
		{"list", []any{int64(1), nil, "x"}, `[1,null,"x"]`},
		{"object", map[string]any{"b": int64(1), "a": []any{}}, `{"a":[],"b":1}`},
		{"empty object", map[string]any{}, `{}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CanonicalJSON(c.in)
			if err != nil || got != c.want {
				t.Fatalf("CanonicalJSON = %s, %v, want %s", got, err, c.want)
			}
		})
	}
}

func TestCanonicalJSONErrors(t *testing.T) {
	for name, in := range map[string]any{
		"type":         struct{}{},
		"in a list":    []any{struct{}{}},
		"in an object": map[string]any{"a": struct{}{}},
	} {
		if _, err := CanonicalJSON(in); err == nil || !strings.Contains(err.Error(), "canonical JSON of a") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
