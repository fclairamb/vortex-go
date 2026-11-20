// Package vortex reads and writes files in the Vortex columnar format.
//
// Open and OpenFile read the trailer, the footer and the schema of a file. Scan reads columns
// of numbers, booleans, strings, binaries, decimals, lists, maps and structs, batch by batch,
// for the columns, the row range and the filter of a Query, and skips what the statistics of
// the file rule out. Rows reads the same rows into Go structs. Writing files is not
// implemented yet. The goals are in docs/goals.md, the planned interface in docs/interface.md
// and the test plan in docs/testing.md.
package vortex

//go:generate go test -run=^TestReadmeCompat$ -update-readme .
