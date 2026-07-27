// Package gitio is the faithful git plumbing wrapper (design.md §12):
// hash-object, ancestry, similarity diff, patch-id, reflog, refs,
// commit-tree/update-ref, fetch/push. Primary Evidence implementor.
//
// gitio interprets nothing: it answers questions git can answer; every
// threshold, band, and guard lives in core. Arrives with M2.
package gitio
