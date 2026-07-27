package app

import "meltcloud.io/dm/internal/core/record"

// The typed command vocabulary cli's parse constructs — inputs belong to
// the callee (architecture.md §8). M2 carries the walking-skeleton verbs
// `a` and `r`; the rest of the §4.2 grammar arrives with M3.

// Command is one parsed batch command.
type Command interface {
	// Raw returns the verbatim input text — the `▸` block echo (§4.3).
	Raw() string
}

// CmdCreate is `a:path:subj:body` (§4.2).
type CmdCreate struct {
	RawText string
	Path    record.Path
	Subj    record.Subject
	Body    string
}

// CmdRead is `r:path`, `r:path:N`, or `r:#handle` (§4.2). Exactly one of
// Path/Handle is set.
type CmdRead struct {
	RawText string
	Path    record.Path
	Handle  record.Handle
	Depth   int
}

func (c CmdCreate) Raw() string { return c.RawText }
func (c CmdRead) Raw() string   { return c.RawText }
