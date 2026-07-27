package record

import (
	"fmt"
	"strings"
)

// The percent-encoding canon (§4.1/§8.3): exactly `%` `:` space and C0
// bytes encode, every other byte stays raw. One canon shared by record
// fields, CLI path fields, and all dm output. Canonical — always this set,
// never more — is load-bearing: union dedup is byte identity, so any path
// must have exactly one encoding. Deliberately hand-rolled: net/url
// encodes a different set.

const hexUpper = "0123456789ABCDEF"

func pathByteNeedsEscape(c byte) bool {
	return c == '%' || c == ':' || c == ' ' || c < 0x20
}

// EncodePath renders a raw path in the canonical percent encoding.
func EncodePath(p Path) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		if pathByteNeedsEscape(c) {
			b.WriteByte('%')
			b.WriteByte(hexUpper[c>>4])
			b.WriteByte(hexUpper[c&0x0F])
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// DecodePath decodes a percent-encoded path strictly: a `%` not followed by
// two hex digits is an error (§4.1 — a forgotten escape fails loudly
// instead of naming the wrong file). Any valid %XX escape is accepted;
// canonicality of stored fields is enforced separately by the codec.
func DecodePath(s string) (Path, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '%' {
			b.WriteByte(c)
			continue
		}
		if i+2 >= len(s) {
			return "", fmt.Errorf(`path %q: "%%" not followed by two hex digits`, s)
		}
		hi, ok1 := hexVal(s[i+1])
		lo, ok2 := hexVal(s[i+2])
		if !ok1 || !ok2 {
			return "", fmt.Errorf(`path %q: "%%" not followed by two hex digits`, s)
		}
		b.WriteByte(hi<<4 | lo)
		i += 2
	}
	return Path(b.String()), nil
}

// decodeCanonicalPath decodes a stored path field and enforces the
// canonical encoding: re-encoding must reproduce the field byte-for-byte.
func decodeCanonicalPath(field string) (Path, error) {
	p, err := DecodePath(field)
	if err != nil {
		return "", err
	}
	if enc := EncodePath(p); enc != field {
		return "", fmt.Errorf("non-canonical path encoding %q (canonical is %q)", field, enc)
	}
	return p, nil
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}
