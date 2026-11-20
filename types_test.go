package vortex

import (
	"fmt"
	"testing"
)

// where a type is met in the corpus.
const (
	whereAlone    = "alone"
	whereInStruct = "in a struct"
	whereInList   = "in a list"
)

// typeSeen records, in seen, each (kind, nullability, place) met in t. A column of the root
// struct is alone; the fields of a nested struct are in a struct; the elements of a list,
// a fixed-size list or a map are in a list.
func typeSeen(seen map[string]bool, t Type, where string) {
	seen[fmt.Sprintf("%v/%v/%s", t.Kind(), t.Nullable(), where)] = true
	switch t.Kind() {
	case Struct:
		for _, f := range t.Fields() {
			typeSeen(seen, f.Type, whereInStruct)
		}
	case List, FixedSizeList:
		typeSeen(seen, t.Elem(), whereInList)
	case Map:
		typeSeen(seen, t.Key(), whereInList)
		typeSeen(seen, t.Value(), whereInList)
	}
}

// typeGaps lists the (kind, nullability, place) of every type of docs/interface.md that is
// not in seen. Null is always nullable.
func typeGaps(seen map[string]bool) []string {
	var gaps []string
	for k := Null; k <= Variant; k++ {
		for _, nullable := range []bool{false, true} {
			if k == Null && !nullable {
				continue
			}
			for _, where := range []string{whereAlone, whereInStruct, whereInList} {
				key := fmt.Sprintf("%v/%v/%s", k, nullable, where)
				if !seen[key] && typeExcused[key] == "" {
					gaps = append(gaps, key)
				}
			}
		}
	}
	return gaps
}

// typeExcused are the combinations that no file holds, each with the reason.
var typeExcused = func() map[string]string {
	out := map[string]string{}
	for _, nullable := range []bool{false, true} {
		for _, where := range []string{whereAlone, whereInStruct, whereInList} {
			out[fmt.Sprintf("%v/%v/%s", Variant, nullable, where)] = "vortex-data 0.93.0 cannot write a Variant; column_variant_test.go reads hand-built files"
		}
	}
	return out
}()

func TestEveryLogicalTypeIsInTheCorpus(t *testing.T) {
	seen := map[string]bool{}
	for _, path := range corpus(t) {
		f, err := OpenFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, fld := range f.Schema().Fields() {
			typeSeen(seen, fld.Type, whereAlone)
		}
		f.Close()
	}
	for _, g := range typeGaps(seen) {
		t.Errorf("no fixture holds %s", g)
	}
	for key, why := range typeExcused {
		if seen[key] {
			t.Errorf("%s is in a fixture, remove it from typeExcused (%s)", key, why)
		}
	}
}

func TestTypeGapsReportsAMissingType(t *testing.T) {
	if gaps := typeGaps(map[string]bool{}); len(gaps) < 70 {
		t.Fatalf("an empty corpus has %d gaps", len(gaps))
	}
}
