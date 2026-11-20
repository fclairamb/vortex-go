package pco

// pageVar is what a page holds for one latent variable.
type pageVar struct {
	v     *latentVar
	state []uint64 // the delta state
	body  []uint64 // n latents, the first count of them read
	ans   [interleaving]uint32
	count int // latents in the body of the page
}

// kind is the delta encoding that applies to a latent variable.
func (c *Chunk) kind(key int) int {
	d := &c.delta
	switch {
	case key == varDelta:
		return deltaNone
	case key == varSecondary && (d.kind == deltaConv1 || !d.secondary):
		return deltaNone
	}
	return d.kind
}

// stateCount is the number of latents of the delta state of a latent variable.
func (c *Chunk) stateCount(key int) int {
	switch c.kind(key) {
	case deltaConsecutive:
		return c.delta.order
	case deltaLookback:
		return 1 << c.delta.stateLog
	case deltaConv1:
		return len(c.delta.weights)
	}
	return 0
}

// Page decodes a page of n numbers into dst, which has room for n numbers, little-endian.
func (c *Chunk) Page(page []byte, n int, dst []byte) error {
	if n < 1 || n > maxN {
		return corrupt("a page of %d numbers", n)
	}
	if len(dst) != n*c.typ.Bits()/8 {
		return corrupt("a destination of %d bytes for %d %s", len(dst), n, c.typ)
	}
	r := newBitReader(page)
	pv, err := c.readVars(r, n)
	if err != nil {
		return err
	}
	for start := 0; start < n; start += batchN {
		for _, p := range pv {
			if p != nil {
				if k := min(batchN, max(0, p.count-start)); k > 0 {
					p.read(r, start, k)
				}
			}
		}
		if r.err != nil {
			return r.err
		}
	}
	r.align("page")
	if r.err != nil {
		return r.err
	}
	if p := pv[varDelta]; p != nil {
		if err := c.checkLookbacks(p.body[:p.count]); err != nil {
			return err
		}
	}
	return c.join(pv, n, dst)
}

// readVars reads the metadata of a page of n numbers, and prepares the latent variables.
func (c *Chunk) readVars(r *bitReader, n int) (pv [3]*pageVar, err error) {
	for key, v := range c.vars {
		if v != nil {
			pv[key] = &pageVar{v: v}
			if err := c.readPageMeta(r, pv[key], key); err != nil {
				return pv, err
			}
		}
	}
	r.align("page metadata")
	if r.err != nil {
		return pv, r.err
	}
	primaryBody := max(0, n-c.stateCount(varPrimary))
	for key, p := range pv {
		if p == nil {
			continue
		}
		p.count = max(0, n-c.stateCount(key))
		if key == varDelta {
			p.count = primaryBody
		}
		if p.v.nBins == 0 && primaryBody > 0 {
			return pv, corrupt("%d numbers and no bins", primaryBody)
		}
		p.body = make([]uint64, n)
	}
	return pv, nil
}

// readPageMeta reads the delta state and the tANS states of a latent variable.
func (c *Chunk) readPageMeta(r *bitReader, p *pageVar, key int) error {
	ns := c.stateCount(key)
	if ns*p.v.bits > r.left() {
		return corrupt("a delta state of %d latents, the input has %d bits left", ns, r.left())
	}
	p.state = make([]uint64, ns)
	for i := range p.state {
		p.state[i] = r.read(p.v.bits)
	}
	for i := range p.ans {
		p.ans[i] = uint32(r.read(p.v.ansLog))
	}
	return r.err
}

// read reads k latents of the body, from index start: first the tANS symbols of all of them,
// then their offsets.
func (p *pageVar) read(r *bitReader, start, k int) {
	v := p.v
	lat := p.body[start : start+k]
	var offBits [batchN]uint8
	for i := range lat {
		lane := i & (interleaving - 1)
		s := p.ans[lane]
		node := v.nodes[s]
		lat[i], offBits[i] = v.lowers[s], node.offBits
		p.ans[lane] = node.next + uint32(r.read(int(node.toRead)))
	}
	for i := range lat {
		lat[i] = (lat[i] + r.read(int(offBits[i]))) & v.mask
	}
}
