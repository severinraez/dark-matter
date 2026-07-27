package app

import (
	"crypto/rand"
	"fmt"
	"io"

	"meltcloud.io/dm/internal/core/union"
	"meltcloud.io/dm/internal/gitio"
	"meltcloud.io/dm/internal/local"
	"meltcloud.io/dm/internal/store"
)

// Init is `dm init` (§10.1, §8.4 row 1): create .git/.dm/ (replica id,
// pending, cache) once per clone, configure the refs/dm/* fetch refspec,
// and fetch or create the store branch. No Session — there is nothing to
// lock or snapshot yet. Idempotent throughout.
func Init(dir string, det Determinism, stdout io.Writer) error {
	repo, err := gitio.Open(dir)
	if err != nil {
		return err
	}
	id := det.ReplicaID
	if id == "" {
		id, err = mintReplicaID()
		if err != nil {
			return err
		}
	}
	existing, id, err := local.At(repo.GitDir).Init(id)
	if err != nil {
		return err
	}
	if existing {
		fmt.Fprintf(stdout, "already initialized (replica %s)\n", id)
	} else {
		fmt.Fprintf(stdout, "initialized %s (replica %s)\n", ".git/.dm", id)
	}

	remote, err := shareRemote(repo)
	if err != nil {
		return err
	}
	if remote != "" {
		if err := ensureRefspec(repo, remote); err != nil {
			return err
		}
	}
	st := &store.Store{Repo: repo}
	tip, err := st.Tip()
	if err != nil {
		return err
	}
	if tip == nil && remote != "" {
		// Fetch the store if the remote already has one — probing first,
		// because fetching a nonexistent ref is a hard git error.
		remoteTip, err := repo.LsRemote(remote, store.Ref)
		if err != nil {
			return err
		}
		if remoteTip != nil {
			if err := repo.Fetch(remote); err != nil {
				return err
			}
			tip, err = st.Tip()
			if err != nil {
				return err
			}
			if tip != nil {
				fmt.Fprintf(stdout, "store fetched from %s\n", remote)
			}
		}
	}
	if tip == nil {
		// No store anywhere yet: create the empty branch at epoch 0. The
		// first sync pushes it; a lost creation race just resolves at
		// fetch time (the forced refspec mirrors whatever landed first).
		commit, err := st.Commit(union.NewSet(), nil, union.NewSet(), nil, det.Now())
		if err != nil {
			return err
		}
		if err := repo.UpdateRef(store.Ref, commit); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "store created (empty)")
	}
	return nil
}

// shareRemote picks the remote sync talks to: origin, else the single
// configured remote, else none (a remoteless clone syncs locally, §8.4).
func shareRemote(repo *gitio.Repo) (string, error) {
	remotes, err := repo.Remotes()
	if err != nil {
		return "", err
	}
	for _, r := range remotes {
		if r == "origin" {
			return "origin", nil
		}
	}
	if len(remotes) == 1 {
		return remotes[0], nil
	}
	return "", nil
}

// ensureRefspec adds the forced refs/dm/* fetch refspec once (§8.1);
// custom refs push and fetch fine through forges, and the force flag lets
// a compaction's rewritten branch mirror cleanly (§8.7).
func ensureRefspec(repo *gitio.Repo, remote string) error {
	key := "remote." + remote + ".fetch"
	specs, err := repo.ConfigValues(key)
	if err != nil {
		return err
	}
	for _, s := range specs {
		if s == store.Refspec {
			return nil
		}
	}
	return repo.ConfigAdd(key, store.Refspec)
}

// crockford is the base32 alphabet ULIDs use, reused for the replica id
// (plan: one codec for handles and replica ids).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// mintReplicaID spends the bytes rather than fighting collisions (§8.5):
// 8 Crockford base32 chars (40 bits) from a CSPRNG, no registry, no
// coordination.
func mintReplicaID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("minting replica id: %w", err)
	}
	id := make([]byte, 8)
	for i, b := range raw {
		id[i] = crockford[int(b)%len(crockford)]
	}
	return string(id), nil
}
