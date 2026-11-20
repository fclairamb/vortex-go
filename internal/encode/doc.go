// Package encode is the encoders of the lightweight encodings: bit-packing, frame of
// reference, zigzag, run-length, run-end, dictionary, sequence, constant and sparse.
//
// An encoder takes a typed slice and the nulls of its rows (true for a null row, nil for
// none) and returns the Node tree that the array writer serializes: the identifier, the
// metadata, the buffers and the children of each array, as the reference writes them. For the
// deterministic encoders the bytes are the reference's, which testdata/vectors checks.
//
// Every encoder belongs to an Encoder, which owns all the memory of the nodes it returns: they
// are valid until the next Reset. A running Encoder allocates nothing.
//
// Each encoder has an Estimate twin that returns, from a sample of the rows, the size in bytes
// of the buffers of the encoded array, without building it.
package encode
