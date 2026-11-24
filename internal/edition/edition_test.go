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
	if Version != "0.94.0" || ID != "core 2026.08.3" {
		t.Fatalf("Version %q, ID %q", Version, ID)
	}
}

func TestKnown(t *testing.T) {
	for _, name := range []string{"core 2025.05.0", "core 2025.10.0", "core 2026.08.1", ID} {
		if !Known(name) {
			t.Errorf("%q is unknown", name)
		}
	}
	for _, name := range []string{"", "core 2025.07.0", "preview 2026.08.0", "core 2099.01.0"} {
		if Known(name) {
			t.Errorf("%q is known", name)
		}
	}
}

func TestAllows(t *testing.T) {
	cases := []struct {
		edition, kind, id string
		want              bool
	}{
		{"core 2025.05.0", KindLayout, "vortex.stats", true},
		{"core 2025.05.0", KindLayout, "vortex.zoned", false},
		{"core 2025.10.0", KindArray, "vortex.listview", true},
		{"core 2025.06.0", KindArray, "vortex.listview", false},
		{"core 2026.08.0", KindLayout, "vortex.zoned", true},
		{"core 2026.08.2", KindDType, "vortex.uuid", false},
		{ID, KindDType, "vortex.uuid", true},
		{ID, KindArray, "acme.unknown", false},
		{"core 2025.07.0", KindArray, "vortex.bool", false},
	}
	for _, c := range cases {
		if got := Allows(c.edition, c.kind, c.id); got != c.want {
			t.Errorf("Allows(%q, %q, %q) = %v, want %v", c.edition, c.kind, c.id, got, c.want)
		}
	}
}

// The editions compare as strings: every name has the same width, the date as numbers.
func TestEditionsOrderAsText(t *testing.T) {
	for _, m := range Members() {
		if len(m.Since) != len(ID) {
			t.Errorf("%q is not as wide as %q", m.Since, ID)
		}
		if m.Since > ID {
			t.Errorf("%s %s is since %q, after the newest edition", m.Kind, m.ID, m.Since)
		}
	}
}
