package app

import (
	"fmt"
	"os"
	"path/filepath"

	"meltcloud.io/dm/internal/core/record"
	"meltcloud.io/dm/internal/core/view"
)

// Block is one command's structured outcome — cli renders it under the
// command's `▸` echo (§4.3). Exactly one field is set.
type Block struct {
	Echo    string
	Created *view.Created
	Surface *view.Surface
	Err     string // per-command ✗ message
}

// BatchResult is phase 2's outcome: one block per command, in input order,
// plus the footer tally (§4.4).
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
	for _, cmd := range cmds {
		b := Block{Echo: cmd.Raw()}
		switch c := cmd.(type) {
		case CmdCreate:
			b.Created, b.Err = ex.create(c)
		case CmdRead:
			b.Surface, b.Err = ex.read(c)
		default:
			return BatchResult{}, fmt.Errorf("unknown command type %T", cmd)
		}
		if b.Err == "" {
			res.OK++
		} else {
			res.Failed++
		}
		res.Blocks = append(res.Blocks, b)
	}
	return res, nil
}

// entry is the folded current state of one logical entry. M2's fold is the
// degenerate case — only CR records exist (`a` is the only write verb), so
// current state is the create itself; the real per-record LWW fold arrives
// with core/fold in M3 and replaces this.
type entry struct {
	Subj   record.Subject
	Anchor record.Anchor
	Origin record.SHA
	Path   record.Path
	Body   string
}

// executor carries the batch-scoped view over store ∪ pending (M2: pending
// only) plus memos that hold for one checkout snapshot.
type executor struct {
	s       *Session
	entries map[record.EntryID]*entry
	order   []record.EntryID // creation (rec-id) order — M2's deterministic surface order
	handles map[record.EntryID]record.Handle
	taken   map[record.Handle]bool
	landed  map[record.SHA]bool // rule-(b) memo, valid for this HEAD
}

func (s *Session) newExecutor() (*executor, error) {
	ex := &executor{
		s:       s,
		entries: make(map[record.EntryID]*entry),
		handles: make(map[record.EntryID]record.Handle),
		taken:   make(map[record.Handle]bool),
		landed:  make(map[record.SHA]bool),
	}
	// Replay in rec-id order so handle extension resolves exactly as it
	// did at each mint (§3.5); Pending is already sorted.
	for _, rec := range s.Pending {
		cr, ok := rec.(record.Create)
		if !ok {
			// Other record types cannot exist before M3's write verbs;
			// tolerate them silently rather than corrupt the walking
			// skeleton (they fold in when core/fold lands).
			continue
		}
		if err := ex.admit(cr); err != nil {
			return nil, err
		}
	}
	return ex, nil
}

func (ex *executor) admit(cr record.Create) error {
	if _, seen := ex.entries[cr.Entry]; !seen {
		ex.order = append(ex.order, cr.Entry)
		h, err := record.DeriveHandle(cr.Entry, func(h record.Handle) bool { return ex.taken[h] })
		if err != nil {
			return err
		}
		ex.handles[cr.Entry] = h
		ex.taken[h] = true
	}
	ex.entries[cr.Entry] = &entry{cr.Subj, cr.Anchor, cr.Origin, cr.Path, cr.Body}
	return nil
}

// create executes `a` (§6.1, §8.6): stamp both anchors from the working
// tree, mint ids, append exactly one pending record before acking —
// per-command durability (architecture.md §7).
func (ex *executor) create(c CmdCreate) (*view.Created, string) {
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
	encoded, err := record.Encode(cr)
	if err != nil {
		return nil, err.Error()
	}
	if err := ex.stageBlobIfUncommitted(*blob, c.Path); err != nil {
		return nil, err.Error()
	}
	if err := ex.s.Local.AppendPendingRecord(recID, encoded); err != nil {
		return nil, err.Error()
	}
	ex.s.Pending = append(ex.s.Pending, cr)
	if err := ex.admit(cr); err != nil {
		return nil, err.Error()
	}
	return &view.Created{Handle: handle}, ""
}

// stageBlobIfUncommitted stages described bytes the odb lacks to
// pending/blobs (§8.1) so truly uncommitted content stays fetchable. The
// staged bytes are the raw working-tree file; if checkin filters rewrite
// content the staged bytes can diverge from the blob — reconciled when the
// store materializes blobs (M4).
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

// read executes `r:path` (§5.1, §8.6). M2 visibility is the walking
// skeleton's two minimal rules: rule (a) = exact anchored blob at the path
// hint (§9.1 layer 1 only), rule (b) = origin is an ancestor of HEAD.
// Layers 2–5, folder/parent notes, links, ranking, and stats arrive with
// their milestones.
func (ex *executor) read(c CmdRead) (*view.Surface, string) {
	if c.Handle != "" {
		return nil, fmt.Sprintf("#%s: handle reads not implemented yet (M3)", c.Handle)
	}
	if c.Path.IsFolder() {
		return nil, fmt.Sprintf("%s: folder reads not implemented yet (M5)", record.EncodePath(c.Path))
	}
	working, err := ex.s.Repo.WorkingBlob(c.Path)
	if err != nil {
		return nil, err.Error()
	}
	surface := &view.Surface{Node: c.Path}
	for _, id := range ex.order {
		e := ex.entries[id]
		if e.Path != c.Path || e.Anchor.IsPath() {
			continue
		}
		if working == nil || e.Anchor.Blob != *working {
			continue // rule (a): the tree does not contain the described content
		}
		ok, err := ex.landedInHead(e.Origin)
		if err != nil {
			return nil, err.Error()
		}
		if !ok {
			continue // rule (b): the origin line has not landed here
		}
		surface.Own = append(surface.Own, view.Preview(e.Subj, ex.handles[id], e.Body))
	}
	return surface, ""
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
