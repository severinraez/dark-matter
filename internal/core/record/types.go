// Package record owns the record model: record types, the canonical codec,
// ULID minting rules, handle derivation + collision extension, and the
// shared value types (design.md §3.5, §8.3; architecture.md §6).
//
// The CRDT byte-identity invariant lives here: union dedup is byte
// identity, so every value has exactly one encoding and Encode/Decode
// round-trip byte-identically.
//
// Core imports no adapters and performs no I/O; the sole external import is
// oklog/ulid — a pure codec/arithmetic library whose entropy is injected as
// io.Reader (the plan's pinned dependency choice).
package record

import (
	"fmt"
	"strings"

	"github.com/oklog/ulid/v2"
)

// SHA is a git object name in lowercase hex (commit, tree, or blob).
type SHA string

// Valid reports whether s is plausible lowercase hex of abbreviated-to-full
// length. dm always writes full object names; the codec does not pin a
// length so fixtures and docs stay legible.
func (s SHA) Valid() bool {
	if len(s) < 4 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Path is a repo-relative slash path in raw (decoded) bytes. Folder paths
// carry a trailing slash; that suffix is what distinguishes a folder
// note's path key from a blob SHA in record fields.
type Path string

// IsFolder reports whether the path names a folder (trailing slash).
func (p Path) IsFolder() bool { return strings.HasSuffix(string(p), "/") }

// Subject is the kind of knowledge a note carries (§3.3). The enum is
// closed by design — no extensible fallback.
type Subject string

const (
	SubjCode Subject = "c" // behavior, edge cases, gotchas
	SubjArch Subject = "a" // cross-cutting design
	SubjDev  Subject = "d" // build, test, codegen
	SubjOps  Subject = "o" // deploy, logging, where secrets live
)

// Valid reports whether s is one of the closed subject enum.
func (s Subject) Valid() bool {
	switch s {
	case SubjCode, SubjArch, SubjDev, SubjOps:
		return true
	}
	return false
}

// Sig is an explicit feedback signal (§7.3).
type Sig string

const (
	SigUseful   Sig = "+"
	SigNotHere  Sig = "-"
	SigDisputed Sig = "!"
)

// Valid reports whether s is a known feedback signal.
func (s Sig) Valid() bool {
	switch s {
	case SigUseful, SigNotHere, SigDisputed:
		return true
	}
	return false
}

// Matcher is the evidence class stamped on a landing verdict (§9.4).
type Matcher string

const (
	MatcherReflog Matcher = "m1" // local reflog succession
	MatcherPaired Matcher = "m2" // full in-order segment pairing
	MatcherSquash Matcher = "m3" // cumulative patch-id squash match
	MatcherManual Matcher = "m5" // manual vd disposition
)

// Valid reports whether m is a known matcher id.
func (m Matcher) Valid() bool {
	switch m {
	case MatcherReflog, MatcherPaired, MatcherSquash, MatcherManual:
		return true
	}
	return false
}

// RecID is a per-record ULID (§8.3): union-merge key, LWW timestamp, and
// deterministic tiebreak in one field. Minted monotonically per process.
type RecID ulid.ULID

// EntryID is a logical entry's ULID, minted with fresh randomness so its
// tail keeps full entropy for handle derivation (§3.5).
type EntryID ulid.ULID

// String returns the canonical 26-char uppercase Crockford form.
func (r RecID) String() string { return ulid.ULID(r).String() }

// String returns the canonical 26-char uppercase Crockford form.
func (e EntryID) String() string { return ulid.ULID(e).String() }

// Compare returns -1, 0, or 1; ULID byte order is creation-time order with
// the random tail as deterministic tiebreak — the LWW total order.
func (r RecID) Compare(o RecID) int { return ulid.ULID(r).Compare(ulid.ULID(o)) }

// Less reports strict LWW order.
func (r RecID) Less(o RecID) bool { return r.Compare(o) < 0 }

// Time returns the embedded creation time in unix milliseconds.
func (r RecID) Time() uint64 { return ulid.ULID(r).Time() }

// IsZero reports the zero (unminted) id.
func (r RecID) IsZero() bool { return ulid.ULID(r) == ulid.ULID{} }

// IsZero reports the zero (unminted) id.
func (e EntryID) IsZero() bool { return ulid.ULID(e) == ulid.ULID{} }

// ParseRecID parses the canonical form; anything but the exact canonical
// encoding is an error (byte-identity invariant).
func ParseRecID(s string) (RecID, error) {
	u, err := parseCanonicalULID(s)
	return RecID(u), err
}

// ParseEntryID parses the canonical form, strictly likewise.
func ParseEntryID(s string) (EntryID, error) {
	u, err := parseCanonicalULID(s)
	return EntryID(u), err
}

func parseCanonicalULID(s string) (ulid.ULID, error) {
	u, err := ulid.ParseStrict(s)
	if err != nil {
		return ulid.ULID{}, fmt.Errorf("invalid ULID %q: %w", s, err)
	}
	if u.String() != s {
		return ulid.ULID{}, fmt.Errorf("non-canonical ULID %q (canonical is %s)", s, u.String())
	}
	return u, nil
}

// Anchor is a note's content half (§3.1): the blob SHA of the described
// bytes for a file note, or the path key for a folder note. Exactly one
// side is set.
type Anchor struct {
	Blob    SHA  // file note: content key
	PathKey Path // folder note: path key, always ending "/"
}

// BlobAnchor returns a file-note anchor.
func BlobAnchor(blob SHA) Anchor { return Anchor{Blob: blob} }

// PathAnchor returns a folder-note anchor.
func PathAnchor(key Path) Anchor { return Anchor{PathKey: key} }

// IsPath reports a folder-note (path key) anchor.
func (a Anchor) IsPath() bool { return a.PathKey != "" }

// Type tags a record's kind — the two-letter prefix of its encoding (§8.3).
type Type string

const (
	TypeCreate    Type = "CR"
	TypeSupersede Type = "SU"
	TypeTombstone Type = "TB"
	TypeReAnchor  Type = "RA"
	TypeRePath    Type = "RP"
	TypeLink      Type = "LN"
	TypeUnlink    Type = "UL"
	TypeFeedback  Type = "FB"
	TypeVerdict   Type = "VD"
)

// Record is any store record. Concrete types are the nine §8.3 structs.
type Record interface {
	// Type returns the record's kind tag.
	Type() Type
	// ID returns the record's rec-id (union key, LWW order).
	ID() RecID
}

// Create is CR: mints a logical entry, stamps both anchors (§6.1).
type Create struct {
	Rec    RecID
	Entry  EntryID
	Subj   Subject
	Anchor Anchor
	Origin SHA // HEAD commit at write time — rule (b)
	Path   Path
	Body   string
}

// Supersede is SU: a new body revision, anchors re-stamped (§6.1).
type Supersede struct {
	Rec    RecID
	Entry  EntryID
	Subj   Subject
	Anchor Anchor
	Origin SHA
	Path   Path
	Body   string
}

// Tombstone is TB: absorbing delete, no origin, terminal everywhere (§8.3).
type Tombstone struct {
	Rec   RecID
	Entry EntryID
}

// ReAnchor is RA: re-stamps the anchors with no new body revision (§9.5).
type ReAnchor struct {
	Rec    RecID
	Entry  EntryID
	Anchor Anchor
	Origin SHA
	Path   Path
}

// RePath is RP: a new path hint only, scoped by its own origin; the content
// anchor is untouched — relocate, never bless (§6.5).
type RePath struct {
	Rec    RecID
	Entry  EntryID
	Origin SHA
	Path   Path
}

// Link is LN: a directed link between two entries, optional comment (§6.4).
type Link struct {
	Rec     RecID
	From    EntryID
	To      EntryID
	Comment string // optional; "" = none
}

// Unlink is UL: removes a link; the pair's current state is LWW (§6.4).
type Unlink struct {
	Rec  RecID
	From EntryID
	To   EntryID
}

// Feedback is FB: an explicit agent signal with optional reason (§7.3).
type Feedback struct {
	Rec    RecID
	Entry  EntryID
	Sig    Sig
	Reason string // optional; "" = none
}

// Verdict is VD: a memoized rule-(b) landing binding per origin, or its
// manual unlanded voiding; largest rec-id wins per origin (§9.4).
type Verdict struct {
	Rec      RecID
	Origin   SHA
	Landed   bool    // false = unlanded (voids a wrong binding)
	LandedAs SHA     // set iff Landed
	Matcher  Matcher // set iff Landed; evidence class for auditability
}

func (r Create) Type() Type    { return TypeCreate }
func (r Supersede) Type() Type { return TypeSupersede }
func (r Tombstone) Type() Type { return TypeTombstone }
func (r ReAnchor) Type() Type  { return TypeReAnchor }
func (r RePath) Type() Type    { return TypeRePath }
func (r Link) Type() Type      { return TypeLink }
func (r Unlink) Type() Type    { return TypeUnlink }
func (r Feedback) Type() Type  { return TypeFeedback }
func (r Verdict) Type() Type   { return TypeVerdict }

func (r Create) ID() RecID    { return r.Rec }
func (r Supersede) ID() RecID { return r.Rec }
func (r Tombstone) ID() RecID { return r.Rec }
func (r ReAnchor) ID() RecID  { return r.Rec }
func (r RePath) ID() RecID    { return r.Rec }
func (r Link) ID() RecID      { return r.Rec }
func (r Unlink) ID() RecID    { return r.Rec }
func (r Feedback) ID() RecID  { return r.Rec }
func (r Verdict) ID() RecID   { return r.Rec }
