package vortex

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/testutil"
)

// checkFixtures returns what is wrong with the expected values of every *.vortex under root:
// no .expected.json, one that does not parse, or one that lacks a column or a hash.
func checkFixtures(root string) ([]string, error) {
	var problems []string
	found := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".vortex") {
			return err
		}
		found++
		exp, err := testutil.LoadExpected(path + ".expected.json")
		if err == nil {
			err = exp.Check()
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
		}
		return nil
	})
	if err == nil && found == 0 {
		problems = append(problems, "no *.vortex under "+root)
	}
	return problems, err
}

func TestFixturesHaveExpectedValues(t *testing.T) {
	problems, err := checkFixtures("testdata/fixtures")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p + "; run `make oracle`")
	}
}

func TestCheckFixturesFailures(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if problems, _ := checkFixtures(dir); len(problems) != 1 || !strings.Contains(problems[0], "no *.vortex") {
		t.Fatalf("an empty folder must be reported, got %v", problems)
	}
	write("nojson.vortex", "x")
	write("lacks.vortex", "x")
	write("lacks.vortex.expected.json", `{"rows": 1, "columns": {}, "encodings": [],
		"schema": {"kind": "struct", "nullable": false, "fields": [
		{"name": "a", "dtype": {"kind": "bool", "nullable": false}}]}}`)
	write("ok.vortex", "x")
	write("ok.vortex.expected.json", `{"rows": 0, "columns": {}, "encodings": [],
		"schema": {"kind": "struct", "nullable": false}}`)
	problems, err := checkFixtures(dir)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(problems, "\n")
	if len(problems) != 2 || !strings.Contains(joined, "nojson.vortex") ||
		!strings.Contains(joined, `lacks.vortex`) || !strings.Contains(joined, `"a" has no expected`) {
		t.Fatalf("problems = %q", problems)
	}
}

func TestCheckFixturesWalkError(t *testing.T) {
	if _, err := checkFixtures(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing root must fail")
	}
}
