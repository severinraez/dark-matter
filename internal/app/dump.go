package app

import (
	"fmt"
	"io"
	"sort"

	"meltcloud.io/dm/internal/core/record"
)

// Dump is `dm dump` (§11.3): the full raw state of store ∪ pending in a
// deterministic, un-budgeted, unranked form — the ground truth, bypassing
// view. Prints every record's canonical encoding in rec-id (fold) order,
// then every stat row (per-replica counters + t, §7.2) with this
// replica's pending deltas folded into its own file.
func Dump(dir string, det Determinism, stdout, notice io.Writer) error {
	s, err := OpenSession(dir, det, notice)
	if err != nil {
		return err
	}
	defer s.Close()
	for _, rec := range s.View() {
		encoded, err := record.Encode(rec)
		if err != nil {
			return err
		}
		if _, err := stdout.Write(encoded); err != nil {
			return err
		}
	}
	stats := s.StatsView()
	replicas := make([]string, 0, len(stats))
	for replica := range stats {
		replicas = append(replicas, replica)
	}
	sort.Strings(replicas)
	for _, replica := range replicas {
		ids := make([]string, 0, len(stats[replica]))
		rows := make(map[string]string, len(stats[replica]))
		for id, row := range stats[replica] {
			ids = append(ids, id.String())
			rows[id.String()] = fmt.Sprintf("i:%d x:%d h:%d t:%d", row.I, row.X, row.H, row.T)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if _, err := fmt.Fprintf(stdout, "stat %s %s %s\n", replica, id, rows[id]); err != nil {
				return err
			}
		}
	}
	return nil
}
