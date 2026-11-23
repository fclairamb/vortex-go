package vortex

import (
	"bytes"
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"sort"
	"unicode/utf8"

	"github.com/fclairamb/vortex-go/internal/fbs"
	"github.com/fclairamb/vortex-go/internal/flatbuf"
)

// The limits of the user metadata of a file, the ones of the reference (MAX_METADATA_SEGMENTS
// and MAX_METADATA_KEY_BYTES): its writer refuses more, its reader refuses a file with more.
const (
	// MaxMetadataEntries is the number of metadata entries a file may hold.
	MaxMetadataEntries = 16
	// MaxMetadataKeyBytes is the length of a metadata key, in bytes of UTF-8.
	MaxMetadataKeyBytes = 64
	// maxMetadataAlign is the largest alignment the reference accepts from a file (64 KiB).
	maxMetadataAlign = 16
)

// metaEntry is a metadata entry of an open file: where its value is, and the value itself
// when the bytes read at Open held it.
type metaEntry struct {
	key    string
	value  []byte // nil when the value was outside the bytes read at Open
	offset uint64
	length uint32
}

// checkMetadataKey checks a key against the rules of the reference: not empty, valid UTF-8 (a
// flatbuffers string is), at most MaxMetadataKeyBytes bytes.
func checkMetadataKey(key string) error {
	switch {
	case key == "":
		return fmt.Errorf("a metadata key is empty")
	case !utf8.ValidString(key):
		return fmt.Errorf("the metadata key %q is not valid UTF-8", key)
	case len(key) > MaxMetadataKeyBytes:
		return fmt.Errorf("the metadata key %q is %d bytes, at most %d", key, len(key), MaxMetadataKeyBytes)
	}
	return nil
}

// checkMetadata checks one entry the writer is given.
func checkMetadata(key string, value []byte) error {
	if err := checkMetadataKey(key); err != nil {
		return err
	}
	if uint64(len(value)) > maxSegment {
		return fmt.Errorf("the metadata value of %q is %d bytes, at most %d", key, len(value), maxSegment)
	}
	return nil
}

// setMetadata takes the metadata of the options, copied.
func (w *Writer) setMetadata(m map[string][]byte) error {
	if len(m) > MaxMetadataEntries {
		return fmt.Errorf("vortex: %d metadata entries, at most %d", len(m), MaxMetadataEntries)
	}
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if err := w.SetMetadata(k, m[k]); err != nil {
			return err
		}
	}
	return nil
}

// SetMetadata adds an entry to the metadata of the file, or replaces the value of its key: the
// way to store what is only known once the rows are written. The writer keeps a copy of value
// and writes it at Close. A key is a non-empty UTF-8 string of at most MaxMetadataKeyBytes
// bytes, and a file holds at most MaxMetadataEntries entries: SetMetadata returns an error for
// an entry beyond these, and the writer goes on without it. After Close it returns
// ErrWriterClosed.
func (w *Writer) SetMetadata(key string, value []byte) error {
	if w.done {
		return ErrWriterClosed
	}
	if err := checkMetadata(key, value); err != nil {
		return fmt.Errorf("vortex: SetMetadata: %w", err)
	}
	if _, ok := w.userMeta[key]; !ok && len(w.userMeta) == MaxMetadataEntries {
		return fmt.Errorf("vortex: SetMetadata: %q would be metadata entry %d, at most %d",
			key, MaxMetadataEntries+1, MaxMetadataEntries)
	}
	if w.userMeta == nil {
		w.userMeta = map[string][]byte{}
	}
	w.userMeta[key] = bytes.Clone(value)
	return nil
}

// writeMetadata writes the values of the metadata, in the order of their keys, and returns the
// offset of each.
func (w *Writer) writeMetadata() (keys []string, offsets []int64) {
	keys = slices.Sorted(maps.Keys(w.userMeta))
	offsets = make([]int64, len(keys))
	for i, k := range keys {
		offsets[i] = w.pos
		w.put(w.userMeta[k])
	}
	return keys, offsets
}

// metadataVector builds the vector of the entries of the postscript, 0 when there is none.
func (w *Writer) metadataVector(keys []string, offsets []int64) flatbuf.Off {
	if len(keys) == 0 {
		return 0
	}
	entries := make([]flatbuf.Off, len(keys))
	for i, k := range keys {
		key := w.fb.String(k)
		w.fb.StartTable(3)
		w.fb.Uint64(0, uint64(offsets[i]), 0)
		w.fb.Uint32(1, uint32(len(w.userMeta[k])), 0)
		s := w.fb.EndTable() // alignment 1, the default
		w.fb.StartTable(2)
		w.fb.Ref(0, key)
		w.fb.Ref(1, s)
		entries[i] = w.fb.EndTable()
	}
	return w.fb.Offsets(entries)
}

// readMetadata reads the metadata entries of the postscript, with the checks of the reference
// reader: their number, their keys, where their values are. b has the bytes read at Open:
// the values it holds are kept.
func readMetadata(ps fbs.Postscript, psStart uint64, size int64, b blob) ([]metaEntry, error) {
	list, err := collect(ps.Metadata())
	if err != nil {
		return nil, wrapCorrupt("postscript metadata", psStart, err)
	}
	if len(list) > MaxMetadataEntries {
		return nil, corrupt(int64(psStart), "the postscript has %d metadata entries, at most %d",
			len(list), MaxMetadataEntries)
	}
	out := make([]metaEntry, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, m := range list {
		e, err := readMetaEntry(m, psStart, size)
		if err != nil {
			return nil, err
		}
		if seen[e.key] {
			return nil, corrupt(int64(psStart), "the metadata key %q is there twice", e.key)
		}
		seen[e.key] = true
		if lo := int64(e.offset); lo >= b.lo {
			e.value = bytes.Clone(b.buf[lo-b.lo : lo-b.lo+int64(e.length)])
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out, nil
}

func readMetaEntry(m fbs.PostscriptMetadata, psStart uint64, size int64) (metaEntry, error) {
	key, err := m.Key()
	if err == nil {
		err = checkMetadataKey(key)
	}
	if err != nil {
		return metaEntry{}, wrapCorrupt("postscript metadata key", psStart, err)
	}
	s, ok, err := m.Segment()
	if err == nil && !ok {
		err = fmt.Errorf("no segment")
	}
	var e metaEntry
	var exp uint8
	if err == nil {
		e.offset, err = s.Offset()
	}
	if err == nil {
		e.length, err = s.Length()
	}
	if err == nil {
		exp, err = s.AlignmentExponent()
	}
	if err != nil {
		return e, wrapCorrupt(fmt.Sprintf("postscript metadata %q", key), psStart, err)
	}
	at := int64(min(e.offset, math.MaxInt64))
	switch {
	case e.offset > uint64(size) || uint64(e.length) > uint64(size)-e.offset:
		return e, corrupt(at, "the metadata value of %q, %d bytes, is past the end of the file (%d bytes)",
			key, e.length, size)
	case exp > maxMetadataAlign:
		return e, corrupt(at, "the metadata value of %q is aligned to 2^%d, at most 2^%d", key, exp, maxMetadataAlign)
	case e.offset%(1<<exp) != 0:
		return e, corrupt(at, "the metadata value of %q is not aligned to %d", key, 1<<exp)
	}
	e.key = key
	return e, nil
}

// MetadataKeys returns the keys of the metadata of the file, sorted. A file holds at most
// MaxMetadataEntries of them.
func (f *File) MetadataKeys() []string {
	keys := make([]string, len(f.meta))
	for i, e := range f.meta {
		keys[i] = e.key
	}
	return keys
}

// Metadata returns the value of a key of the metadata of the file, and whether the file has
// it. The value is a new slice, the caller's to keep. A value is read with the rest of the end
// of the file at Open when it lies there, as the small ones the writer stores just before the
// footer do; another one is read now, from the reader of the file, and the error is the one of
// that read. After Close, that read returns an error that wraps os.ErrClosed.
func (f *File) Metadata(key string) ([]byte, bool, error) {
	i, ok := slices.BinarySearchFunc(f.meta, key, func(e metaEntry, k string) int {
		switch {
		case e.key < k:
			return -1
		case e.key > k:
			return 1
		}
		return 0
	})
	if !ok {
		return nil, false, nil
	}
	e := f.meta[i]
	if e.value != nil {
		return bytes.Clone(e.value), true, nil
	}
	lo, hi := int64(e.offset), int64(e.offset)+int64(e.length)
	if m := f.mapped; m != nil {
		if !m.begin() {
			return nil, true, fmt.Errorf("vortex: Metadata: %w", os.ErrClosed)
		}
		defer m.end()
		return bytes.Clone(m.view(lo, hi)), true, nil
	}
	if f.closed.Load() {
		return nil, true, fmt.Errorf("vortex: Metadata: %w", os.ErrClosed)
	}
	buf := make([]byte, e.length)
	if err := readFull(f.r, buf, lo); err != nil {
		return nil, true, fmt.Errorf("vortex: Metadata %q: %w", key, err)
	}
	return buf, true, nil
}
