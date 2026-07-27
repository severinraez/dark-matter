// Package view owns ranking, search term matching, surface/digest assembly,
// worklist assembly + line grouping, and the crowding threshold (design.md
// §5.5–§5.6, §9.6; architecture.md §6).
//
// It also owns the read-model (Surface, WorklistReport, acks) that app
// returns and cli renders under the relaxed import rule. Evidence role
// consumed: Lineage. Arrives with M7 (worklist grouping M6).
package view
