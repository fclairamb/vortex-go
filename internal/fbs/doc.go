// Package fbs holds typed accessors over flatbuf.Table for the tables of the four flatbuffers
// schemas a Vortex file uses. They are written by hand from the .fbs files at the pinned
// upstream tag, with the field ids in comments (ids follow declaration order, a union takes
// two). message.fbs (IPC) is not used by files and is left out.
//
//   - vortex-file/flatbuffers/vortex-file/footer.fbs: Postscript, PostscriptSegment,
//     PostscriptMetadata, Footer, ArraySpec, LayoutSpec, SegmentSpec, FileStatistics
//   - vortex-layout/flatbuffers/vortex-layout/layout.fbs: Layout
//   - vortex-array/flatbuffers/vortex-array/array.fbs: Array, ArrayNode, Buffer, ArrayStats
//   - vortex-array/flatbuffers/vortex-dtype/dtype.fbs: DType and its union
//
// None of these schemas uses nested flatbuffers, vectors of unions, shared strings or 64-bit
// offsets, so internal/flatbuf does not implement them.
package fbs
