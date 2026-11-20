// Package vortex reads and writes files in the Vortex columnar format.
//
// Open and OpenFile read the trailer, the footer and the schema of a file. Scan reads whole
// columns of numbers and booleans, batch by batch. Filters, the other types and writing files
// are not implemented yet. The goals are in docs/goals.md, the planned
// interface in docs/interface.md and the test plan in docs/testing.md.
package vortex
