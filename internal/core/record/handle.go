package record

import (
	"fmt"
	"strings"
)

// Handles (§3.5): a note's display reference, shown as #a3f9c1. Derived
// from identity, not position — the first handleLen chars of the entry-id's
// 16-char random tail (the leading 10 chars are wall-clock and would be
// shared by every id minted in the same ~4-minute window), lowercased for
// display. Extended by handleStep chars at mint time when the short form is
// already taken in store ∪ pending.

const (
	ulidTimeChars = 10 // leading Crockford chars encoding the timestamp
	ulidTailChars = 16 // random-tail chars (80 bits)
	handleLen     = 6  // default handle length (30 bits of entropy)
	handleStep    = 2  // chars added per collision extension
)

// Handle is the lowercase tail prefix, without the leading '#'.
type Handle string

// Tail returns the entry-id's full 16-char random tail, lowercased —
// the string handles are prefixes of and resolution sorts by (§8.2).
func (e EntryID) Tail() string {
	return strings.ToLower(e.String()[ulidTimeChars:])
}

// Handle returns the default (shortest) handle for the entry.
func (e EntryID) Handle() Handle { return Handle(e.Tail()[:handleLen]) }

// HandleCandidates returns the mint-time extension ladder: tail prefixes of
// length 6, 8, …, 16, shortest first.
func HandleCandidates(e EntryID) []Handle {
	tail := e.Tail()
	var out []Handle
	for n := handleLen; n <= ulidTailChars; n += handleStep {
		out = append(out, Handle(tail[:n]))
	}
	return out
}

// DeriveHandle picks the shortest candidate not already taken — write-time
// collision extension (§3.5). taken is queried against store ∪ pending.
// Exhausting the ladder means another entry shares the full 80-bit tail;
// vanishingly unlikely, surfaced as an error rather than papered over.
func DeriveHandle(e EntryID, taken func(Handle) bool) (Handle, error) {
	for _, h := range HandleCandidates(e) {
		if !taken(h) {
			return h, nil
		}
	}
	return "", fmt.Errorf("entry %s: every handle length is taken (full-tail collision)", e)
}
