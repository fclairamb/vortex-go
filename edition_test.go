package vortex

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/fclairamb/vortex-go/internal/edition"
)

// notSeen lists the members of the edition that no file of the corpus holds, each with the
// reason. An entry that is in fact seen makes the test fail: the list can only shrink.
var notSeen = map[string]string{
	key(edition.KindArray, "vortex.variant"):         "vortex-data 0.93.0 cannot write a Variant (extra/MISSING.md); read from hand-built files",
	key(edition.KindArray, "vortex.parquet.variant"): "vortex-data 0.93.0 cannot write a parquet Variant (extra/MISSING.md); read from hand-built files",
}

func key(kind, id string) string { return kind + " " + id }

// decodedSets is what the registries say, by kind.
func decodedSets() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, s := range edition.Supported() {
		if out[s.Kind] == nil {
			out[s.Kind] = map[string]bool{}
		}
		if s.Decoded {
			out[s.Kind][s.ID] = true
		}
	}
	return out
}

// checkComplete returns a message for each member of the edition that has no decoder.
func checkComplete(decoded map[string]map[string]bool) []string {
	var msgs []string
	for _, m := range edition.Missing(decoded) {
		msgs = append(msgs, key(m.Kind, m.ID)+" is in the edition ("+m.Since+") and has no decoder")
	}
	return msgs
}

// checkSeen returns a message for each member never met, unless it is excused, and for each
// excuse that is in fact met.
func checkSeen(seen map[string]string, excused map[string]string) []string {
	var msgs []string
	for _, m := range edition.Members() {
		k := key(m.Kind, m.ID)
		_, met := seen[k]
		reason, excuse := excused[k]
		switch {
		case !met && !excuse:
			msgs = append(msgs, k+" is never met in the corpus: add a fixture that holds it (see testdata/fixtures/<version>/extra/MISSING.md)")
		case met && excuse:
			msgs = append(msgs, k+" is met in "+seen[k]+", remove it from notSeen ("+reason+")")
		}
	}
	return msgs
}

// scanCorpus scans every fixture and records, for each identifier met, one fixture holding it.
func scanCorpus(t *testing.T) map[string]string {
	t.Helper()
	var mu sync.Mutex
	seen := map[string]string{}
	current := ""
	edition.Observe(func(kind, id string) {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := seen[key(kind, id)]; !ok {
			seen[key(kind, id)] = current
		}
	})
	defer edition.Observe(nil)
	for _, path := range corpus(t) {
		current = fixtureName(path)
		f, err := OpenFile(path)
		if err != nil {
			t.Fatalf("%s: %v", current, err)
		}
		for _, err := range f.Scan(context.Background(), Query{}) {
			if err != nil {
				t.Fatalf("%s: %v", current, err)
			}
		}
		f.Close()
	}
	return seen
}

func TestEditionIsComplete(t *testing.T) {
	for _, msg := range checkComplete(decodedSets()) {
		t.Error(msg)
	}
}

func TestEditionIsSeen(t *testing.T) {
	for _, msg := range checkSeen(scanCorpus(t), notSeen) {
		t.Error(msg)
	}
}

func TestCompleteCheckCatchesAMissingDecoder(t *testing.T) {
	decoded := decodedSets()
	delete(decoded[edition.KindArray], "vortex.zstd")
	delete(decoded[edition.KindLayout], "vortex.zoned")
	msgs := checkComplete(decoded)
	if len(msgs) != 2 || !strings.Contains(strings.Join(msgs, "\n"), "array vortex.zstd") {
		t.Fatalf("messages %q", msgs)
	}
}

func TestSeenCheckCatchesAMissingFixture(t *testing.T) {
	seen := map[string]string{}
	for _, m := range edition.Members() {
		seen[key(m.Kind, m.ID)] = "fixture"
	}
	if msgs := checkSeen(seen, nil); len(msgs) != 0 {
		t.Fatalf("a full corpus gave %q", msgs)
	}
	delete(seen, key(edition.KindArray, "vortex.pco"))
	msgs := checkSeen(seen, nil)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "array vortex.pco is never met") {
		t.Fatalf("messages %q", msgs)
	}
	// an excuse covers it, until the identifier is met
	excused := map[string]string{key(edition.KindArray, "vortex.pco"): "no tool writes it"}
	if msgs := checkSeen(seen, excused); len(msgs) != 0 {
		t.Fatalf("an excused member gave %q", msgs)
	}
	seen[key(edition.KindArray, "vortex.pco")] = "pco"
	if msgs := checkSeen(seen, excused); len(msgs) != 1 || !strings.Contains(msgs[0], "remove it from notSeen") {
		t.Fatalf("a met excuse gave %q", msgs)
	}
}

func TestSupportedMatchesRegistries(t *testing.T) {
	var n, written int
	for _, s := range edition.Supported() {
		n++
		if s.Written {
			written++
		}
		if !s.Decoded {
			t.Errorf("%s %s is not decoded", s.Kind, s.ID)
		}
	}
	if written < 20 {
		t.Errorf("only %d members are written", written)
	}
	if n != len(edition.Members()) {
		t.Errorf("%d supports for %d members", n, len(edition.Members()))
	}
}

func TestUnknownEncodingNamesItself(t *testing.T) {
	path := "testdata/fixtures/0.93.0/primitives.vortex"
	f, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.arrayEncodings = slices.Repeat([]string{"acme.unknown"}, len(f.arrayEncodings))
	var got error
	for _, err := range f.Scan(context.Background(), Query{}) {
		if err != nil {
			got = err
			break
		}
	}
	if !errors.Is(got, errors.ErrUnsupported) || !strings.Contains(got.Error(), `"acme.unknown"`) || !strings.Contains(got.Error(), "offset") {
		t.Fatalf("error %v", got)
	}
}

func TestUnknownLayoutNamesItself(t *testing.T) {
	f, err := OpenFile("testdata/fixtures/0.93.0/primitives.vortex")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.layoutEncodings = slices.Repeat([]string{"acme.layout"}, len(f.layoutEncodings))
	var got error
	for _, err := range f.Scan(context.Background(), Query{}) {
		if err != nil {
			got = err
			break
		}
	}
	if !errors.Is(got, errors.ErrUnsupported) || !strings.Contains(got.Error(), `"acme.layout"`) {
		t.Fatalf("error %v", got)
	}
}
