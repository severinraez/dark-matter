// Package lineage owns rule-(b) classification — VD chains, unlanded
// voiding, the qualified/degraded-clone guard — and the matcher ladder
// (m1–m3 guards, mint pass, floors) (design.md §9.4; architecture.md §6).
//
// Evidence roles consumed: Lineage, Match. Classification feeds core/fold
// as plain data (map[origin]LandedState), keeping fold port-free.
//
// Arrives with M3 (classification consumed as data) and M6 (the ladder).
package lineage
