package testutil

import "testing"

func TestHasher(t *testing.T) {
	h := NewHasher()
	if got, want := h.Sum(), "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"; got != want {
		t.Fatalf("empty = %s", got)
	}
	h.Valid()
	h.Bool(true)
	h.Null()
	h.Valid()
	h.Bool(false)
	h.Raw([]byte{7, 0, 0, 0})
	// sha256 of 01 01 00 01 00 07 00 00 00, computed with hashlib.
	if got, want := h.Sum(), "e0dfc6d9ce768779693c831072db961d2345d900315425551359f231969cdb4c"; got != want {
		t.Fatalf("hash = %s, want %s", got, want)
	}
}
