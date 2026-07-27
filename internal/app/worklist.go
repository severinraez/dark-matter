package app

import (
	"io"

	"meltcloud.io/dm/internal/core/resolve"
	"meltcloud.io/dm/internal/core/view"
)

// Worklist is `dm worklist` (§9.6): everything that wants agent judgment —
// orphaned notes (rule (a) layer 5), ambiguous follows (splits, scatters,
// multi-candidate matches), and disputed notes — as a pure query over
// store ∪ pending and the current checkout; running it changes nothing.
// The rule-(b) groups (abandoned, grouped by line) arrive with M6; session
// scoping and the on-request stale/unconfirmed listing with M7.
func Worklist(dir string, det Determinism, notice io.Writer) (*view.WorklistReport, error) {
	s, err := OpenSession(dir, det, notice)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	ex, err := s.newExecutor()
	if err != nil {
		return nil, err
	}
	return ex.worklist()
}

// worklist classifies every landed, non-deleted entry and keeps the ones
// wanting judgment, in creation order (deterministic).
func (ex *executor) worklist() (*view.WorklistReport, error) {
	report := &view.WorklistReport{}
	for _, id := range ex.order {
		e, err := ex.fold(id)
		if err != nil {
			return nil, err
		}
		if e.Deleted || !e.Landed {
			continue
		}
		res, err := ex.resolution(id)
		if err != nil {
			return nil, err
		}
		var reasons []string
		runners := res.Runners
		switch res.State {
		case resolve.Orphaned, resolve.Scattered, resolve.Split:
			reasons = append(reasons, res.State.String())
		case resolve.Unconfirmed:
			if len(res.Runners) > 0 {
				// Multi-candidate match: dm picked by affinity, the agent
				// decides (§9.1 ambiguity policy). The item lists every
				// candidate, dm's pick first.
				reasons = append(reasons, "ambiguous")
				runners = append([]resolve.Candidate{{Path: res.Path, Score: res.Score}}, res.Runners...)
			}
		}
		if e.Disputed {
			reasons = append(reasons, "disputed")
		}
		if len(reasons) == 0 {
			continue
		}
		report.Items = append(report.Items, view.WorklistItem{
			Handle:  ex.handles[id],
			Subj:    e.Subj,
			Path:    e.Path,
			Reasons: reasons,
			Vote:    res.Vote,
			Runners: runners,
		})
	}
	return report, nil
}
