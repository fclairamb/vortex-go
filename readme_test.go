package vortex

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
)

var updateReadme = flag.Bool("update-readme", false, "rewrite the compat line of README.md")

const (
	compatBegin = "<!-- compat:begin -->"
	compatEnd   = "<!-- compat:end -->"
)

// compatLine is the README line for the releases of testdata/compat-releases.txt, one per
// line, oldest first: the list that `make compat` read without a difference.
func compatLine(releases string) string {
	list := strings.Fields(releases)
	if len(list) == 0 {
		return "No published release has been checked yet."
	}
	return fmt.Sprintf("Reads the fixtures of every published release, %d of them, from %s to %s, checked every night against the reference reader.",
		len(list), list[0], list[len(list)-1])
}

// withCompatLine returns readme with the text between the compat markers replaced by line.
func withCompatLine(readme, line string) (string, error) {
	before, rest, ok := strings.Cut(readme, compatBegin)
	if !ok {
		return "", fmt.Errorf("no %s marker", compatBegin)
	}
	_, after, ok := strings.Cut(rest, compatEnd)
	if !ok {
		return "", fmt.Errorf("no %s marker", compatEnd)
	}
	return before + compatBegin + "\n" + line + "\n" + compatEnd + after, nil
}

// TestReadmeCompat checks that the README line is the one the list of releases gives. `go
// generate` rewrites it, CI fails when it is stale.
func TestReadmeCompat(t *testing.T) {
	releases, err := os.ReadFile("testdata/compat-releases.txt")
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	want, err := withCompatLine(string(readme), compatLine(string(releases)))
	if err != nil {
		t.Fatal(err)
	}
	if want == string(readme) {
		return
	}
	if !*updateReadme {
		t.Fatal("README.md is stale: run go generate ./...")
	}
	if err := os.WriteFile("README.md", []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCompatLine(t *testing.T) {
	if got := compatLine("0.64.0\n0.68.0\n0.93.0\n"); !strings.Contains(got, "3 of them, from 0.64.0 to 0.93.0") {
		t.Fatalf("line %q", got)
	}
	if got := compatLine(""); !strings.Contains(got, "No published release") {
		t.Fatalf("line %q", got)
	}
	for _, readme := range []string{"nothing", compatBegin + " nothing"} {
		if _, err := withCompatLine(readme, "x"); err == nil {
			t.Fatalf("%q: no error", readme)
		}
	}
	got, err := withCompatLine("a\n"+compatBegin+"\nold\n"+compatEnd+"\nb\n", "new")
	if err != nil || got != "a\n"+compatBegin+"\nnew\n"+compatEnd+"\nb\n" {
		t.Fatalf("got %q, %v", got, err)
	}
}
