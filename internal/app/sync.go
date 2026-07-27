package app

import (
	"errors"
	"fmt"
	"io"
	"sort"

	"meltcloud.io/dm/internal/core/record"
	"meltcloud.io/dm/internal/core/union"
	"meltcloud.io/dm/internal/core/view"
	"meltcloud.io/dm/internal/gitio"
	"meltcloud.io/dm/internal/store"
)

// EnvSkipFetch skips sync's initial fetch when set (non-empty). Debug/test
// residue in the §11.2 spirit: it lets the harness stage a genuinely stale
// mirror and drive the non-fast-forward retry path deterministically (the
// retry loop's own fetches always run — convergence is unaffected).
const EnvSkipFetch = "DM_SKIP_FETCH"

// SyncOptions carries sync's invocation knobs (cli resolves them from the
// environment).
type SyncOptions struct {
	SkipFetch bool
}

// currentView reads the store mirror's present tip ∪ pending in rec-id
// order — the view sync-time consumers (mint pass, health line) need,
// while the session's own mirror snapshot stays pre-fetch for the epoch
// rule (§8.4).
func (s *Session) currentView() ([]record.Record, error) {
	tip, err := s.Store.Tip()
	if err != nil {
		return nil, err
	}
	set, err := s.Store.Read(tip)
	if err != nil {
		return nil, err
	}
	view := make([]record.Record, 0, len(set.Records)+len(s.Pending))
	for id, data := range set.Records {
		rec, err := record.Decode(data)
		if err != nil {
			return nil, fmt.Errorf("store record %s: %w", id, err)
		}
		view = append(view, rec)
	}
	for _, rec := range s.Pending {
		if _, inStore := set.Records[rec.ID()]; !inStore {
			view = append(view, rec)
		}
	}
	sort.Slice(view, func(i, j int) bool { return view[i].ID().Less(view[j].ID()) })
	return view, nil
}

// mintForSync runs the load-bearing mint moment (§9.4): m1 plus the heavy
// matchers over the post-fetch store ∪ pending, right after fetch — while
// the author's clone holds both the local branch refs (the evidence) and
// the fresh target line, before forge branch auto-delete and GC destroy
// the evidence. Degraded clones never mint.
func (s *Session) mintForSync(pairs []forcedUpdate) error {
	if !s.Qualified {
		return nil
	}
	view, err := s.currentView()
	if err != nil {
		return err
	}
	ex, err := s.newExecutorFrom(view)
	if err != nil {
		return err
	}
	if err := ex.mintM1(); err != nil {
		return err
	}
	return ex.mintPass(pairs)
}

// healthLine computes the §8.4 sync footer: one line of worklist counts,
// session-scoped then global, over the post-sync state. The session scope
// is the work this sync shared (pending at open) — each backlog item
// lands in front of the session best equipped to judge it (§9.6).
func (s *Session) healthLine() (string, error) {
	recs, err := s.currentView()
	if err != nil {
		return "", err
	}
	ex, err := s.newExecutorFrom(recs)
	if err != nil {
		return "", err
	}
	data, err := ex.collectWorklist()
	if err != nil {
		return "", err
	}
	scope, err := ex.sessionScope()
	if err != nil {
		return "", err
	}
	scoped, elsewhere := healthTally(data, scope)
	return view.HealthLine(scoped, elsewhere), nil
}

// maxPushAttempts bounds the retry loop. Each retry only ever adds, so a
// lost race converges on the next attempt; hitting the bound means the
// remote is advancing faster than we can fetch, and erroring out beats
// spinning — the next sync starts where this one left off.
const maxPushAttempts = 5

// Sync is `dm sync` (§8.4), the one git-facing verb: fetch (refs/dm/store
// + code refs) → mint pass (§9.4: attempt landings while the evidence —
// local branch refs plus the fresh target line — is at hand; verdicts
// append to pending) → fold pending into a candidate store commit → CRDT
// union merge → push, with a fetch-merge-retry loop on a non-fast-forward
// race. Pending clears only on a successful push; a failed push degrades
// to fetch-only — the fetch and union merge have already applied locally,
// so a read-only-remote clone gets receive-only behavior plus an error
// line, losing nothing.
func Sync(dir string, det Determinism, opts SyncOptions, stdout, stderr io.Writer) error {
	s, err := OpenSession(dir, det, stderr)
	if err != nil {
		return err
	}
	defer s.Close()

	remote, err := shareRemote(s.Repo)
	if err != nil {
		return err
	}
	var pairs []forcedUpdate
	if remote != "" {
		// The refspec is normally init's job; re-ensure so a remote added
		// after init still syncs.
		if err := ensureRefspec(s.Repo, remote); err != nil {
			return err
		}
		if !opts.SkipFetch {
			// Remote-tracking rewrites across the fetch are the m1r
			// candidate pairs — they exist only at this moment (§9.4).
			before, err := s.Repo.RefSnapshot()
			if err != nil {
				return err
			}
			if err := s.Repo.Fetch(remote); err != nil {
				return fmt.Errorf("sync: fetch %s: %w", remote, err)
			}
			after, err := s.Repo.RefSnapshot()
			if err != nil {
				return err
			}
			if pairs, err = forcedUpdates(s.Repo, before, after); err != nil {
				return err
			}
		}
	}

	// Mint pass, over the post-fetch store ∪ pending — the fetched tip may
	// carry late-syncing origins; the session's own mirror stays pre-fetch
	// for the epoch rule below. Minted verdicts land in pending and ride
	// this same sync's push (§11.4 mint-pass-order).
	if err := s.mintForSync(pairs); err != nil {
		return err
	}

	// Own contribution: everything pending, as canonical bytes.
	own := make(map[record.RecID][]byte, len(s.Pending))
	ownIDs := make([]record.RecID, 0, len(s.Pending))
	for _, rec := range s.Pending {
		encoded, err := record.Encode(rec)
		if err != nil {
			return err
		}
		own[rec.ID()] = encoded
		ownIDs = append(ownIDs, rec.ID())
	}
	ownBlobs, err := s.Local.ScanPendingBlobs()
	if err != nil {
		return err
	}
	blobSrc := func(sha record.SHA) ([]byte, bool) {
		content, err := s.Local.ReadPendingBlob(sha)
		return content, err == nil
	}
	// The stat-delta accumulator folds into this replica's register file
	// (add counters, max t — §8.1) and, like pending records, clears only
	// on a successful push.
	ownStats := s.PendingStats

	var fetched union.Set
	var pushErr error
	pushed := false
	for attempt := 1; ; attempt++ {
		tip, err := s.Store.Tip()
		if err != nil {
			return err
		}
		if fetched, err = s.Store.Read(tip); err != nil {
			return err
		}
		// The epoch rule runs against the pre-fetch mirror (s.StoreSet):
		// under a newer fetched epoch the merge adopts the fetched tree
		// wholesale and only pending — own unpushed records — re-enters.
		merged := union.WithOwn(union.Merge(s.StoreSet, fetched), own, ownBlobs, s.Replica, ownStats)
		commit, err := s.Store.Commit(merged, tip, fetched, blobSrc, det.Now())
		if err != nil {
			return err
		}
		if tip != nil && commit == *tip {
			// Nothing to share — everything pending already landed (e.g. a
			// push whose clear was interrupted). The fetch already applied.
			pushed = true
			break
		}
		if remote != "" {
			pushErr = s.Repo.Push(remote, commit, store.Ref)
			if errors.Is(pushErr, gitio.ErrPushRejected) && attempt < maxPushAttempts {
				// Lost the race: re-fetch, re-merge (only ever adds), retry.
				fmt.Fprintf(stderr, "dm: sync: store moved under the push, retrying (attempt %d)\n", attempt+1)
				if err := s.Repo.Fetch(remote); err != nil {
					return fmt.Errorf("sync: fetch %s: %w", remote, err)
				}
				continue
			}
			if pushErr != nil {
				break
			}
		}
		// Landed (or remoteless: the local ref update is the landing).
		if err := s.Repo.UpdateRef(store.Ref, commit); err != nil {
			return err
		}
		pushed = true
		break
	}

	// Received = records the fetch brought in over the pre-fetch mirror.
	received := 0
	for id := range fetched.Records {
		if _, had := s.StoreSet.Records[id]; !had {
			received++
		}
	}
	health, healthErr := s.healthLine()
	if pushed {
		if err := s.Local.ClearPending(ownIDs, ownBlobs); err != nil {
			return err
		}
		if err := s.Local.ClearPendingStats(); err != nil {
			return err
		}
		s.PendingStats = union.Stats{} // never re-flushed at Close
		shared := 0
		for id := range own {
			if _, had := fetched.Records[id]; !had {
				shared++
			}
		}
		fmt.Fprintf(stdout, "pushed %d · received %d\n", shared, received)
		if healthErr != nil {
			return healthErr
		}
		// The backlog is surfaced at every share point, never demanded
		// (§8.4, §9.6 layer 4).
		fmt.Fprintln(stdout, health)
		return nil
	}
	// Fetch-only degradation (§8.4): the merge above already applied
	// locally via the fetched mirror; pending stays intact for the next
	// successful sync.
	fmt.Fprintf(stdout, "received %d · push failed — pending kept (%d)\n", received, len(ownIDs))
	if healthErr == nil {
		fmt.Fprintln(stdout, health)
	}
	return fmt.Errorf("sync: %w", pushErr)
}
