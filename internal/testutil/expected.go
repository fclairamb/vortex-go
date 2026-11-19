// Package testutil holds test-only helpers. Nothing outside _test.go files imports it.
package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const kindStruct = "struct"

// DType is a node of the DType tree the oracle wrote. Only the fields of its Kind are set.
type DType struct {
	Kind        string   `json:"kind"`
	Ptype       string   `json:"ptype,omitempty"`
	ExtID       string   `json:"ext_id,omitempty"`
	ExtMetadata string   `json:"ext_metadata,omitempty"`
	Element     *DType   `json:"element,omitempty"`
	Key         *DType   `json:"key,omitempty"`
	Value       *DType   `json:"value,omitempty"`
	Storage     *DType   `json:"storage,omitempty"`
	Fields      []*Field `json:"fields,omitempty"`
	Precision   int      `json:"precision,omitempty"`
	Scale       int      `json:"scale,omitempty"`
	Size        int      `json:"size,omitempty"`
	Nullable    bool     `json:"nullable"`
	KeysSorted  bool     `json:"keys_sorted,omitempty"`
}

// Field is a named field of a struct DType.
type Field struct {
	DType *DType `json:"dtype"`
	Name  string `json:"name"`
}

// Column is what the oracle read for one column, or one nested struct field (dotted path).
type Column struct {
	// SHA256 is the canonical hash, nil for the types the oracle does not hash (variant).
	SHA256      *string  `json:"sha256"`
	SampleError string   `json:"sample_error,omitempty"`
	First       []string `json:"first"`
	Last        []string `json:"last"`
	Nulls       int64    `json:"nulls"`
}

// Expected is the content of a <file>.expected.json.
type Expected struct {
	Columns   map[string]Column `json:"columns"`
	Encodings []string          `json:"encodings"`
	Schema    DType             `json:"schema"`
	Rows      int64             `json:"rows"`
}

// LoadExpected reads and parses a <file>.expected.json. Unknown keys are an error, so a
// change of the oracle's format cannot go unnoticed.
func LoadExpected(path string) (*Expected, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var e Expected
	if err := dec.Decode(&e); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &e, nil
}

// Paths lists the column paths the oracle writes for a struct DType: each field, then, for
// a struct field, its own fields as dotted paths.
func (d *DType) Paths(prefix string) []string {
	var out []string
	for _, f := range d.Fields {
		p := prefix + f.Name
		out = append(out, p)
		if f.DType.Kind == kindStruct {
			out = append(out, f.DType.Paths(p+".")...)
		}
	}
	return out
}

// isVariant reports whether the oracle leaves the hash of this DType to the variant spec.
func (d *DType) isVariant() bool {
	return d.Kind == "extension" && strings.Contains(d.ExtID, "variant")
}

// Check returns what is wrong with the expected values: no column of the schema may lack
// an entry, and every entry has a hash, except the variant ones.
func (e *Expected) Check() error {
	if e.Schema.Kind != kindStruct {
		return fmt.Errorf("schema is a %q, want a struct", e.Schema.Kind)
	}
	if e.Rows < 0 {
		return fmt.Errorf("rows is %d", e.Rows)
	}
	return e.Schema.checkColumns("", e.Columns)
}

func (d *DType) checkColumns(prefix string, columns map[string]Column) error {
	for _, f := range d.Fields {
		p := prefix + f.Name
		c, ok := columns[p]
		if !ok {
			return fmt.Errorf("column %q has no expected values", p)
		}
		if c.SHA256 == nil && !f.DType.isVariant() {
			return fmt.Errorf("column %q has no hash", p)
		}
		if f.DType.Kind == kindStruct {
			if err := f.DType.checkColumns(p+".", columns); err != nil {
				return err
			}
		}
	}
	return nil
}
