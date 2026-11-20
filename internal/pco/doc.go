// Package pco decodes the wrapped format of Pco, the numeric codec behind vortex.pco.
//
// It follows docs/format.md and the source of the pco crate, version 1.1.0 (format version
// 4.1). The wrapped format has three parts: a header, per chunk a metadata block, and per page
// of a chunk the compressed numbers. This package reads the header (ReadHeader), a chunk
// metadata (Header.Chunk) and a page (Chunk.Page); how the parts are stored is the caller's
// business.
//
// Latents are held as uint64 whatever their width, the arithmetic wrapping at the width of the
// type. A page is decoded in full: its numbers are never held in the batches of 256 that the
// reference reader works on, which gives the same numbers.
package pco
