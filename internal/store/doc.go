// Package store owns the store tree layout (design.md §8.1: record shards,
// stats files, blobs, epoch marker), tip ⇄ record-set materialization, and
// candidate commit builds. It rides gitio for object/ref plumbing and makes
// no merge or sweep decisions (those are core/union's). Arrives with M4.
package store
