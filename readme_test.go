package vortex

import (
	"flag"
	"fmt"
	"os"
	"regexp"
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

// badgeRe matches a badge line: [![alt](image)](link).
var badgeRe = regexp.MustCompile(`^\[!\[([^\]]*)\]\(([^)]+)\)\]\(([^)]+)\)$`)

// TestReadmeBadges checks the badge block under the title: every badge names the repository,
// the pkg.go.dev and Go Report Card badges use the module path of go.mod, the build badge
// points at an existing workflow, LICENSE exists, and no badge URL carries a token.
func TestReadmeBadges(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	gomod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	modulePath := ""
	for _, line := range strings.Split(string(gomod), "\n") {
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			modulePath = strings.TrimSpace(rest)
		}
	}
	if modulePath == "" {
		t.Fatal("no module line in go.mod")
	}
	if modulePath != "github.com/fclairamb/vortex-go" {
		t.Fatalf("module path %q", modulePath)
	}

	var badges []string
	for _, line := range strings.Split(string(readme), "\n") {
		if strings.HasPrefix(line, "[![") {
			badges = append(badges, line)
		}
	}
	if len(badges) != 6 {
		t.Fatalf("%d badges, want 6", len(badges))
	}
	wantLabels := []string{"Build", "Coverage", "Go Reference", "Go Report Card", "Go", "License"}
	for i, line := range badges {
		m := badgeRe.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("badge %d is not [![alt](image)](link): %q", i, line)
		}
		if m[1] != wantLabels[i] {
			t.Errorf("badge %d label %q, want %q", i, m[1], wantLabels[i])
		}
		if strings.Contains(m[2], "token") || strings.Contains(m[3], "token") {
			t.Errorf("badge %d carries a token: %q", i, line)
		}
		if !strings.Contains(m[2]+m[3], "fclairamb/vortex-go") {
			t.Errorf("badge %d does not name fclairamb/vortex-go: %q", i, line)
		}
	}
	if !strings.Contains(badges[2], "pkg.go.dev/badge/"+modulePath+".svg") ||
		!strings.Contains(badges[2], "pkg.go.dev/"+modulePath+")") {
		t.Errorf("Go Reference badge is not for %s: %q", modulePath, badges[2])
	}
	if !strings.Contains(badges[3], "goreportcard.com/badge/"+modulePath+")") {
		t.Errorf("Go Report Card badge is not for %s: %q", modulePath, badges[3])
	}
	if m := badgeRe.FindStringSubmatch(badges[0]); m == nil || !strings.HasSuffix(m[3], "/workflows/build.yml") {
		t.Errorf("Build badge does not link to build.yml: %q", badges[0])
	}
	if _, err := os.Stat(".github/workflows/build.yml"); err != nil {
		t.Errorf("build workflow: %v", err)
	}
	if _, err := os.Stat("LICENSE"); err != nil {
		t.Errorf("LICENSE: %v", err)
	}
}
