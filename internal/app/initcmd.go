package app

import (
	"crypto/rand"
	"fmt"
	"io"

	"meltcloud.io/dm/internal/gitio"
	"meltcloud.io/dm/internal/local"
)

// Init is `dm init` (§10.1): create .git/.dm/ (replica id, pending, cache)
// once per clone. No Session — there is nothing to lock or snapshot yet.
// Configuring the refs/dm/* refspec and fetching or creating the store
// branch arrive with M4 (M2 has no store branch: pending alone carries the
// slice).
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
	return nil
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
