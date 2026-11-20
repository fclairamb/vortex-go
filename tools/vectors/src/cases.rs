//! Deterministic inputs: no random crate, so that a run gives the same files.

pub struct Rng(pub u64);

impl Rng {
    pub fn next(&mut self) -> u64 {
        // splitmix64
        self.0 = self.0.wrapping_add(0x9E3779B97F4A7C15);
        let mut z = self.0;
        z = (z ^ (z >> 30)).wrapping_mul(0xBF58476D1CE4E5B9);
        z = (z ^ (z >> 27)).wrapping_mul(0x94D049BB133111EB);
        z ^ (z >> 31)
    }
}

/// n values of `bits` bits; one in `every` (0 for none) has its high bits set instead, up to
/// `max` bits.
pub fn small(rng: &mut Rng, n: usize, bits: u32, every: u64, max: u32) -> Vec<u64> {
    (0..n)
        .map(|_| {
            let v = rng.next();
            if every > 0 && v % every == 0 {
                let wide = rng.next() >> (64 - max);
                wide | (1u64 << (max - 1).max(bits))
            } else if bits == 0 {
                0
            } else {
                rng.next() >> (64 - bits)
            }
        })
        .collect()
}

/// Runs of 1 to `max` equal values.
pub fn runs(rng: &mut Rng, n: usize, max: u64, bits: u32) -> Vec<u64> {
    let mut out = Vec::with_capacity(n);
    while out.len() < n {
        let v = rng.next() >> (64 - bits);
        let k = 1 + rng.next() % max;
        for _ in 0..k.min((n - out.len()) as u64) {
            out.push(v);
        }
    }
    out
}

/// One value in `k` is null.
pub fn nulls(rng: &mut Rng, n: usize, k: u64) -> Vec<bool> {
    let mut v: Vec<bool> = (0..n).map(|_| rng.next() % k != 0).collect();
    // a null array has a valid value, and a valid array a null: the cases of an all valid or
    // all null array are not vectors.
    if n > 1 {
        v[0] = true;
        v[1] = false;
    }
    v
}
