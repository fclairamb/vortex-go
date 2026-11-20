package vortex

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The error messages of the common mistakes (docs/testing.md section 6), asserted as text: they
// name the column, the types and the offset, so a user can fix the call from the message.

const fixtureInts = "testdata/fixtures/0.93.0/extra/default_ints.vortex"

func TestMessageUnknownColumn(t *testing.T) {
	f, err := OpenFile(fixtureInts)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var got error
	for _, err := range f.Scan(context.Background(), Query{}, "nope") {
		got = err
		break
	}
	if got == nil || got.Error() != `vortex: Scan: unknown column "nope"` {
		t.Fatalf("message %v", got)
	}
}

func TestMessageFieldTypeMismatch(t *testing.T) {
	f, err := OpenFile(fixtureInts)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	type Row struct {
		Sorted sql.Null[string] `vortex:"sorted"`
	}
	var got error
	for _, err := range Rows[Row](context.Background(), f, Query{}) {
		got = err
		break
	}
	if got == nil || got.Error() != `vortex: Rows: field Sorted (sql.Null[string]): column "sorted" of type i64?: want int64 or int` {
		t.Fatalf("message %v", got)
	}
}

func TestMessageNotVortex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("this is a text file, not a Vortex file"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, got := OpenFile(path)
	if !errors.Is(got, ErrNotVortex) || got.Error() != path+": vortex: not a Vortex file: bad magic at offset 0" {
		t.Fatalf("message %v", got)
	}
}

func TestMessageUnsupportedEncoding(t *testing.T) {
	f, err := OpenFile("testdata/fixtures/0.93.0/primitives.vortex")
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
	if !errors.Is(got, errors.ErrUnsupported) || got.Error() != `vortex: column "": segment at offset 8: unsupported encoding "acme.unknown" at column "": unsupported operation` {
		t.Fatalf("message %v", got)
	}
}
