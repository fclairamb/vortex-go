package vortex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/edition"
)

type manifest struct {
	Fixtures []struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
	} `json:"fixtures"`
}

func isDataset(name string) bool {
	return strings.HasPrefix(name, "tpch_") || strings.HasPrefix(name, "clickbench_")
}

// checkUpstream returns what is wrong with the fixtures folder and the edition table for the
// pinned version, or "" when all is in place.
func checkUpstream(version, editionVersion, fixturesDir string) string {
	const hint = "; run `make upstream`"
	raw, err := os.ReadFile(filepath.Join(fixturesDir, "manifest.json"))
	if err != nil {
		return "fixtures of " + version + " missing: " + err.Error() + hint
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return "manifest.json of " + version + " is invalid: " + err.Error() + hint
	}
	for _, f := range m.Fixtures {
		if isDataset(f.Name) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(fixturesDir, f.Name))
		if err != nil {
			return "fixture " + f.Name + " missing: " + err.Error() + hint
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			return "fixture " + f.Name + " does not match its SHA-256" + hint
		}
	}
	if editionVersion != version {
		return "edition table is for " + editionVersion + ", .vortex-version is " + version + hint
	}
	return ""
}

func pinnedVersion(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(".vortex-version")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

func TestUpstreamInPlace(t *testing.T) {
	version := pinnedVersion(t)
	if msg := checkUpstream(version, edition.Version, filepath.Join("testdata", "fixtures", version)); msg != "" {
		t.Fatal(msg)
	}
}

func TestCheckUpstreamFailures(t *testing.T) {
	version := pinnedVersion(t)
	src := filepath.Join("testdata", "fixtures", version)

	copyDir := func(t *testing.T) string {
		t.Helper()
		dst := t.TempDir()
		entries, err := os.ReadDir(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(src, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dst
	}
	firstFixture := func(t *testing.T) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(src, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		for _, f := range m.Fixtures {
			if !isDataset(f.Name) {
				return f.Name
			}
		}
		t.Fatal("no synthetic fixture")
		return ""
	}

	t.Run("no folder", func(t *testing.T) {
		expectFailure(t, checkUpstream(version, version, filepath.Join(t.TempDir(), "none")))
	})
	t.Run("invalid manifest", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		expectFailure(t, checkUpstream(version, version, dir))
	})
	t.Run("file missing", func(t *testing.T) {
		dir := copyDir(t)
		if err := os.Remove(filepath.Join(dir, firstFixture(t))); err != nil {
			t.Fatal(err)
		}
		expectFailure(t, checkUpstream(version, version, dir))
	})
	t.Run("flipped byte", func(t *testing.T) {
		dir := copyDir(t)
		path := filepath.Join(dir, firstFixture(t))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data[0] ^= 0xff
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		expectFailure(t, checkUpstream(version, version, dir))
	})
	t.Run("version differs", func(t *testing.T) {
		expectFailure(t, checkUpstream("0.94.0", version, src))
	})
}

func expectFailure(t *testing.T, msg string) {
	t.Helper()
	if !strings.Contains(msg, "make upstream") {
		t.Fatalf("message %q does not name `make upstream`", msg)
	}
}

// cargoPin returns the version the vortex crate is pinned to in a Cargo.toml: the "=x.y.z" of
// its `vortex = { version = "=x.y.z", ... }` line.
func cargoPin(cargo string) (string, bool) {
	m := regexp.MustCompile(`(?m)^vortex\s*=\s*\{[^}]*version\s*=\s*"=(\d+\.\d+\.\d+)"`).FindStringSubmatch(cargo)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// The Rust benchmark harness is pinned to the release of .vortex-version: Renovate moves both
// in one PR, and this fails when only one moved.
func TestBenchRustPinFollowsVortexVersion(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("tools", "bench-rust", "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	pin, ok := cargoPin(string(raw))
	if !ok {
		t.Fatal("tools/bench-rust/Cargo.toml has no `vortex = { version = \"=x.y.z\" }` line")
	}
	if version := pinnedVersion(t); pin != version {
		t.Fatalf("tools/bench-rust/Cargo.toml pins vortex %s, .vortex-version is %s: change both", pin, version)
	}
}

// The Variant reader of the interop tests is pinned to the same release, for the vortex crate and
// for vortex-parquet-variant.
func TestVariantJSONPinFollowsVortexVersion(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("tools", "variantjson", "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	version := pinnedVersion(t)
	pin, ok := cargoPin(string(raw))
	if !ok {
		t.Fatal("tools/variantjson/Cargo.toml has no `vortex = { version = \"=x.y.z\" }` line")
	}
	if pin != version {
		t.Fatalf("tools/variantjson/Cargo.toml pins vortex %s, .vortex-version is %s: change both", pin, version)
	}
	m := regexp.MustCompile(`(?m)^vortex-parquet-variant\s*=\s*"=(\d+\.\d+\.\d+)"`).FindStringSubmatch(string(raw))
	if m == nil || m[1] != version {
		t.Fatalf("tools/variantjson/Cargo.toml: vortex-parquet-variant is %v, .vortex-version is %s: change both", m, version)
	}
}

func TestCargoPin(t *testing.T) {
	if v, ok := cargoPin("[dependencies]\nvortex = { version = \"=1.2.3\", features = [\"tokio\"] }\n"); !ok || v != "1.2.3" {
		t.Errorf("got %q %v", v, ok)
	}
	for _, bad := range []string{"", "vortex = \"1.2.3\"\n", "vortex = { version = \"1.2.3\" }\n", "vortex-fastlanes = { version = \"=1.2.3\" }\n"} {
		if v, ok := cargoPin(bad); ok {
			t.Errorf("%q: pin %q found", bad, v)
		}
	}
}
