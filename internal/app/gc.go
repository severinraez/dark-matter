package app

import (
	"errors"
	"fmt"
	"io"

	"meltcloud.io/dm/internal/core/union"
	"meltcloud.io/dm/internal/gitio"
	"meltcloud.io/dm/internal/store"
)

// GCOptions carries gc's invocation knobs (cli resolves them from the
// environment).
type GCOptions struct {
	SkipFetch bool
}

// GC is `dm gc` (§8.7): fetch → deterministic mark-and-sweep (union.Sweep)
// → orphan re-commit of the slimmed tip tree with epoch bumped by one →
// compare-and-swap push, always --force-with-lease on the fetched tip. An
// orphan tip is never a fast-forward, and plain --force could overwrite a
// concurrently-landed sync whose pusher has already cleared pending —
// records that would then exist nowhere. A lease rejection re-enters the
// same fetch → re-sweep → re-push loop as sync's retry, absorbing a normal
// sync landing mid-compaction. Any replica may run it; there is no
// coordinator, and the trigger is manual only (v1).
//
// Pending is untouched: the compactor can never see other replicas'
// pending, and its own lands at the next sync — into the new epoch,
// dangling references and all (ignored at read, swept next pass).
func GC(dir string, det Determinism, opts GCOptions, stdout, stderr io.Writer) error {
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
		if err := ensureRefspec(s.Repo, remote); err != nil {
			return err
		}
		if !opts.SkipFetch {
			if err := s.Repo.Fetch(remote); err != nil {
				return fmt.Errorf("gc: fetch %s: %w", remote, err)
			}
		}
	}

	for attempt := 1; ; attempt++ {
		tip, err := s.Store.Tip()
		if err != nil {
			return err
		}
		if tip == nil {
			fmt.Fprintln(stdout, "nothing to compact — no store yet")
			return nil
		}
		cur, err := s.Store.Read(tip)
		if err != nil {
			return err
		}
		swept, dropped, err := union.Sweep(cur, det.Now())
		if err != nil {
			return err
		}
		blobSrc, err := s.Store.TipBlobSource(*tip)
		if err != nil {
			return err
		}
		commit, err := s.Store.Compact(swept, blobSrc, det.Now())
		if err != nil {
			return err
		}
		if remote != "" {
			pushErr := s.Repo.PushLease(remote, commit, store.Ref, *tip)
			if errors.Is(pushErr, gitio.ErrPushRejected) && attempt < maxPushAttempts {
				// Lost the CAS — something landed in the fetch→sweep→push
				// window. Refetch and re-sweep so it is carried, not lost.
				fmt.Fprintf(stderr, "dm: gc: store moved under the push, retrying (attempt %d)\n", attempt+1)
				if err := s.Repo.Fetch(remote); err != nil {
					return fmt.Errorf("gc: fetch %s: %w", remote, err)
				}
				continue
			}
			if pushErr != nil {
				return fmt.Errorf("gc: push: %w", pushErr)
			}
		}
		// The local mirror follows the landing (remoteless: the local ref
		// update is the landing).
		if err := s.Repo.UpdateRef(store.Ref, commit); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "compacted: dropped %d records · %d blobs · epoch %d\n",
			dropped.Records, dropped.Blobs, swept.Epoch)
		return nil
	}
}
