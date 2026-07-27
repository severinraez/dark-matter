package record

import (
	"errors"
	"fmt"
	"strings"
)

// The canonical codec (§8.3). One record encodes to exactly one byte
// sequence — union dedup is byte identity, so canonicality is a CRDT
// invariant, enforced on both encode and decode:
//
//   - fields are separated by a single space, fixed fields first, the
//     free-text field (body / reason / comment) last and raw — `:`, spaces
//     and backticks in it need no escaping;
//   - path-valued fixed fields carry the §4.1/§8.3 percent-encoding canon
//     (pathenc.go);
//   - the record text is line-framed exactly like stdin batches: a
//     physical line's trailing run of n backslashes decodes to n/2 literal
//     backslashes, and an odd run continues the record onto the next line
//     (the continuation is the encoding of a newline). Only bodies can
//     contain newlines, but the frame applies to the whole record so a
//     path ending in a raw backslash cannot fake a continuation;
//   - the encoding ends with exactly one newline.

const (
	vdLanded   = "landed"
	vdUnlanded = "unlanded"
)

// Encode renders a record in the canonical form, validating every field.
func Encode(r Record) ([]byte, error) {
	logical, err := encodeLogical(r)
	if err != nil {
		return nil, fmt.Errorf("encoding %s %s: %w", r.Type(), r.ID(), err)
	}
	return frame(logical), nil
}

// Decode parses one canonically-encoded record; any deviation from the
// canonical form is an error.
func Decode(data []byte) (Record, error) {
	logical, err := unframe(data)
	if err != nil {
		return nil, err
	}
	typ, rest, ok := strings.Cut(logical, " ")
	if !ok {
		return nil, fmt.Errorf("record %q: missing fields", typ)
	}
	var r Record
	switch Type(typ) {
	case TypeCreate:
		r, err = decodeNote(rest, true)
	case TypeSupersede:
		r, err = decodeNote(rest, false)
	case TypeTombstone:
		r, err = decodeTombstone(rest)
	case TypeReAnchor:
		r, err = decodeReAnchor(rest)
	case TypeRePath:
		r, err = decodeRePath(rest)
	case TypeLink:
		r, err = decodeLink(rest)
	case TypeUnlink:
		r, err = decodeUnlink(rest)
	case TypeFeedback:
		r, err = decodeFeedback(rest)
	case TypeVerdict:
		r, err = decodeVerdict(rest)
	default:
		return nil, fmt.Errorf("unknown record type %q", typ)
	}
	if err != nil {
		return nil, fmt.Errorf("decoding %s record: %w", typ, err)
	}
	return r, nil
}

// ---- logical text (fields joined, body raw, real newlines) ----

func encodeLogical(r Record) (string, error) {
	if r.ID().IsZero() {
		return "", errors.New("zero rec-id")
	}
	switch v := r.(type) {
	case Create:
		return encodeNote(TypeCreate, v.Rec, v.Entry, v.Subj, v.Anchor, v.Origin, v.Path, v.Body)
	case Supersede:
		return encodeNote(TypeSupersede, v.Rec, v.Entry, v.Subj, v.Anchor, v.Origin, v.Path, v.Body)
	case Tombstone:
		if err := checkEntry(v.Entry); err != nil {
			return "", err
		}
		return join(string(TypeTombstone), v.Rec.String(), v.Entry.String()), nil
	case ReAnchor:
		if err := errs(checkEntry(v.Entry), checkAnchor(v.Anchor), checkSHA("origin", v.Origin), checkPath(v.Path)); err != nil {
			return "", err
		}
		return join(string(TypeReAnchor), v.Rec.String(), v.Entry.String(), encodeAnchor(v.Anchor), string(v.Origin), EncodePath(v.Path)), nil
	case RePath:
		if err := errs(checkEntry(v.Entry), checkSHA("origin", v.Origin), checkPath(v.Path)); err != nil {
			return "", err
		}
		return join(string(TypeRePath), v.Rec.String(), v.Entry.String(), string(v.Origin), EncodePath(v.Path)), nil
	case Link:
		if err := errs(checkID("from-id", v.From), checkID("to-id", v.To), checkOptText("comment", v.Comment)); err != nil {
			return "", err
		}
		s := join(string(TypeLink), v.Rec.String(), v.From.String(), v.To.String())
		if v.Comment != "" {
			s = join(s, v.Comment)
		}
		return s, nil
	case Unlink:
		if err := errs(checkID("from-id", v.From), checkID("to-id", v.To)); err != nil {
			return "", err
		}
		return join(string(TypeUnlink), v.Rec.String(), v.From.String(), v.To.String()), nil
	case Feedback:
		if err := checkEntry(v.Entry); err != nil {
			return "", err
		}
		if !v.Sig.Valid() {
			return "", fmt.Errorf("invalid feedback signal %q", v.Sig)
		}
		if err := checkOptText("reason", v.Reason); err != nil {
			return "", err
		}
		s := join(string(TypeFeedback), v.Rec.String(), v.Entry.String(), string(v.Sig))
		if v.Reason != "" {
			s = join(s, v.Reason)
		}
		return s, nil
	case Verdict:
		if err := checkSHA("origin", v.Origin); err != nil {
			return "", err
		}
		if !v.Landed {
			if v.LandedAs != "" || v.Matcher != "" {
				return "", errors.New("unlanded verdict carries no landed-as or matcher")
			}
			return join(string(TypeVerdict), v.Rec.String(), string(v.Origin), vdUnlanded), nil
		}
		if err := checkSHA("landed-as", v.LandedAs); err != nil {
			return "", err
		}
		if !v.Matcher.Valid() {
			return "", fmt.Errorf("invalid matcher %q", v.Matcher)
		}
		return join(string(TypeVerdict), v.Rec.String(), string(v.Origin), vdLanded, string(v.LandedAs), string(v.Matcher)), nil
	default:
		return "", fmt.Errorf("unknown record type %T", r)
	}
}

func encodeNote(t Type, rec RecID, entry EntryID, subj Subject, anchor Anchor, origin SHA, path Path, body string) (string, error) {
	if err := errs(checkEntry(entry), checkAnchor(anchor), checkSHA("origin", origin), checkPath(path)); err != nil {
		return "", err
	}
	if !subj.Valid() {
		return "", fmt.Errorf("invalid subject %q", subj)
	}
	if body == "" {
		return "", errors.New("empty body")
	}
	if err := ValidateBody(body); err != nil {
		return "", err
	}
	return join(string(t), rec.String(), entry.String(), string(subj), encodeAnchor(anchor), string(origin), EncodePath(path), body), nil
}

func decodeNote(rest string, create bool) (Record, error) {
	f := strings.SplitN(rest, " ", 7)
	if len(f) != 7 {
		return nil, errors.New("want <rec-id> <entry-id> <subj> <anchor> <origin> <path> <body>")
	}
	rec, entry, err := decodeIDPair(f[0], f[1])
	if err != nil {
		return nil, err
	}
	subj := Subject(f[2])
	if !subj.Valid() {
		return nil, fmt.Errorf("invalid subject %q", subj)
	}
	anchor, err := decodeAnchor(f[3])
	if err != nil {
		return nil, err
	}
	origin := SHA(f[4])
	if !origin.Valid() {
		return nil, fmt.Errorf("invalid origin %q", origin)
	}
	path, err := decodeCanonicalPath(f[5])
	if err != nil {
		return nil, err
	}
	body := f[6]
	if body == "" {
		return nil, errors.New("empty body")
	}
	if err := ValidateBody(body); err != nil {
		return nil, err
	}
	if create {
		return Create{rec, entry, subj, anchor, origin, path, body}, nil
	}
	return Supersede{rec, entry, subj, anchor, origin, path, body}, nil
}

func decodeTombstone(rest string) (Record, error) {
	f := strings.Split(rest, " ")
	if len(f) != 2 {
		return nil, errors.New("want <rec-id> <entry-id>")
	}
	rec, entry, err := decodeIDPair(f[0], f[1])
	if err != nil {
		return nil, err
	}
	return Tombstone{rec, entry}, nil
}

func decodeReAnchor(rest string) (Record, error) {
	f := strings.Split(rest, " ")
	if len(f) != 5 {
		return nil, errors.New("want <rec-id> <entry-id> <anchor> <origin> <path>")
	}
	rec, entry, err := decodeIDPair(f[0], f[1])
	if err != nil {
		return nil, err
	}
	anchor, err := decodeAnchor(f[2])
	if err != nil {
		return nil, err
	}
	origin := SHA(f[3])
	if !origin.Valid() {
		return nil, fmt.Errorf("invalid origin %q", origin)
	}
	path, err := decodeCanonicalPath(f[4])
	if err != nil {
		return nil, err
	}
	return ReAnchor{rec, entry, anchor, origin, path}, nil
}

func decodeRePath(rest string) (Record, error) {
	f := strings.Split(rest, " ")
	if len(f) != 4 {
		return nil, errors.New("want <rec-id> <entry-id> <origin> <path>")
	}
	rec, entry, err := decodeIDPair(f[0], f[1])
	if err != nil {
		return nil, err
	}
	origin := SHA(f[2])
	if !origin.Valid() {
		return nil, fmt.Errorf("invalid origin %q", origin)
	}
	path, err := decodeCanonicalPath(f[3])
	if err != nil {
		return nil, err
	}
	return RePath{rec, entry, origin, path}, nil
}

func decodeLink(rest string) (Record, error) {
	f := strings.SplitN(rest, " ", 4)
	if len(f) < 3 {
		return nil, errors.New("want <rec-id> <from-id> <to-id> [comment]")
	}
	rec, err := ParseRecID(f[0])
	if err != nil {
		return nil, err
	}
	from, err := ParseEntryID(f[1])
	if err != nil {
		return nil, err
	}
	to, err := ParseEntryID(f[2])
	if err != nil {
		return nil, err
	}
	var comment string
	if len(f) == 4 {
		comment = f[3]
		if err := checkOptText("comment", comment); err != nil {
			return nil, err
		}
		if comment == "" {
			return nil, errors.New("dangling separator: empty comment")
		}
	}
	return Link{rec, from, to, comment}, nil
}

func decodeUnlink(rest string) (Record, error) {
	f := strings.Split(rest, " ")
	if len(f) != 3 {
		return nil, errors.New("want <rec-id> <from-id> <to-id>")
	}
	rec, err := ParseRecID(f[0])
	if err != nil {
		return nil, err
	}
	from, err := ParseEntryID(f[1])
	if err != nil {
		return nil, err
	}
	to, err := ParseEntryID(f[2])
	if err != nil {
		return nil, err
	}
	return Unlink{rec, from, to}, nil
}

func decodeFeedback(rest string) (Record, error) {
	f := strings.SplitN(rest, " ", 4)
	if len(f) < 3 {
		return nil, errors.New("want <rec-id> <entry-id> <sig> [reason]")
	}
	rec, entry, err := decodeIDPair(f[0], f[1])
	if err != nil {
		return nil, err
	}
	sig := Sig(f[2])
	if !sig.Valid() {
		return nil, fmt.Errorf("invalid feedback signal %q", sig)
	}
	var reason string
	if len(f) == 4 {
		reason = f[3]
		if err := checkOptText("reason", reason); err != nil {
			return nil, err
		}
		if reason == "" {
			return nil, errors.New("dangling separator: empty reason")
		}
	}
	return Feedback{rec, entry, sig, reason}, nil
}

func decodeVerdict(rest string) (Record, error) {
	f := strings.Split(rest, " ")
	if len(f) < 3 {
		return nil, errors.New("want <rec-id> <origin> landed <landed-as> <matcher> | <rec-id> <origin> unlanded")
	}
	rec, err := ParseRecID(f[0])
	if err != nil {
		return nil, err
	}
	origin := SHA(f[1])
	if !origin.Valid() {
		return nil, fmt.Errorf("invalid origin %q", origin)
	}
	switch f[2] {
	case vdUnlanded:
		if len(f) != 3 {
			return nil, errors.New("unlanded verdict carries no further fields")
		}
		return Verdict{Rec: rec, Origin: origin}, nil
	case vdLanded:
		if len(f) != 5 {
			return nil, errors.New("want <landed-as> <matcher> after landed")
		}
		landedAs := SHA(f[3])
		if !landedAs.Valid() {
			return nil, fmt.Errorf("invalid landed-as %q", landedAs)
		}
		matcher := Matcher(f[4])
		if !matcher.Valid() {
			return nil, fmt.Errorf("invalid matcher %q", matcher)
		}
		return Verdict{Rec: rec, Origin: origin, Landed: true, LandedAs: landedAs, Matcher: matcher}, nil
	default:
		return nil, fmt.Errorf("want landed or unlanded, got %q", f[2])
	}
}

// ---- field helpers ----

func encodeAnchor(a Anchor) string {
	if a.IsPath() {
		return EncodePath(a.PathKey)
	}
	return string(a.Blob)
}

// decodeAnchor disambiguates by shape: folder path keys always end in a
// raw "/" ("/" is never percent-encoded); anything else must be a blob SHA.
func decodeAnchor(field string) (Anchor, error) {
	if strings.HasSuffix(field, "/") {
		p, err := decodeCanonicalPath(field)
		if err != nil {
			return Anchor{}, fmt.Errorf("anchor: %w", err)
		}
		return PathAnchor(p), nil
	}
	blob := SHA(field)
	if !blob.Valid() {
		return Anchor{}, fmt.Errorf("invalid anchor %q (neither blob SHA nor folder path key)", field)
	}
	return BlobAnchor(blob), nil
}

func decodeIDPair(recField, entryField string) (RecID, EntryID, error) {
	rec, err := ParseRecID(recField)
	if err != nil {
		return RecID{}, EntryID{}, err
	}
	entry, err := ParseEntryID(entryField)
	if err != nil {
		return RecID{}, EntryID{}, err
	}
	return rec, entry, nil
}

func checkEntry(e EntryID) error {
	return checkID("entry-id", e)
}

func checkID(name string, e EntryID) error {
	if e.IsZero() {
		return fmt.Errorf("zero %s", name)
	}
	return nil
}

func checkSHA(name string, s SHA) error {
	if !s.Valid() {
		return fmt.Errorf("invalid %s %q", name, s)
	}
	return nil
}

func checkPath(p Path) error {
	if p == "" {
		return errors.New("empty path")
	}
	return nil
}

func checkAnchor(a Anchor) error {
	if a.IsPath() {
		if a.Blob != "" {
			return errors.New("anchor sets both blob and path key")
		}
		if !a.PathKey.IsFolder() {
			return fmt.Errorf("folder path key %q must end in /", a.PathKey)
		}
		return nil
	}
	return checkSHA("anchor blob", a.Blob)
}

// checkOptText validates an optional trailing free-text field (reason,
// comment); empty means absent. Same byte rules as bodies — trailing
// fields share the body-last grammar (§8.3), including `\` continuation.
func checkOptText(name, s string) error {
	if s == "" {
		return nil
	}
	if err := ValidateBody(s); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func errs(es ...error) error {
	for _, e := range es {
		if e != nil {
			return e
		}
	}
	return nil
}

func join(fields ...string) string { return strings.Join(fields, " ") }

// ---- line framing ----

// frame renders logical record text as physical lines: trailing backslash
// runs double, a single appended "\" continues a record across a newline,
// and the encoding ends with exactly one newline.
func frame(logical string) []byte {
	lines := strings.Split(logical, "\n")
	var b strings.Builder
	for i, ln := range lines {
		n := trailingBackslashes(ln)
		b.WriteString(ln[:len(ln)-n])
		for j := 0; j < 2*n; j++ {
			b.WriteByte('\\')
		}
		if i < len(lines)-1 {
			b.WriteString("\\\n")
		} else {
			b.WriteByte('\n')
		}
	}
	return []byte(b.String())
}

// unframe reverses frame, strictly: exactly one record, terminated, no
// trailing garbage.
func unframe(data []byte) (string, error) {
	s := string(data)
	if !strings.HasSuffix(s, "\n") {
		return "", errors.New("record does not end in a newline")
	}
	phys := strings.Split(s[:len(s)-1], "\n")
	var b strings.Builder
	for i, ln := range phys {
		n := trailingBackslashes(ln)
		b.WriteString(ln[:len(ln)-n])
		for j := 0; j < n/2; j++ {
			b.WriteByte('\\')
		}
		if n%2 == 1 {
			if i == len(phys)-1 {
				return "", errors.New("record ends mid-continuation")
			}
			b.WriteByte('\n')
		} else if i != len(phys)-1 {
			return "", errors.New("content after record end")
		}
	}
	return b.String(), nil
}

func trailingBackslashes(s string) int {
	n := 0
	for n < len(s) && s[len(s)-1-n] == '\\' {
		n++
	}
	return n
}
