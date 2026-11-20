package pco

// undelta turns the body of a latent variable, deltas, into its latents: n of them.
// lookbacks are the body of the delta latent variable.
func (c *Chunk) undelta(key int, p *pageVar, n int, lookbacks []uint64) []uint64 {
	nb := p.v.bits
	mask, mid := p.v.mask, uint64(1)<<(nb-1)
	switch c.kind(key) {
	case deltaConsecutive:
		consecutive(p.state, p.body, mask, mid)
	case deltaLookback:
		return c.lookback(p, n, lookbacks)
	case deltaConv1:
		return c.conv1(p, n)
	}
	return p.body
}

// consecutive decodes deltas of order len(state) in place. The deltas are signed, so they are
// stored with the middle of the range as zero. Each pass is a cumulative sum from the state.
func consecutive(state, a []uint64, mask, mid uint64) {
	for i := range a {
		a[i] = (a[i] + mid) & mask
	}
	for m := len(state) - 1; m >= 0; m-- {
		moment := state[m]
		for i, d := range a {
			a[i] = moment
			moment = (moment + d) & mask
		}
	}
}

// checkLookbacks checks that the lookbacks of a page are within the window.
func (c *Chunk) checkLookbacks(lookbacks []uint64) error {
	window := uint64(1) << c.delta.windowLog
	for _, lb := range lookbacks {
		if lb < 1 || lb > window {
			return corrupt("lookback of %d, the window is %d", lb, window)
		}
	}
	return nil
}

// lookback decodes x[g] = delta[g] + x[g - lookback[g]], where the x before the first one are
// the state, and zero before that. The result is the state then the x, cut to n.
func (c *Chunk) lookback(p *pageVar, n int, lookbacks []uint64) []uint64 {
	mask, mid := p.v.mask, uint64(1)<<(p.v.bits-1)
	st := len(p.state)
	hist := make([]uint64, st+p.count)
	copy(hist, p.state)
	for g := 0; g < p.count; g++ {
		x := (p.body[g] + mid) & mask
		if idx := st + g - int(lookbacks[g]); idx >= 0 {
			x = (x + hist[idx]) & mask
		}
		hist[st+g] = x
	}
	return hist[:n]
}

// conv1 decodes x[i] = delta[i] + (bias + sum(weight[j] * x[i-order+j])) >> quantization, the
// sum being clamped at 0, with the state as the first x.
func (c *Chunk) conv1(p *pageVar, n int) []uint64 {
	d := &c.delta
	order := len(d.weights)
	mask, mid := p.v.mask, uint64(1)<<(p.v.bits-1)
	x := make([]uint64, order+p.count)
	copy(x, p.state)
	for i := range p.count {
		s := d.bias
		for j, w := range d.weights {
			s += w * int64(x[i+j])
		}
		x[order+i] = ((p.body[i] + mid) & mask) + uint64(max(s, 0)>>d.quant)
		x[order+i] &= mask
	}
	return x[:n]
}
