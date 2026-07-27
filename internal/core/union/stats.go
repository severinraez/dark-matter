package union

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"meltcloud.io/dm/internal/core/record"
)

// The stats register-file codec (§8.1): one row per entry that has stats,
// `<entry-id> i:<n> x:<n> h:<n> t:<ms>`. One codec shared by the store tree
// (stats/<replica-id>) and the clone-local pending accumulator
// (.git/.dm/pending/stats) — the file *is* the compacted form. Canonical
// form writes every field in that order, rows sorted by entry-id; the
// parser takes any subset (missing field = zero, §7.2).

// FormatStats serializes a register file canonically.
func FormatStats(stats Stats) []byte {
	ids := make([]string, 0, len(stats))
	byID := make(map[string]Row, len(stats))
	for id, row := range stats {
		ids = append(ids, id.String())
		byID[id.String()] = row
	}
	sort.Strings(ids)
	var b strings.Builder
	for _, id := range ids {
		row := byID[id]
		fmt.Fprintf(&b, "%s i:%d x:%d h:%d t:%d\n", id, row.I, row.X, row.H, row.T)
	}
	return []byte(b.String())
}

// ParseStats reads a register file.
func ParseStats(content []byte) (Stats, error) {
	stats := Stats{}
	for _, line := range strings.Split(string(content), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		id, err := record.ParseEntryID(fields[0])
		if err != nil {
			return nil, err
		}
		var row Row
		for _, f := range fields[1:] {
			key, val, ok := strings.Cut(f, ":")
			if !ok {
				return nil, fmt.Errorf("bad stat field %q", f)
			}
			n, err := strconv.ParseUint(val, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("bad stat field %q: %w", f, err)
			}
			switch key {
			case "i":
				row.I = n
			case "x":
				row.X = n
			case "h":
				row.H = n
			case "t":
				row.T = n
			default:
				return nil, fmt.Errorf("unknown stat field %q", f)
			}
		}
		stats[id] = row
	}
	return stats, nil
}
