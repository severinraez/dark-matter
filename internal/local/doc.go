// Package local owns .git/.dm/* (design.md §8.2): pending records, blobs
// and stats, the replica id, cache file mechanics (atomic build+rename),
// and the invocation lock. Cache keys and invalidation semantics are
// dictated from above. Arrives with M2.
package local
