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
	run, err := firstRun(ends, offset, rows)
	if err != nil {
		return nil, err
	}
	if values.Type.Kind == Prim && values.Validity == nil {
		return withType(fillRuns(n.ctx.opt.Arena, values, ends, offset, run, rows), t), nil
	}
	idx := make([]int, rows)
	for i := range idx {
		for ends[run] <= offset+i {
			run++
		}
		idx[i] = run
	}
	return withType(take(n.ctx.opt.Arena, values, idx), t), nil
}

// firstRun checks the ends of the runs of rows rows from position offset, which must strictly
// increase, the last one covering the rows, and returns the run that row 0 is in.
func firstRun(ends []int, offset, rows int) (int, error) {
	for i := 1; i < len(ends); i++ {
		if ends[i] <= ends[i-1] {
			return 0, fmt.Errorf("run end %d is %d, after %d", i, ends[i], ends[i-1])
		}
	}
	if rows == 0 {
		return 0, nil
	}
	if len(ends) == 0 || ends[len(ends)-1] < offset+rows {
		return 0, fmt.Errorf("the runs end before row %d", offset+rows)
	}
	return sort.SearchInts(ends, offset+1), nil // the first run that ends after offset
}

// fillRuns is the rows rows from position offset of runs of numbers, none null: a run fills
// its rows with its value, as the reference reader does.
func fillRuns(ar *Arena, values *Array, ends []int, offset, run, rows int) *Array {
	w := values.Type.PType.Width()
	out := &Array{Type: values.Type, Len: rows, Data: ar.Bytes(rows*w, false)}
	switch w {
	case 1:
		fillRunsOf(out.Data, values.Data, ends, offset, run)
	case 2:
		fillRunsOf(words[uint16](out.Data), words[uint16](values.Data), ends, offset, run)
	case 4:
		fillRunsOf(words[uint32](out.Data), words[uint32](values.Data), ends, offset, run)
	default:
		fillRunsOf(words[uint64](out.Data), words[uint64](values.Data), ends, offset, run)
	}
	return out
}

// fillRunsOf fills out with the values of the runs, from run.
func fillRunsOf[T word](out, vals []T, ends []int, offset, run int) {
	for pos := 0; pos < len(out); run++ {
		end := min(ends[run]-offset, len(out))
		v := vals[run]
		for i := range out[pos:end] {
			out[pos+i] = v
		}
		pos = end
	}
}
