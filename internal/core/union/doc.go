// Package union owns store-set policy: the CRDT union merge, the epoch
// rule, sweep droppability (rescue pins, VD retention, TB-forever), and the
// stats-file merge (design.md §8.4, §8.7, §7.2; architecture.md §6).
//
// Pure decisions only — tree layout and commit builds are internal/store's.
// M4 carries merge + epoch + the stats-file merge; sweep arrives with M8.
package union
