package edition

import "testing"

func TestHas(t *testing.T) {
	cases := []struct {
		kind, id string
		want     bool
	}{
		{KindArray, "fastlanes.bitpacked", true},
		{KindArray, "fastlanes.delta", false},
		{KindLayout, "vortex.zoned", true},
		{KindDType, "vortex.uuid", true},
		{KindAggregate, "vortex.min", true},
		{KindAggregate, "vortex.zoned", false},
	}
	for _, c := range cases {
		if got := Has(c.kind, c.id); got != c.want {
			t.Errorf("Has(%q, %q) = %v, want %v", c.kind, c.id, got, c.want)
		}
	}
}

func TestCountPerKind(t *testing.T) {
	want := map[string]int{KindArray: 34, KindLayout: 6, KindDType: 4, KindAggregate: 6}
	got := map[string]int{}
	for _, m := range Members() {
		got[m.Kind]++
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s: %d members, want %d", k, got[k], n)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected kinds: %v", got)
	}
}

func TestMembersIsACopy(t *testing.T) {
	m := Members()
	m[0].ID = "changed"
	if Members()[0].ID == "changed" {
		t.Fatal("Members returned the table itself")
	}
}

func TestEdition(t *testing.T) {
	if Version != "0.93.0" || ID != "core 2026.08.3" {
		t.Fatalf("Version %q, ID %q", Version, ID)
	}
}
