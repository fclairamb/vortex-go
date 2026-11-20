//go:build compat

package vortex

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/testutil"
)

// passedFile lists, one per line, the releases whose files all matched the oracle. `make
// compat` turns it into testdata/compat-releases.txt and the README line.
const passedFile = ".cache/compat-passed.txt"

// TestCompat reads every file of every release fetched by `make fixtures` (.cache/fixtures),
// scans every column and compares each canonical hash with the oracle's. One subtest per
// release and file.
func TestCompat(t *testing.T) {
	dirs, err := filepath.Glob(".cache/fixtures/*")
	if err != nil || len(dirs) == 0 {
		t.Fatal("no .cache/fixtures: run make fixtures")
	}
	sort.Slice(dirs, func(i, j int) bool { return versionLess(filepath.Base(dirs[i]), filepath.Base(dirs[j])) })
	var passed []string
	var files, columns int
	for _, dir := range dirs {
		version := filepath.Base(dir)
		paths := mustGlob(t, dir+"/*.vortex")
		ok := len(paths) > 0
		t.Run(version, func(t *testing.T) {
			for _, path := range paths {
				t.Run(fixtureName(path), func(t *testing.T) {
					if err := checkFixture(path, maxBatchRows, map[string]bool{}); err != nil {
						t.Error(err)
						ok = false
						return
					}
					exp, err := testutil.LoadExpected(path + ".expected.json")
					if err != nil {
						t.Fatal(err)
					}
					files++
					columns += len(exp.Schema.Paths(""))
				})
			}
		})
		if ok {
			passed = append(passed, version)
		}
	}
	t.Logf("%d releases, %d files, %d columns compared", len(passed), files, columns)
	if err := os.WriteFile(passedFile, []byte(strings.Join(passed, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// versionLess orders x.y.z version strings numerically.
func versionLess(a, b string) bool {
	var pa, pb [3]int
	fmt.Sscanf(a, "%d.%d.%d", &pa[0], &pa[1], &pa[2])
	fmt.Sscanf(b, "%d.%d.%d", &pb[0], &pb[1], &pb[2])
	return pa[0] < pb[0] || pa[0] == pb[0] && (pa[1] < pb[1] || pa[1] == pb[1] && pa[2] < pb[2])
}
