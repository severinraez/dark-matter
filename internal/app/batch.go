package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"meltcloud.io/dm/internal/core/fold"
	"meltcloud.io/dm/internal/core/record"
	"meltcloud.io/dm/internal/core/view"
)

// Block is one command's structured outcome — cli renders it under the
// command's `▸` echo (§4.3). Exactly one result field is set: Err for a
// whole-command `✗`, Macro for mv/rm (per-entry lines, §6.5), one of the
// others for a single-line ack or read result.
type Block struct {
	Echo      string
	Err       string // whole-command ✗ message
	Created   *view.Created
	Ack       *view.Ack
	Macro     []MacroItem
	Surface   *view.Surface
	Expansion *view.Expansion
}

// MacroItem is one entry's line inside an mv/rm expansion: an ack, or a
// per-entry ✗ (the rest of the macro still applies — §6.5).
type MacroItem struct {
	Ack view.Ack
	Err string
}

// BatchResult is phase 2's outcome: one block per command, in input order,
// plus the footer tally (§4.4). A command counts as failed if its block
// carries a whole-command or per-entry error.
type BatchResult struct {
	Blocks []Block
	OK     int
	Failed int
}

// ExecuteBatch runs a syntactically-valid batch in order (§4.1 phase 2).
// Reads never fail the batch; write failures error inline while other
// commands still apply. Later commands see earlier writes — the pending
// overlay is read-your-writes (architecture.md §7).
func (s *Session) ExecuteBatch(cmds []Command) (BatchResult, error) {
	ex, err := s.newExecutor()
	if err != nil {
		return BatchResult{}, err
	}
	var res BatchResult
	for i, cmd := range cmds {
		b := Block{Echo: cmd.Raw()}
		switch c := cmd.(type) {
		case CmdCreate:
			b.Created, b.Err = ex.create(i+1, c)
		case CmdRead:
			if c.Target.Handle != "" || c.Target.IsBackref() {
				b.Expansion, b.Err = ex.expand(c)
			} else {
				b.Surface, b.Err = ex.read(c)
			}
		case CmdSupersede:
			b.Ack, b.Err = ex.supersede(c)
		case CmdTombstone:
			b.Ack, b.Err = ex.tombstone(c)
		case CmdReAnchor:
			b.Ack, b.Err = ex.reAnchor(c)
		case CmdFeedback:
			b.Ack, b.Err = ex.feedback(c)
		case CmdLink:
			b.Ack, b.Err = ex.link(c)
		case CmdUnlink:
			b.Ack, b.Err = ex.unlink(c)
		case CmdMove:
			b.Macro, b.Err = ex.move(c)
		case CmdRemove:
			b.Macro, b.Err = ex.remove(c)
		default:
			return BatchResult{}, fmt.Errorf("unknown command type %T", cmd)
		}
		failed := b.Err != ""
		for _, it := range b.Macro {
			if it.Err != "" {
				failed = true
			}
		}
		if failed {
			res.Failed++
		} else {
			res.OK++
		}
		res.Blocks = append(res.Blocks, b)
	}
	return res, nil
}

// executor carries the batch-scoped view over store ∪ pending plus memos
// that hold for one checkout snapshot.
type executor struct {
	s        *Session
	records  map[record.EntryID][]record.Record // per-entry CR/SU/TB/RA/RP/FB
	linkRecs []record.Record                    // LN/UL, folded on demand
	order    []record.EntryID                   // creation (rec-id) order
	handles  map[record.EntryID]record.Handle
	taken    map[record.Handle]bool
	landed   map[record.SHA]bool // rule-(b) memo, valid for this HEAD
	folded   map[record.EntryID]fold.Entry
	links    []fold.Link            // memo over linkRecs; nil after invalidation
	made     map[int]record.EntryID // $N → the entry command N created
}

func (s *Session) newExecutor() (*executor, error) {
	ex := &executor{
		s:       s,
		records: make(map[record.EntryID][]record.Record),
		handles: make(map[record.EntryID]record.Handle),
		taken:   make(map[record.Handle]bool),
		landed:  make(map[record.SHA]bool),
		folded:  make(map[record.EntryID]fold.Entry),
		made:    make(map[int]record.EntryID),
	}
	// Replay store ∪ pending in rec-id order so handle extension resolves
	// exactly as it did at each mint (§3.5).
	for _, rec := range s.View() {
		if err := ex.absorb(rec); err != nil {
			return nil, err
		}
	}
	return ex, nil
}

// absorb indexes one record into the batch-scoped view and invalidates the
// memos it touches. Handles derive from CR admission only.
func (ex *executor) absorb(rec record.Record) error {
	switch v := rec.(type) {
	case record.Create:
		if _, seen := ex.handles[v.Entry]; !seen {
			ex.order = append(ex.order, v.Entry)
			h, err := record.DeriveHandle(v.Entry, func(h record.Handle) bool { return ex.taken[h] })
			if err != nil {
				return err
			}
			ex.handles[v.Entry] = h
			ex.taken[h] = true
		}
		ex.addEntryRecord(v.Entry, rec)
	case record.Supersede:
		ex.addEntryRecord(v.Entry, rec)
	case record.Tombstone:
		ex.addEntryRecord(v.Entry, rec)
	case record.ReAnchor:
		ex.addEntryRecord(v.Entry, rec)
	case record.RePath:
		ex.addEntryRecord(v.Entry, rec)
	case record.Feedback:
		ex.addEntryRecord(v.Entry, rec)
	case record.Link, record.Unlink:
		ex.linkRecs = append(ex.linkRecs, rec)
		ex.links = nil
	}
	// Other types (VD) play no role in the entry fold; dump still prints
	// them from Pending.
	return nil
}

func (ex *executor) addEntryRecord(id record.EntryID, rec record.Record) {
	ex.records[id] = append(ex.records[id], rec)
	delete(ex.folded, id)
}

// commit durably appends one record before its ack renders — per-command
// durability (architecture.md §7) — and folds it into the overlay so later
// commands read this write.
func (ex *executor) commit(rec record.Record) error {
	encoded, err := record.Encode(rec)
	if err != nil {
		return err
	}
	if err := ex.s.Local.AppendPendingRecord(rec.ID(), encoded); err != nil {
		return err
	}
	ex.s.Pending = append(ex.s.Pending, rec)
	return ex.absorb(rec)
}

// fold returns the entry state this checkout reads, classifying each
// record's origin against HEAD first — rule (b) consumed per record as
// data-in (architecture.md §6).
func (ex *executor) fold(id record.EntryID) (fold.Entry, error) {
	if e, ok := ex.folded[id]; ok {
		return e, nil
	}
	recs := ex.records[id]
	landed := make(map[record.SHA]bool)
	for _, r := range recs {
		var origin record.SHA
		switch v := r.(type) {
		case record.Create:
			origin = v.Origin
		case record.Supersede:
			origin = v.Origin
		case record.ReAnchor:
			origin = v.Origin
		case record.RePath:
			origin = v.Origin
		default:
			continue
		}
		ok, err := ex.landedInHead(origin)
		if err != nil {
			return fold.Entry{}, err
		}
		landed[origin] = ok
	}
	e := fold.Fold(id, recs, landed)
	ex.folded[id] = e
	return e, nil
}

func (ex *executor) landedInHead(origin record.SHA) (bool, error) {
	if v, ok := ex.landed[origin]; ok {
		return v, nil
	}
	v, err := ex.s.Repo.IsAncestor(origin, ex.s.Head)
	if err != nil {
		return false, err
	}
	ex.landed[origin] = v
	return v, nil
}

func (ex *executor) linkSet() []fold.Link {
	if ex.links == nil {
		ex.links = fold.Links(ex.linkRecs)
	}
	return ex.links
}

// live reports whether an entry participates in reads here: landed, not
// tombstoned (§5.3 — pending-elsewhere and deleted entries are invisible).
func (ex *executor) live(id record.EntryID) (bool, error) {
	if _, known := ex.handles[id]; !known {
		return false, nil // dangling reference — ignored at read (§8.7)
	}
	e, err := ex.fold(id)
	if err != nil {
		return false, err
	}
	return e.Landed && !e.Deleted, nil
}

// ---- handle resolution (semantic, phase 2 — architecture.md §7) ----

// resolve maps a ref to the one entry it addresses against the visible
// set (§5.3), or returns the per-command error text: unknown (covers
// pending-elsewhere — indistinguishable by design), deleted, or ambiguous
// with the extended candidates (§3.5).
func (ex *executor) resolve(ref Ref) (record.EntryID, string) {
	if ref.IsBackref() {
		id, ok := ex.made[ref.Backref]
		if !ok {
			return record.EntryID{}, fmt.Sprintf("%s: referenced create failed", ref)
		}
		return ex.checkAddressable(id, ref)
	}
	var alive, dead []record.EntryID
	for _, id := range ex.order {
		if !strings.HasPrefix(id.Tail(), string(ref.Handle)) {
			continue
		}
		e, err := ex.fold(id)
		if err != nil {
			return record.EntryID{}, err.Error()
		}
		switch {
		case e.Deleted:
			dead = append(dead, id)
		case e.Landed:
			alive = append(alive, id)
		}
	}
	switch len(alive) {
	case 1:
		return alive[0], ""
	case 0:
		if len(dead) > 0 {
			return record.EntryID{}, ref.String() + " deleted"
		}
		return record.EntryID{}, ref.String() + " unknown"
	default:
		ext := extendDistinct(alive, len(ref.Handle))
		parts := make([]string, len(alive))
		for i, id := range alive {
			parts[i] = "#" + string(ext[id])
		}
		return record.EntryID{}, fmt.Sprintf("%s ambiguous: %s", ref, strings.Join(parts, " · "))
	}
}

func (ex *executor) checkAddressable(id record.EntryID, ref Ref) (record.EntryID, string) {
	e, err := ex.fold(id)
	if err != nil {
		return record.EntryID{}, err.Error()
	}
	if e.Deleted {
		return record.EntryID{}, ref.String() + " deleted"
	}
	if !e.Landed {
		return record.EntryID{}, ref.String() + " unknown"
	}
	return id, ""
}

// extendDistinct picks, per colliding entry, the shortest §3.5 ladder
// prefix that is longer than the ambiguous input and distinct within the
// candidate set — the extended forms an ambiguity error surfaces.
func extendDistinct(ids []record.EntryID, inputLen int) map[record.EntryID]record.Handle {
	ladders := make([][]record.Handle, len(ids))
	for i, id := range ids {
		ladders[i] = record.HandleCandidates(id)
	}
	for step := 0; step < len(ladders[0]); step++ {
		if len(ladders[0][step]) <= inputLen {
			continue
		}
		seen := make(map[record.Handle]bool, len(ids))
		distinct := true
		for i := range ids {
			if seen[ladders[i][step]] {
				distinct = false
				break
			}
			seen[ladders[i][step]] = true
		}
		if distinct {
			out := make(map[record.EntryID]record.Handle, len(ids))
			for i, id := range ids {
				out[id] = ladders[i][step]
			}
			return out
		}
	}
	// Full-tail collision: nothing distinguishes them but the whole tail.
	out := make(map[record.EntryID]record.Handle, len(ids))
	for _, id := range ids {
		out[id] = record.Handle(id.Tail())
	}
	return out
}

// ---- write verbs ----

// create executes `a` (§6.1, §8.6): stamp both anchors from the working
// tree, mint ids, append exactly one pending record before acking.
func (ex *executor) create(idx int, c CmdCreate) (*view.Created, string) {
	if c.Path.IsFolder() {
		return nil, fmt.Sprintf("%s: folder notes not implemented yet (M5)", record.EncodePath(c.Path))
	}
	if err := record.ValidateBody(c.Body); err != nil {
		return nil, err.Error()
	}
	blob, err := ex.s.Repo.WorkingBlob(c.Path)
	if err != nil {
		return nil, err.Error()
	}
	if blob == nil {
		return nil, fmt.Sprintf("%s: no such file in working tree", record.EncodePath(c.Path))
	}
	entryID, err := ex.s.Minter.MintEntryID()
	if err != nil {
		return nil, err.Error()
	}
	handle, err := record.DeriveHandle(entryID, func(h record.Handle) bool { return ex.taken[h] })
	if err != nil {
		return nil, err.Error()
	}
	recID, err := ex.s.Minter.MintRecID()
	if err != nil {
		return nil, err.Error()
	}
	cr := record.Create{
		Rec:    recID,
		Entry:  entryID,
		Subj:   c.Subj,
		Anchor: record.BlobAnchor(*blob),
		Origin: ex.s.Head,
		Path:   c.Path,
		Body:   c.Body,
	}
	if err := ex.stageBlobIfUncommitted(*blob, c.Path); err != nil {
		return nil, err.Error()
	}
	if err := ex.commit(cr); err != nil {
		return nil, err.Error()
	}
	ex.made[idx] = entryID
	return &view.Created{Handle: handle}, ""
}

// supersede executes `u` (§6.1): a new body revision, anchors re-stamped
// from the working tree at the entry's folded path.
func (ex *executor) supersede(c CmdSupersede) (*view.Ack, string) {
	id, msg := ex.resolve(c.Target)
	if msg != "" {
		return nil, msg
	}
	if err := record.ValidateBody(c.Body); err != nil {
		return nil, err.Error()
	}
	e, err := ex.fold(id)
	if err != nil {
		return nil, err.Error()
	}
	blob, err := ex.s.Repo.WorkingBlob(e.Path)
	if err != nil {
		return nil, err.Error()
	}
	if blob == nil {
		return nil, fmt.Sprintf("%s: no such file in working tree", record.EncodePath(e.Path))
	}
	recID, err := ex.s.Minter.MintRecID()
	if err != nil {
		return nil, err.Error()
	}
	su := record.Supersede{
		Rec:    recID,
		Entry:  id,
		Subj:   e.Subj,
		Anchor: record.BlobAnchor(*blob),
		Origin: ex.s.Head,
		Path:   e.Path,
		Body:   c.Body,
	}
	if err := ex.stageBlobIfUncommitted(*blob, e.Path); err != nil {
		return nil, err.Error()
	}
	if err := ex.commit(su); err != nil {
		return nil, err.Error()
	}
	after, err := ex.fold(id)
	if err != nil {
		return nil, err.Error()
	}
	return &view.Ack{Verb: view.AckSuperseded, Handle: ex.handles[id], Rev: after.Rev}, ""
}

// tombstone executes `d` (§6.1): terminal, absorbing (§8.3).
func (ex *executor) tombstone(c CmdTombstone) (*view.Ack, string) {
	id, msg := ex.resolve(c.Target)
	if msg != "" {
		return nil, msg
	}
	recID, err := ex.s.Minter.MintRecID()
	if err != nil {
		return nil, err.Error()
	}
	if err := ex.commit(record.Tombstone{Rec: recID, Entry: id}); err != nil {
		return nil, err.Error()
	}
	return &view.Ack{Verb: view.AckTombstoned, Handle: ex.handles[id]}, ""
}

// reAnchor executes `k` (§6.1, §9.5): re-stamp the anchors with no new
// body revision; the optional path names the new home explicitly.
func (ex *executor) reAnchor(c CmdReAnchor) (*view.Ack, string) {
	id, msg := ex.resolve(c.Target)
	if msg != "" {
		return nil, msg
	}
	path := c.Path
	if path == "" {
		e, err := ex.fold(id)
		if err != nil {
			return nil, err.Error()
		}
		path = e.Path
	}
	if path.IsFolder() {
		return nil, fmt.Sprintf("%s: folder notes not implemented yet (M5)", record.EncodePath(path))
	}
	blob, err := ex.s.Repo.WorkingBlob(path)
	if err != nil {
		return nil, err.Error()
	}
	if blob == nil {
		return nil, fmt.Sprintf("%s: no such file in working tree", record.EncodePath(path))
	}
	recID, err := ex.s.Minter.MintRecID()
	if err != nil {
		return nil, err.Error()
	}
	ra := record.ReAnchor{
		Rec:    recID,
		Entry:  id,
		Anchor: record.BlobAnchor(*blob),
		Origin: ex.s.Head,
		Path:   path,
	}
	if err := ex.stageBlobIfUncommitted(*blob, path); err != nil {
		return nil, err.Error()
	}
	if err := ex.commit(ra); err != nil {
		return nil, err.Error()
	}
	return &view.Ack{Verb: view.AckReAnchored, Handle: ex.handles[id]}, ""
}

// feedback executes `f` (§7.3): an FB log record, never a counter.
func (ex *executor) feedback(c CmdFeedback) (*view.Ack, string) {
	id, msg := ex.resolve(c.Target)
	if msg != "" {
		return nil, msg
	}
	if c.Reason != "" {
		if err := record.ValidateBody(c.Reason); err != nil {
			return nil, err.Error()
		}
	}
	recID, err := ex.s.Minter.MintRecID()
	if err != nil {
		return nil, err.Error()
	}
	fb := record.Feedback{Rec: recID, Entry: id, Sig: c.Sig, Reason: c.Reason}
	if err := ex.commit(fb); err != nil {
		return nil, err.Error()
	}
	return &view.Ack{Verb: view.AckFeedback, Handle: ex.handles[id], Sig: c.Sig}, ""
}

// link executes `al` (§6.4): a directed LN record between two visible
// entries.
func (ex *executor) link(c CmdLink) (*view.Ack, string) {
	from, msg := ex.resolve(c.From)
	if msg != "" {
		return nil, msg
	}
	to, msg := ex.resolve(c.To)
	if msg != "" {
		return nil, msg
	}
	if from == to {
		return nil, fmt.Sprintf("#%s: cannot link an entry to itself", ex.handles[from])
	}
	if c.Comment != "" {
		if err := record.ValidateBody(c.Comment); err != nil {
			return nil, err.Error()
		}
	}
	recID, err := ex.s.Minter.MintRecID()
	if err != nil {
		return nil, err.Error()
	}
	ln := record.Link{Rec: recID, From: from, To: to, Comment: c.Comment}
	if err := ex.commit(ln); err != nil {
		return nil, err.Error()
	}
	return &view.Ack{Verb: view.AckLinked, Handle: ex.handles[from], To: ex.handles[to]}, ""
}

// unlink executes `dl` (§6.4): an UL record; the pair must currently fold
// to linked.
func (ex *executor) unlink(c CmdUnlink) (*view.Ack, string) {
	from, msg := ex.resolve(c.From)
	if msg != "" {
		return nil, msg
	}
	to, msg := ex.resolve(c.To)
	if msg != "" {
		return nil, msg
	}
	linked := false
	for _, ln := range ex.linkSet() {
		if ln.From == from && ln.To == to {
			linked = true
			break
		}
	}
	if !linked {
		return nil, fmt.Sprintf("no link #%s → #%s", ex.handles[from], ex.handles[to])
	}
	recID, err := ex.s.Minter.MintRecID()
	if err != nil {
		return nil, err.Error()
	}
	if err := ex.commit(record.Unlink{Rec: recID, From: from, To: to}); err != nil {
		return nil, err.Error()
	}
	return &view.Ack{Verb: view.AckUnlinked, Handle: ex.handles[from], To: ex.handles[to]}, ""
}

// ---- mv / rm macros (§6.5) ----

// homedAt returns the visible-or-worklist-state entries homed at path in
// creation order — any landed, non-tombstoned entry whose folded path
// matches (rule (a) deliberately not required: orphans at a dead path are
// exactly mv/rm's target; pending-elsewhere entries are untouched).
func (ex *executor) homedAt(path record.Path) ([]record.EntryID, error) {
	var out []record.EntryID
	for _, id := range ex.order {
		e, err := ex.fold(id)
		if err != nil {
			return nil, err
		}
		if e.Deleted || !e.Landed {
			continue
		}
		if path.IsFolder() {
			if !strings.HasPrefix(string(e.Path), string(path)) {
				continue
			}
		} else if e.Path != path {
			continue
		}
		out = append(out, id)
	}
	return out, nil
}

// move executes `mv:old:new`: one RP (path hint + scoping origin) per
// entry homed at old — relocate, never bless; the content anchor is
// untouched. A destination file absent from the working tree fails that
// entry alone (§6.5).
func (ex *executor) move(c CmdMove) ([]MacroItem, string) {
	targets, err := ex.homedAt(c.Old)
	if err != nil {
		return nil, err.Error()
	}
	if len(targets) == 0 {
		return nil, fmt.Sprintf("no entries at %s", record.EncodePath(c.Old))
	}
	var items []MacroItem
	for _, id := range targets {
		e, err := ex.fold(id)
		if err != nil {
			return nil, err.Error()
		}
		newPath := c.New
		if c.Old.IsFolder() {
			newPath = c.New + e.Path[len(c.Old):]
		}
		blob, err := ex.s.Repo.WorkingBlob(newPath)
		if err != nil {
			return nil, err.Error()
		}
		if blob == nil {
			items = append(items, MacroItem{Err: fmt.Sprintf("#%s: %s: no such file in working tree",
				ex.handles[id], record.EncodePath(newPath))})
			continue
		}
		recID, err := ex.s.Minter.MintRecID()
		if err != nil {
			return nil, err.Error()
		}
		rp := record.RePath{Rec: recID, Entry: id, Origin: ex.s.Head, Path: newPath}
		if err := ex.commit(rp); err != nil {
			return nil, err.Error()
		}
		items = append(items, MacroItem{Ack: view.Ack{Verb: view.AckMoved, Handle: ex.handles[id]}})
	}
	return items, ""
}

// remove executes `rm:path`: one TB per entry homed at path — the
// node-level counterpart to d, for paths whose knowledge is genuinely
// gone (§6.5).
func (ex *executor) remove(c CmdRemove) ([]MacroItem, string) {
	targets, err := ex.homedAt(c.Path)
	if err != nil {
		return nil, err.Error()
	}
	if len(targets) == 0 {
		return nil, fmt.Sprintf("no entries at %s", record.EncodePath(c.Path))
	}
	var items []MacroItem
	for _, id := range targets {
		recID, err := ex.s.Minter.MintRecID()
		if err != nil {
			return nil, err.Error()
		}
		if err := ex.commit(record.Tombstone{Rec: recID, Entry: id}); err != nil {
			return nil, err.Error()
		}
		items = append(items, MacroItem{Ack: view.Ack{Verb: view.AckTombstoned, Handle: ex.handles[id]}})
	}
	return items, ""
}

// stageBlobIfUncommitted stages described bytes the odb lacks to
// pending/blobs (§8.1) so truly uncommitted content stays fetchable. The
// staged bytes are the raw working-tree file; if checkin filters rewrite
// content the staged bytes can diverge from the anchor SHA — the store
// writes them as-is under the anchor's path, which stays the address
// (store.Commit).
func (ex *executor) stageBlobIfUncommitted(blob record.SHA, path record.Path) error {
	has, err := ex.s.Repo.HasObject(blob)
	if err != nil || has {
		return err
	}
	content, err := os.ReadFile(filepath.Join(ex.s.Repo.Top, filepath.FromSlash(string(path))))
	if err != nil {
		return err
	}
	return ex.s.Local.StageBlob(blob, content)
}

// ---- reads ----

// read executes `r:path` (§5.1, §8.6). Visibility per M3's slice: rule (a)
// = the folded anchor blob at the folded path hint (§9.1 layer 1), rule
// (b) folded per record by core/fold. Layers 2–5, folder/parent notes, and
// ranking arrive with their milestones.
func (ex *executor) read(c CmdRead) (*view.Surface, string) {
	if c.Path.IsFolder() {
		return nil, fmt.Sprintf("%s: folder reads not implemented yet (M5)", record.EncodePath(c.Path))
	}
	working, err := ex.s.Repo.WorkingBlob(c.Path)
	if err != nil {
		return nil, err.Error()
	}
	surface := &view.Surface{Node: c.Path}
	shown := make(map[record.EntryID]bool)
	for _, id := range ex.order {
		e, err := ex.fold(id)
		if err != nil {
			return nil, err.Error()
		}
		if e.Deleted || !e.Landed || e.Anchor.IsPath() || e.Path != c.Path {
			continue
		}
		if working == nil || e.Anchor.Blob != *working {
			continue // rule (a): the tree does not contain the described content
		}
		surface.Own = append(surface.Own, view.Preview(e.Subj, ex.handles[id], e.Body))
		shown[id] = true
	}
	links, err := ex.countLinks(shown)
	if err != nil {
		return nil, err.Error()
	}
	surface.Links = links
	return surface, ""
}

// countLinks tallies live link edges touching the surfaced entries — the
// inventory footer's link count (§5.1).
func (ex *executor) countLinks(shown map[record.EntryID]bool) (int, error) {
	n := 0
	for _, ln := range ex.linkSet() {
		if !shown[ln.From] && !shown[ln.To] {
			continue
		}
		fromLive, err := ex.live(ln.From)
		if err != nil {
			return 0, err
		}
		toLive, err := ex.live(ln.To)
		if err != nil {
			return 0, err
		}
		if fromLive && toLive {
			n++
		}
	}
	return n, nil
}

// expand executes `r:#handle` (§5.3): the full body of one entry plus its
// links one level and relations. Link endpoints that are not visible here
// are omitted (§5.3; dangling ids are ignorable, §8.7).
func (ex *executor) expand(c CmdRead) (*view.Expansion, string) {
	id, msg := ex.resolve(c.Target)
	if msg != "" {
		return nil, msg
	}
	e, err := ex.fold(id)
	if err != nil {
		return nil, err.Error()
	}
	exp := &view.Expansion{Node: e.Path, Subj: e.Subj, Handle: ex.handles[id], Body: e.Body}
	seenFrom := make(map[record.Path]bool)
	for _, ln := range ex.linkSet() {
		switch id {
		case ln.From:
			ok, err := ex.live(ln.To)
			if err != nil {
				return nil, err.Error()
			}
			if ok {
				exp.Links = append(exp.Links, view.LinkPreview{Handle: ex.handles[ln.To], Comment: ln.Comment})
			}
		case ln.To:
			ok, err := ex.live(ln.From)
			if err != nil {
				return nil, err.Error()
			}
			if !ok {
				continue
			}
			src, err := ex.fold(ln.From)
			if err != nil {
				return nil, err.Error()
			}
			if !seenFrom[src.Path] {
				seenFrom[src.Path] = true
				exp.LinkedFrom = append(exp.LinkedFrom, src.Path)
			}
		}
	}
	return exp, ""
}
