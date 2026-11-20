package array

import (
	"fmt"
	"sort"
)

// decodeRunEnd decodes vortex.runend: metadata {ends_ptype, num_runs, offset}, children the
// ends (the exclusive end of each run, strictly increasing) and the values, one per run. The
// array is rows values from position offset of the runs.
func decodeRunEnd(n *Node, t DType, rows int) (*Array, error) {
	meta, err := varints(n.Metadata(), 3)
	if err != nil {
		return nil, err
	}
	p, err := intType("ends_ptype", meta[0])
	if err != nil {
		return nil, err
	}
	runs, err := count("num_runs", meta[1])
	if err != nil {
		return nil, err
	}
	offset, err := count("offset", meta[2])
	if err != nil {
		return nil, err
	}
	if n.NumBuffers() != 0 || n.NumChildren() != 2 {
		return nil, fmt.Errorf("expected no buffer and 2 children, got %d and %d", n.NumBuffers(), n.NumChildren())
	}
	ends, err := n.ints(0, "run ends", p, runs)
	if err != nil {
		return nil, err
	}
	values, err := n.Child(1, t, runs)
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}
	for i := 1; i < len(ends); i++ {
		if ends[i] <= ends[i-1] {
			return nil, fmt.Errorf("run end %d is %d, after %d", i, ends[i], ends[i-1])
		}
	}
	if rows == 0 {
		return withType(take(values, nil), t), nil
	}
	if len(ends) == 0 || ends[len(ends)-1] < offset+rows {
		return nil, fmt.Errorf("the runs end before row %d", offset+rows)
	}
	idx := make([]int, rows)
	run := sort.SearchInts(ends, offset+1) // the first run that ends after offset
	for i := range idx {
		for ends[run] <= offset+i {
			run++
		}
		idx[i] = run
	}
	return withType(take(values, idx), t), nil
}
