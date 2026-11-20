// Package bench compares vortex-go with parquet-go on the same data. It is a separate module,
// so that parquet-go is not a dependency of vortex-go: not in the root go.mod, not in make
// deps, not in the coverage gate.
package bench
