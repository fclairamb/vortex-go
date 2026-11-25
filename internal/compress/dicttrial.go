package compress

import (
	"bytes"
	"hash/maphash"
	"math"

	"github.com/fclairamb/vortex-go/internal/encode"
)

// The dictionary of strings is measured on all the rows of the array, not on its sample, as the
// reference does since 0.94.0 (bytes_candidate.rs of vortex-compressor): a sample of a column of
// thousands of distinct values misses most of its repeats, and rates the dictionary too low. A
// probe of whole runs of rows estimates the distinct values first, and a cost model turns them
// into the best ratio the dictionary could have: it is built only when that ratio beats the best
// one of the sample. When the probe or the dictionary would be too large, the dictionary is
// measured on the sample, as the other candidates.
const (
	dictMaxEntries   = 1 << 16 // so that its codes are 16 bits
	dictMaxBytes     = 1 << 22 // its views and the bytes of its long values
	dictMaxCodeBytes = 1 << 24 // the codes of the rows, 2 bytes each
	dictMaxInput     = 1 << 28 // the bytes of the valid rows
	probeMaxBytes    = 1 << 20 // the bytes of the valid rows of a probe
	probeRun         = 64      // the rows of a run of the probe
	probeRuns        = 128     // the runs of the probe
	headRun          = 16      // the rows of a run of the first probe, that rules out unique rows
	minHeadRows      = 1024    // the rows of a first probe that can tell unique rows
	maxValuesRatio   = 8.0     // the best ratio FSST or OnPair can have on distinct strings
)

// admission is what the cost model makes of a dictionary.
type admission uint8

const (
	admitTrial    admission = iota // build it on all the rows
	admitReject                    // it cannot beat the best ratio
	admitTooLarge                  // measure it on the sample
)

// profile is what the cost model knows of all the rows.
type profile struct {
	rows, valid, valueBytes, plain int
}

func (p profile) mean() float64 { return float64(p.valueBytes) / float64(p.valid) }

// probe is the count of the values of whole runs of rows across the array.
type probe struct {
	rows, distinct, singletons, doubletons int
	pairs, changes                         int // adjacent rows of a run, and those that differ
	complete                               bool
}

// probeOf counts the valid values of probeRuns runs of size rows, false when they hold more
// than probeMaxBytes bytes or no valid row.
func (c *Compressor) probeOf(v [][]byte, nulls []bool, size int) (probe, bool) {
	var p probe
	held := 0
	p.complete = stratified(len(v), size, probeRuns, func(start, n int) {
		for i := start; i < start+n; i++ {
			if !isNull(nulls, i) {
				held += len(v[i])
			}
		}
	}) == len(v)
	if held > probeMaxBytes {
		return p, false
	}
	if c.counts == nil {
		c.counts = map[uint64]uint32{}
	}
	clear(c.counts)
	stratified(len(v), size, probeRuns, func(start, n int) {
		for i := start; i < start+n; i++ {
			if i > start {
				p.pairs++
				if isNull(nulls, i) != isNull(nulls, i-1) || !isNull(nulls, i) && !bytes.Equal(v[i], v[i-1]) {
					p.changes++
				}
			}
			if !isNull(nulls, i) {
				c.counts[maphash.Bytes(seed, v[i])]++
				p.rows++
			}
		}
	})
	if p.rows == 0 {
		return p, false
	}
	p.distinct = len(c.counts)
	for _, k := range c.counts {
		switch k {
		case 1:
			p.singletons++
		case 2:
			p.doubletons++
		}
	}
	return p, true
}

// unique says whether every value of the probe is distinct, in a probe large enough to tell.
func (p probe) unique() bool { return p.distinct == p.rows && (p.complete || p.rows >= minHeadRows) }

// estimated is the distinct values of the valid rows, by the Chao1 estimator.
func (p probe) estimated(valid int) float64 {
	d := float64(p.distinct)
	if p.complete {
		return d
	}
	f1, f2 := float64(p.singletons), float64(p.doubletons)
	unseen := f1 * (f1 - 1) / 2
	if p.doubletons > 0 {
		unseen = f1 * f1 / (2 * f2)
	}
	return min(d+unseen, float64(valid))
}

// runStarts is the share of the rows that start a run of equal values.
func (p probe) runStarts() float64 {
	if p.pairs == 0 {
		return 1
	}
	return max(float64(p.changes)/float64(p.pairs), 1.0/probeRun)
}

// admit applies the bounds and the cost model to the dictionary, for the best ratio so far.
func admit(pr profile, p probe, best float64) admission {
	distinct := p.estimated(pr.valid)
	entries := distinct
	if pr.valid < pr.rows {
		entries++
	}
	values := entries * (pr.mean() + 16) / min(best, maxValuesRatio)
	codes := float64(pr.rows) * p.runStarts() * math.Ceil(math.Log2(max(entries, 2))) / 8
	if float64(pr.plain)/max(values+codes, 1) <= best {
		return admitReject
	}
	if entries > dictMaxEntries || distinct*pr.mean() > dictMaxBytes || pr.valueBytes > dictMaxInput ||
		2*pr.rows > dictMaxCodeBytes {
		return admitTooLarge
	}
	return admitTrial
}

// dictAdmit is what the cost model makes of the dictionary of the strings v, which cost plain
// bytes as a plain array, for the best ratio of the sample so far.
func dictAdmit(c *Compressor, v [][]byte, nulls []bool, plain int, best ratio) admission {
	pr := profile{rows: len(v), plain: plain}
	for i, s := range v {
		if !isNull(nulls, i) {
			pr.valid++
			pr.valueBytes += len(s)
		}
	}
	b := max(float64(best.num)/float64(best.den), 1)
	if head, ok := c.probeOf(v, nulls, headRun); ok && head.unique() && admit(pr, head, b) == admitReject {
		return admitReject
	}
	p, ok := c.probeOf(v, nulls, probeRun)
	if !ok {
		return admitTooLarge
	}
	return admit(pr, p, b)
}

// tooLarge says whether the values of a dictionary of strings, n, are over the bounds of the
// dictionary built on all the rows.
func tooLarge(n *encode.Node) bool {
	vals := n.Kids[1]
	views := len(vals.Bufs[1].Data)
	return views/16 > dictMaxEntries || views+len(vals.Bufs[0].Data) > dictMaxBytes
}
