package app

import (
	"io"

	"meltcloud.io/dm/internal/core/record"
)

// Dump is `dm dump` (§11.3): the full raw state of store ∪ pending in a
// deterministic, un-budgeted, unranked form — the ground truth, bypassing
// view. Prints every record's canonical encoding in rec-id (fold) order;
// resolved-handle annotation and stat rows join as their milestones land.
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
	return nil
}
