// Package evcache is evidence.Cached: the concrete evidence value app wires
// and hands to core — a caching decorator composing gitio (raw queries)
// with local's cache files, implementing all three role interfaces of the
// Evidence port (architecture.md §2, §5).
//
// It carries real logic of its own: the ff-aware, delta-scoped
// unreachability revalidation (design.md §8.2). Core stays cache-oblivious;
// deleting .git/.dm/cache/ must be unobservable to core-level results.
// Arrives with M5–M6.
package evcache
