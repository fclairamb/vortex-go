package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validJSON = `{
  "columns": {
    "a": {"first": ["1"], "last": ["1"], "nulls": 0, "sha256": "00"},
    "s": {"first": [], "last": [], "nulls": 0, "sha256": "01"},
    "s.x": {"first": [], "last": [], "nulls": 0, "sha256": "02"},
    "v": {"first": [], "last": [], "nulls": 0, "sha256": "03"}
  },
  "encodings": ["vortex.primitive"],
  "rows": 1,
  "schema": {"kind": "struct", "nullable": false, "fields": [
    {"name": "a", "dtype": {"kind": "primitive", "nullable": false, "ptype": "i32"}},
    {"name": "s", "dtype": {"kind": "struct", "nullable": true, "fields": [
      {"name": "x", "dtype": {"kind": "bool", "nullable": false}}]}},
    {"name": "v", "dtype": {"kind": "extension", "nullable": false, "ext_id": "vortex.variant",
      "ext_metadata": "", "storage": {"kind": "binary", "nullable": false}}}
  ]}
}`

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.vortex.expected.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadExpectedValid(t *testing.T) {
	e, err := LoadExpected(write(t, validJSON))
	if err != nil {
		t.Fatal(err)
	}
	if e.Rows != 1 || len(e.Columns) != 4 || *e.Columns["a"].SHA256 != "00" {
		t.Fatalf("unexpected content: %+v", e)
	}
	if err := e.Check(); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(e.Schema.Paths(""), ",")
	if got != "a,s,s.x,v" {
		t.Fatalf("paths = %s", got)
	}
}

func TestLoadExpectedTruncated(t *testing.T) {
	if _, err := LoadExpected(write(t, validJSON[:len(validJSON)/2])); err == nil {
		t.Fatal("a truncated file must fail")
	}
}

func TestLoadExpectedMissingAndUnknownKey(t *testing.T) {
	if _, err := LoadExpected(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("a missing file must fail")
	}
	if _, err := LoadExpected(write(t, `{"rows": 1, "surprise": true}`)); err == nil {
		t.Fatal("an unknown key must fail")
	}
}

func TestCheckFailures(t *testing.T) {
	load := func(content string) *Expected {
		t.Helper()
		e, err := LoadExpected(write(t, content))
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	cases := map[string]struct{ from, to, want string }{
		"not a struct":    {`"schema": {"kind": "struct"`, `"schema": {"kind": "bool"`, "want a struct"},
		"negative":        {`"rows": 1`, `"rows": -1`, "rows is -1"},
		"no column":       {`"a": {"first": ["1"], "last": ["1"], "nulls": 0, "sha256": "00"},`, ``, `"a" has no expected`},
		"no nested":       {`"s.x": {"first": [], "last": [], "nulls": 0, "sha256": "02"},`, ``, `"s.x" has no expected`},
		"no hash":         {`"sha256": "01"`, `"sha256": null`, `"s" has no hash`},
		"no variant hash": {`"sha256": "03"`, `"sha256": null`, `"v" has no hash`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(validJSON, c.from) {
				t.Fatalf("test case does not match the JSON: %s", c.from)
			}
			err := load(strings.Replace(validJSON, c.from, c.to, 1)).Check()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Check() = %v, want %q", err, c.want)
			}
		})
	}
}
