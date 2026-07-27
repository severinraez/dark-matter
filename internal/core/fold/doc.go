// Package fold owns the per-entry fold: LWW registers, the absorbing
// tombstone, the rescue fold, per-record rule-(b) consumption, and FB
// dispute + churn derivation (design.md §8.3, §7; architecture.md §6).
//
// Data-in by design: rule-(b) classification arrives as a plain map, never
// through a port — the most test-pinned semantic in the system unit-tests
// with zero fakes. Arrives with M3.
package fold
