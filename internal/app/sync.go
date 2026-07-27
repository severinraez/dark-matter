package app

import (
	"errors"
	"fmt"
	"io"

	"meltcloud.io/dm/internal/core/record"
	"meltcloud.io/dm/internal/core/union"
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

// maxPushAttempts bounds the retry loop. Each retry only ever adds, so a
// lost race converges on the next attempt; hitting the bound means the
// remote is advancing faster than we can fetch, and erroring out beats
// spinning — the next sync starts where this one left off.
const maxPushAttempts = 5

// Sync is `dm sync` (§8.4), the one git-facing verb: fetch (refs/dm/store
// + code refs) → fold pending into a candidate store commit → CRDT union
// merge → push, with a fetch-merge-retry loop on a non-fast-forward race.
// Pending clears only on a successful push; a failed push degrades to
// fetch-only — the fetch and union merge have already applied locally, so
// a read-only-remote clone gets receive-only behavior plus an error line,
// losing nothing. (The mint pass joins in M6, between fetch and fold.)
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
	if remote != "" {
		// The refspec is normally init's job; re-ensure so a remote added
		// after init still syncs.
		if err := ensureRefspec(s.Repo, remote); err != nil {
			return err
		}
		if !opts.SkipFetch {
			if err := s.Repo.Fetch(remote); err != nil {
				return fmt.Errorf("sync: fetch %s: %w", remote, err)
			}
		}
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
		merged := union.WithOwn(union.Merge(s.StoreSet, fetched), own, ownBlobs)
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
	if pushed {
		if err := s.Local.ClearPending(ownIDs, ownBlobs); err != nil {
			return err
		}
		shared := 0
		for id := range own {
			if _, had := fetched.Records[id]; !had {
				shared++
			}
		}
		// Placeholder summary until the §9.6 health line lands (M7).
		fmt.Fprintf(stdout, "pushed %d · received %d\n", shared, received)
		return nil
	}
	// Fetch-only degradation (§8.4): the merge above already applied
	// locally via the fetched mirror; pending stays intact for the next
	// successful sync.
	fmt.Fprintf(stdout, "received %d · push failed — pending kept (%d)\n", received, len(ownIDs))
	return fmt.Errorf("sync: %w", pushErr)
}
