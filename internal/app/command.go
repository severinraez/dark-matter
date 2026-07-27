package app

import (
	"strconv"

	"meltcloud.io/dm/internal/core/record"
)

// The typed command vocabulary cli's parse constructs — inputs belong to
// the callee (architecture.md §8). M3 carries the full CRUD grammar; `s`
// (M7) and `vd` (M6) arrive with their milestones.

// Command is one parsed batch command.
type Command interface {
	// Raw returns the verbatim input text — the `▸` block echo (§4.3).
	Raw() string
}

// Ref is a handle-addressed target: a literal `#handle`, or a same-batch
// `$N` backreference to the entry created by the batch's Nth command
// (§4.2). Backref > 0 means the latter; phase 1 has already verified it
// points to an earlier `a` command, so phase 2 only resolves it.
type Ref struct {
	Handle  record.Handle
	Backref int // 1-based command index; 0 = literal handle
}

// IsBackref reports a $N reference.
func (r Ref) IsBackref() bool { return r.Backref > 0 }

// String returns the input form, for error messages.
func (r Ref) String() string {
	if r.IsBackref() {
		return "$" + strconv.Itoa(r.Backref)
	}
	return "#" + string(r.Handle)
}

// CmdCreate is `a:path:subj:body` (§4.2).
type CmdCreate struct {
	RawText string
	Path    record.Path
	Subj    record.Subject
	Body    string
}

// CmdRead is `r:path`, `r:path:N`, or `r:#handle`/`r:$N` (§4.2). Exactly
// one of Path/Target is set.
type CmdRead struct {
	RawText string
	Path    record.Path
	Target  Ref
	Depth   int
}

// CmdSupersede is `u:#handle:body` (§6.1): a new body revision, anchors
// re-stamped.
type CmdSupersede struct {
	RawText string
	Target  Ref
	Body    string
}

// CmdTombstone is `d:#handle` (§6.1).
type CmdTombstone struct {
	RawText string
	Target  Ref
}

// CmdReAnchor is `k:#handle[:path]` (§6.1, §9.3): confirm valid,
// re-anchor; the optional path names the new home explicitly.
type CmdReAnchor struct {
	RawText string
	Target  Ref
	Path    record.Path // "" = re-anchor in place
}

// CmdFeedback is `f:#handle:sig[:reason]` (§7.3).
type CmdFeedback struct {
	RawText string
	Target  Ref
	Sig     record.Sig
	Reason  string // optional; "" = none
}

// CmdLink is `al:#a:#b[:note]` (§6.4): directed a → b.
type CmdLink struct {
	RawText string
	From    Ref
	To      Ref
	Comment string // optional; "" = none
}

// CmdUnlink is `dl:#a:#b` (§6.4).
type CmdUnlink struct {
	RawText string
	From    Ref
	To      Ref
}

// CmdMove is `mv:old:new` (§6.5): the manual relocation macro, one RP per
// entry homed at old; recursive when both paths are folders.
type CmdMove struct {
	RawText string
	Old     record.Path
	New     record.Path
}

// CmdRemove is `rm:path` (§6.5): one TB per entry homed at path,
// recursive for folders.
type CmdRemove struct {
	RawText string
	Path    record.Path
}

// CmdVerdict is `vd:sha1:landed:sha2` | `vd:sha1:unlanded` (§9.4): manual
// landing disposition (m5) — the subject is a line tip when its objects
// are still reachable (bulk-binds the whole segment) or a single origin
// otherwise; `unlanded` voids a wrong binding. Explicit agent judgment,
// allowed from any clone, degraded included.
type CmdVerdict struct {
	RawText  string
	Subject  record.SHA
	Landed   bool
	LandedAs record.SHA // set iff Landed
}

func (c CmdCreate) Raw() string    { return c.RawText }
func (c CmdRead) Raw() string      { return c.RawText }
func (c CmdSupersede) Raw() string { return c.RawText }
func (c CmdTombstone) Raw() string { return c.RawText }
func (c CmdReAnchor) Raw() string  { return c.RawText }
func (c CmdFeedback) Raw() string  { return c.RawText }
func (c CmdLink) Raw() string      { return c.RawText }
func (c CmdUnlink) Raw() string    { return c.RawText }
func (c CmdMove) Raw() string      { return c.RawText }
func (c CmdRemove) Raw() string    { return c.RawText }
func (c CmdVerdict) Raw() string   { return c.RawText }
