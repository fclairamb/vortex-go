package vortex

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
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
	path := "testdata/fixtures/0.94.0/primitives.vortex"
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
	f, err := OpenFile("testdata/fixtures/0.94.0/primitives.vortex")
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

// checkWritten compares the identifiers met in files (identifier of a kind to the file that holds
// it) with the ones registered as written, for one kind.
func checkWritten(kind string, met map[string]string, registered map[string]bool) []string {
	var msgs []string
	for id, file := range met {
		if !registered[id] {
			msgs = append(msgs, kind+" "+id+" is written (in "+file+") and not registered as written")
		}
	}
	for id := range registered {
		if _, ok := met[id]; !ok {
			msgs = append(msgs, kind+" "+id+" is registered as written and no file holds it")
		}
	}
	slices.Sort(msgs)
	return msgs
}

// writtenFiles writes the files of the writer tests and returns, by kind, the identifiers they
// hold with the first file that holds each. A file does not list its aggregates or its extension
// types, so the kinds aggregate and dtype are not checked.
func writtenFiles(t *testing.T) map[string]map[string]string {
	t.Helper()
	met := map[string]map[string]string{edition.KindArray: {}, edition.KindLayout: {}}
	collect := func(name string, f *File) {
		for _, id := range f.arrayEncodings {
			if _, ok := met[edition.KindArray][id]; !ok {
				met[edition.KindArray][id] = name
			}
		}
		for _, id := range f.layoutEncodings {
			if _, ok := met[edition.KindLayout][id]; !ok {
				met[edition.KindLayout][id] = name
			}
		}
	}
	types := allTypes()
	names := make([]string, 0, len(types))
	for n := range types {
		names = append(names, n)
	}
	slices.Sort(names)
	for mode, c := range map[string]Compression{"default": Default, "compact": Compact, "none": None} {
		g := &gen{r: rand.New(rand.NewPCG(7, 8))}
		var fields []Field
		var cols []Data
		for _, n := range names {
			d, _ := g.make(types[n], 300)
			fields = append(fields, Field{n, types[n]})
			cols = append(cols, d)
		}
		var buf bytes.Buffer
		w := NewWriter(&buf, NewSchema(fields...), WriteOptions{Compression: c})
		if err := w.Write(cols...); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		f, err := Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err != nil {
			t.Fatal(err)
		}
		collect("all types, compression "+mode, f)
	}
	// the files of the older editions: the zone maps of vortex.stats, the lists of vortex.list
	for _, ed := range []string{"core 2025.05.0"} {
		g := &gen{r: rand.New(rand.NewPCG(7, 8))}
		var fields []Field
		var cols []Data
		for _, n := range names {
			if validEdition(types[n], "", ed) != nil {
				continue
			}
			d, _ := g.make(types[n], 300)
			fields = append(fields, Field{n, types[n]})
			cols = append(cols, d)
		}
		var buf bytes.Buffer
		w := NewWriter(&buf, NewSchema(fields...), WriteOptions{Edition: ed})
		if err := w.Write(cols...); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		f, err := Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err != nil {
			t.Fatal(err)
		}
		collect("all types, edition "+ed, f)
	}
	// a Variant column, with and without nulls
	for mode, c := range map[string]Compression{"default": Default, "none": None} {
		vals := variantRows(300, 5)
		var buf bytes.Buffer
		w := NewWriter(&buf, NewSchema(Field{"v", VariantType().AsNullable()}), WriteOptions{Compression: c})
		if err := w.Write(OfVariants(vals)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		collect("variants, compression "+mode, openBytes(t, buf.Bytes()))
	}
	// a column of of about 1 MiB is cut into chunks, in a chunked layout
	rows := make([]int32, 600000)
	for i := range rows {
		rows[i] = int32(i * 7919 % 10007)
	}
	chunked, _ := writeFile(t, NewSchema(Field{"c", Int32Type()}), []Data{Of(rows)})
	collect("600,000 rows", chunked)
	for enc := range encodingIDs {
		var ty Type
		var d Data
		switch enc {
		case "fsst", "onpair", "zstd", "dict", "varbin":
			ty = Utf8Type()
			strs := make([]string, 200)
			for i := range strs {
				strs[i] = []string{"a", "abcdefghijkl", "a string that is longer than twelve bytes"}[i%3]
			}
			d = OfStrings(strs)
			if enc == "varbin" {
				bs := make([][]byte, len(strs))
				for i, s := range strs {
					bs[i] = []byte(s)
				}
				ty, d = BinaryType(), OfBytes(bs)
			}
		case "decimal_byte_parts":
			ty = DecimalOf(5, 2)
			d, _ = (&gen{r: rand.New(rand.NewPCG(1, 1))}).make(ty, 200)
		case "datetimeparts":
			ty = TimestampType(Microsecond, "")
			d, _ = (&gen{r: rand.New(rand.NewPCG(1, 1))}).make(ty, 200)
		case "bytebool":
			ty = BoolType()
			d = OfBools([]bool{true, false, true})
		default:
			ks := forcedTypes(enc)
			ty, d, _ = forcedColumn(enc, ks[len(ks)-1], 100, enc == "masked")
		}
		f, err := writeForced(t, enc, NewSchema(Field{"c", ty}), []Data{d})
		if err != nil {
			t.Fatalf("forced %s: %v", enc, err)
		}
		collect("forced encoding "+enc, f)
	}
	return met
}

func TestWrittenMatchesFiles(t *testing.T) {
	registered := map[string]map[string]bool{edition.KindArray: {}, edition.KindLayout: {}}
	for _, s := range edition.Supported() {
		if s.Written && registered[s.Kind] != nil {
			registered[s.Kind][s.ID] = true
		}
	}
	for kind, met := range writtenFiles(t) {
		for _, msg := range checkWritten(kind, met, registered[kind]) {
			t.Error(msg)
		}
	}
}

func TestWrittenCheckCatchesADrift(t *testing.T) {
	met := map[string]string{"vortex.map": "f.vortex", "vortex.bool": "g.vortex"}
	if msgs := checkWritten("array", met, map[string]bool{"vortex.map": true, "vortex.bool": true}); len(msgs) != 0 {
		t.Fatalf("a match gave %q", msgs)
	}
	msgs := checkWritten("array", met, map[string]bool{"vortex.bool": true})
	if len(msgs) != 1 || !strings.Contains(msgs[0], "vortex.map") || !strings.Contains(msgs[0], "f.vortex") {
		t.Fatalf("an unregistered identifier gave %q", msgs)
	}
	msgs = checkWritten("array", met, map[string]bool{"vortex.map": true, "vortex.bool": true, "vortex.pco": true})
	if len(msgs) != 1 || !strings.Contains(msgs[0], "vortex.pco") {
		t.Fatalf("a missing identifier gave %q", msgs)
	}
}
