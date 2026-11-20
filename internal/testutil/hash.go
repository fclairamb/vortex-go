package testutil

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
)

// Hasher computes the canonical hash of docs/testing.md, section 1, the one the oracle
// computes too: for each row, one validity byte, then the value when the row is valid. The
// caller walks the rows in order and tells the hasher about each one. Integers and floats
// are their little-endian bytes, a bool is one byte, a struct is its fields in order, each
// one a row of its own type.
type Hasher struct {
	h hash.Hash
}

// NewHasher starts a hash.
func NewHasher() *Hasher { return &Hasher{h: sha256.New()} }

// Null adds a null row, of any type.
func (h *Hasher) Null() { h.h.Write([]byte{0}) }

// Valid adds the validity byte of a valid row: its value follows, with Raw or Bool, or its
// fields, each with its own validity byte.
func (h *Hasher) Valid() { h.h.Write([]byte{1}) }

// Raw adds the bytes of a value, as they are in the file.
func (h *Hasher) Raw(b []byte) { h.h.Write(b) }

// Bool adds the value of a bool.
func (h *Hasher) Bool(v bool) {
	if v {
		h.h.Write([]byte{1})
	} else {
		h.h.Write([]byte{0})
	}
}

// Sum is the hash so far, in hexadecimal.
func (h *Hasher) Sum() string { return hex.EncodeToString(h.h.Sum(nil)) }
