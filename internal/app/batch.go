package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"meltcloud.io/dm/internal/core/fold"
	"meltcloud.io/dm/internal/core/lineage"
	"meltcloud.io/dm/internal/core/record"
	"meltcloud.io/dm/internal/core/resolve"
	"meltcloud.io/dm/internal/core/view"
	"meltcloud.io/dm/internal/evcache"
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
	Search    *view.SearchResult
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
	// The heavy matchers also fire at reads after a fetch — memoized per
	// (tip, target-tip), so this is cheap whenever nothing changed
	// (architecture.md §7 decision 5). Forced-update pairs exist only at
	// fetch time; sync passes them in.
	if err := ex.mintPass(nil); err != nil {
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
		case CmdSearch:
			b.Search, b.Err = ex.search(c)
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
		case CmdVerdict:
			b.Ack, b.Err = ex.verdict(c)
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
	ev       *evcache.Cached                    // the wired Evidence value (architecture.md §2)
	records  map[record.EntryID][]record.Record // per-entry CR/SU/TB/RA/RP/FB
	verdicts []record.Record                    // VD records, whole-store
	linkRecs []record.Record                    // LN/UL, folded on demand
	order    []record.EntryID                   // creation (rec-id) order
	handles  map[record.EntryID]record.Handle
	taken    map[record.Handle]bool
	bindings map[record.SHA]record.SHA             // winning landed bindings memo; nil after invalidation
	class    map[record.SHA]lineage.Classification // rule-(b) memo, valid for this snapshot
	folded   map[record.EntryID]fold.Entry
	resolved map[record.EntryID]resolve.Resolution // rule-(a) memo per entry
	links    []fold.Link                           // memo over linkRecs; nil after invalidation
	made     map[int]record.EntryID                // $N → the entry command N created
}

// newExecutor builds the read machinery and runs the m1 mint — the cheap
// reflog scan that fires at any read-bearing invocation (architecture.md
// §7: batch = Open → m1 mint → execute).
func (s *Session) newExecutor() (*executor, error) {
	ex, err := s.newExecutorFrom(s.View())
	if err != nil {
		return nil, err
	}
	if err := ex.mintM1(); err != nil {
		return nil, err
	}
	return ex, nil
}

// newExecutorFrom builds the machinery over an explicit record view — the
// sync mint pass reads the post-fetch tip while the session keeps its
// pre-fetch mirror for the epoch rule (§8.4).
func (s *Session) newExecutorFrom(view []record.Record) (*executor, error) {
	ex := &executor{
		s:        s,
		ev:       evcache.New(s.Repo.Evidence(s.Head), s.Local),
		records:  make(map[record.EntryID][]record.Record),
		handles:  make(map[record.EntryID]record.Handle),
		taken:    make(map[record.Handle]bool),
		class:    make(map[record.SHA]lineage.Classification),
		folded:   make(map[record.EntryID]fold.Entry),
		resolved: make(map[record.EntryID]resolve.Resolution),
		made:     make(map[int]record.EntryID),
	}
	// Replay store ∪ pending in rec-id order so handle extension resolves
	// exactly as it did at each mint (§3.5).
	for _, rec := range view {
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
	case record.Verdict:
		// A verdict can change every origin's classification (chains), so
		// the rule-(b) memos and everything folded over them invalidate.
		ex.verdicts = append(ex.verdicts, rec)
		ex.bindings = nil
		ex.class = make(map[record.SHA]lineage.Classification)
		ex.folded = make(map[record.EntryID]fold.Entry)
		ex.resolved = make(map[record.EntryID]resolve.Resolution)
	}
	return nil
}

func (ex *executor) addEntryRecord(id record.EntryID, rec record.Record) {
	ex.records[id] = append(ex.records[id], rec)
	delete(ex.folded, id)
	delete(ex.resolved, id)
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
// record's origin first — rule (b) consumed per record as data-in
// (architecture.md §6), chain-aware through verdict records (§9.4).
func (ex *executor) fold(id record.EntryID) (fold.Entry, error) {
	if e, ok := ex.folded[id]; ok {
		return e, nil
	}
	recs := ex.records[id]
	class, err := ex.classify(recordOrigins(recs))
	if err != nil {
		return fold.Entry{}, err
	}
	landed := make(map[record.SHA]bool, len(class))
	for origin, c := range class {
		landed[origin] = c.State == lineage.Landed
	}
	e := fold.Fold(id, recs, landed)
	ex.folded[id] = e
	return e, nil
}

// recordOrigins lists the distinct origins of fold-participating records.
func recordOrigins(recs []record.Record) []record.SHA {
	var out []record.SHA
	seen := make(map[record.SHA]bool)
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
		if !seen[origin] {
			seen[origin] = true
			out = append(out, origin)
		}
	}
	return out
}

// landedBindings folds the VD set to the winning origin → landed-as map,
// memoized until a verdict is absorbed.
func (ex *executor) landedBindings() map[record.SHA]record.SHA {
	if ex.bindings == nil {
		ex.bindings = lineage.LandedBindings(lineage.WinningVerdicts(ex.verdicts))
	}
	return ex.bindings
}

// classify runs the §9.4 fold over origins, memoized per snapshot; only
// the not-yet-classified remainder hits the evidence.
func (ex *executor) classify(origins []record.SHA) (map[record.SHA]lineage.Classification, error) {
	out := make(map[record.SHA]lineage.Classification, len(origins))
	var missing []record.SHA
	for _, o := range origins {
		if c, ok := ex.class[o]; ok {
			out[o] = c
		} else {
			missing = append(missing, o)
		}
	}
	if len(missing) > 0 {
		fresh, err := lineage.Classify(ex.ev, ex.landedBindings(), ex.s.Qualified, missing)
		if err != nil {
			return nil, err
		}
		for o, c := range fresh {
			ex.class[o] = c
			out[o] = c
		}
	}
	return out, nil
}

// entryLineage is an entry's rule-(b) state — the best across its records
// (§8.3): any landed → landed; else any pending-elsewhere → pending;
// else unknown beats abandoned (a degraded clone never judges).
func (ex *executor) entryLineage(id record.EntryID) (lineage.State, error) {
	class, err := ex.classify(recordOrigins(ex.records[id]))
	if err != nil {
		return lineage.Abandoned, err
	}
	best := lineage.Abandoned
	for _, c := range class {
		switch c.State {
		case lineage.Landed:
			return lineage.Landed, nil
		case lineage.PendingElsewhere:
			best = lineage.PendingElsewhere
		case lineage.Unknown:
			if best == lineage.Abandoned {
				best = lineage.Unknown
			}
		}
	}
	return best, nil
}

func (ex *executor) linkSet() []fold.Link {
	if ex.links == nil {
		ex.links = fold.Links(ex.linkRecs)
	}
	return ex.links
}

// resolution answers rule (a) for a landed, non-deleted entry, memoized
// per snapshot (§9.1–§9.3; the memo invalidates when the entry gains a
// record).
func (ex *executor) resolution(id record.EntryID) (resolve.Resolution, error) {
	if r, ok := ex.resolved[id]; ok {
		return r, nil
	}
	e, err := ex.fold(id)
	if err != nil {
		return resolve.Resolution{}, err
	}
	r, err := resolve.Resolve(ex.ev, ex.ev, e)
	if err != nil {
		return resolve.Resolution{}, err
	}
	ex.resolved[id] = r
	return r, nil
}

// live reports whether an entry participates in reads here: landed, not
// tombstoned, and placeable — worklist states (orphaned, scattered) never
// surface in reads, search, or link expansion (§5.3).
func (ex *executor) live(id record.EntryID) (bool, error) {
	if _, known := ex.handles[id]; !known {
		return false, nil // dangling reference — ignored at read (§8.7)
	}
	e, err := ex.fold(id)
	if err != nil {
		return false, err
	}
	if !e.Landed || e.Deleted {
		return false, nil
	}
	res, err := ex.resolution(id)
	if err != nil {
		return false, err
	}
	return res.State.Visible(), nil
}

// ---- handle resolution (semantic, phase 2 — architecture.md §7) ----

// resolve maps a ref to the one entry it addresses against the visible
// set (§5.3), or returns the per-command error text: unknown (covers
// pending-elsewhere — indistinguishable by design), deleted, or ambiguous
// with the extended candidates (§3.5). Worklist-state entries (orphaned,
// scattered) are addressable only when repair is set — the k/u/d/mv/rm
// verbs that resolve worklist debt (§9.6); every other verb refuses them.
func (ex *executor) resolve(ref Ref, repair bool) (record.EntryID, string) {
	if ref.IsBackref() {
		id, ok := ex.made[ref.Backref]
		if !ok {
			return record.EntryID{}, fmt.Sprintf("%s: referenced create failed", ref)
		}
		return ex.checkAddressable(id, ref, repair)
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
		case repair:
			// Abandoned entries are addressable by the repair verbs from
			// the worklist (§5.3, §9.6 — that is how a dead line's note is
			// rescued or retired); pending-elsewhere and unknown stay
			// indistinguishable from nonexistent.
			ls, err := ex.entryLineage(id)
			if err != nil {
				return record.EntryID{}, err.Error()
			}
			if ls == lineage.Abandoned {
				alive = append(alive, id)
			}
		}
	}
	if !repair {
		// Filter to the visible set; a worklist-only match is reported as
		// such rather than as unknown — it exists, just not for this verb.
		var visible []record.EntryID
		worklisted := false
		for _, id := range alive {
			res, err := ex.resolution(id)
			if err != nil {
				return record.EntryID{}, err.Error()
			}
			if res.State.Visible() {
				visible = append(visible, id)
			} else {
				worklisted = true
			}
		}
		if len(visible) == 0 && worklisted {
			return record.EntryID{}, ref.String() + " on the worklist — repair with k/u/d/mv/rm"
		}
		alive = visible
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

func (ex *executor) checkAddressable(id record.EntryID, ref Ref, repair bool) (record.EntryID, string) {
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
	if !repair {
		res, err := ex.resolution(id)
		if err != nil {
			return record.EntryID{}, err.Error()
		}
		if !res.State.Visible() {
			return record.EntryID{}, ref.String() + " on the worklist — repair with k/u/d/mv/rm"
		}
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

// stampAnchor derives the write-time anchor for a home path (§8.3): the
// working blob (staged if the odb lacks it) for a file, the path key for a
// folder that exists in the checkout. The error return is per-command text.
func (ex *executor) stampAnchor(path record.Path) (record.Anchor, string) {
	if path.IsFolder() {
		under, err := ex.ev.PathsUnder(path)
		if err != nil {
			return record.Anchor{}, err.Error()
		}
		if len(under) == 0 {
			return record.Anchor{}, fmt.Sprintf("%s: no such folder in working tree", record.EncodePath(path))
		}
		return record.PathAnchor(path), ""
	}
	blob, err := ex.s.Repo.WorkingBlob(path)
	if err != nil {
		return record.Anchor{}, err.Error()
	}
	if blob == nil {
		return record.Anchor{}, fmt.Sprintf("%s: no such file in working tree", record.EncodePath(path))
	}
	if err := ex.stageBlobIfUncommitted(*blob, path); err != nil {
		return record.Anchor{}, err.Error()
	}
	return record.BlobAnchor(*blob), ""
}

// create executes `a` (§6.1, §8.6): stamp both anchors from the working
// tree, mint ids, append exactly one pending record before acking.
func (ex *executor) create(idx int, c CmdCreate) (*view.Created, string) {
	if err := record.ValidateBody(c.Body); err != nil {
		return nil, err.Error()
	}
	anchor, msg := ex.stampAnchor(c.Path)
	if msg != "" {
		return nil, msg
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
		Anchor: anchor,
		Origin: ex.s.Head,
		Path:   c.Path,
		Body:   c.Body,
	}
	if err := ex.commit(cr); err != nil {
		return nil, err.Error()
	}
	ex.made[idx] = entryID
	created := &view.Created{Handle: handle}
	// Crowding nudge (§9.6 layer 2): an `a` landing on a node already
	// carrying many visible notes appends the fold hint to its ack —
	// caught at write time, when the writer has just read the surface.
	n, err := ex.visibleAt(c.Path)
	if err != nil {
		return nil, err.Error()
	}
	if n >= view.CrowdingThreshold {
		created.Crowded = n
	}
	return created, ""
}

// visibleAt counts the visible notes homed at a node — the crowding
// signal's input.
func (ex *executor) visibleAt(path record.Path) (int, error) {
	n := 0
	for _, id := range ex.order {
		e, err := ex.fold(id)
		if err != nil {
			return 0, err
		}
		if e.Deleted || !e.Landed {
			continue
		}
		res, err := ex.resolution(id)
		if err != nil {
			return 0, err
		}
		if !res.State.Visible() {
			continue
		}
		if e.Anchor.IsPath() {
			for _, home := range folderHomes(res) {
				if home == path {
					n++
					break
				}
			}
		} else if res.Path == path {
			n++
		}
	}
	return n, nil
}

// restampPath picks the home a confirming write (`u`, plain `k`)
// re-stamps at: the entry's resolved location — blessing dm's guess is
// exactly what these verbs mean (§9.3). Entries without a single home
// need the agent to name one first.
func (ex *executor) restampPath(id record.EntryID) (record.Path, string) {
	res, err := ex.resolution(id)
	if err != nil {
		return "", err.Error()
	}
	h := ex.handles[id]
	switch res.State {
	case resolve.Split:
		parts := make([]string, len(res.Vote.Candidates))
		for i, c := range res.Vote.Candidates {
			parts[i] = record.EncodePath(c.Path)
		}
		return "", fmt.Sprintf("#%s ambiguous follow — k:#%s:path picks the home: %s",
			h, h, strings.Join(parts, " · "))
	case resolve.Scattered, resolve.Orphaned:
		return "", fmt.Sprintf("#%s %s — name the new home (k:#%s:path) or retire it (d/rm)",
			h, res.State, h)
	}
	return res.Path, ""
}

// supersede executes `u` (§6.1): a new body revision, anchors re-stamped
// from the working tree at the entry's resolved home. A repair verb — it
// is how worklisted debt gets rewritten (§9.6) — though an entry with no
// single home still needs `k:#h:path`/`mv` first.
func (ex *executor) supersede(c CmdSupersede) (*view.Ack, string) {
	id, msg := ex.resolve(c.Target, true)
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
	if !e.Landed {
		// An abandoned entry has no resolved home to restamp at — rescue
		// it first, then rewrite (§8.3 rescue fold).
		h := ex.handles[id]
		return nil, fmt.Sprintf("#%s abandoned — rescue with k:#%s:path first, or retire with d", h, h)
	}
	path, msg := ex.restampPath(id)
	if msg != "" {
		return nil, msg
	}
	anchor, msg := ex.stampAnchor(path)
	if msg != "" {
		return nil, msg
	}
	recID, err := ex.s.Minter.MintRecID()
	if err != nil {
		return nil, err.Error()
	}
	su := record.Supersede{
		Rec:    recID,
		Entry:  id,
		Subj:   e.Subj,
		Anchor: anchor,
		Origin: ex.s.Head,
		Path:   path,
		Body:   c.Body,
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

// tombstone executes `d` (§6.1): terminal, absorbing (§8.3). A repair
// verb — worklisted entries are retired with it (§9.6).
func (ex *executor) tombstone(c CmdTombstone) (*view.Ack, string) {
	id, msg := ex.resolve(c.Target, true)
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
// body revision. Plain `k` blesses dm's guess — the entry's resolved home
// (§9.3); the optional path names the home explicitly (the split case, or
// re-homing a worklisted orphan).
func (ex *executor) reAnchor(c CmdReAnchor) (*view.Ack, string) {
	id, msg := ex.resolve(c.Target, true)
	if msg != "" {
		return nil, msg
	}
	e, err := ex.fold(id)
	if err != nil {
		return nil, err.Error()
	}
	path := c.Path
	if path == "" {
		if !e.Landed {
			h := ex.handles[id]
			return nil, fmt.Sprintf("#%s abandoned — name the new home: k:#%s:path", h, h)
		}
		if path, msg = ex.restampPath(id); msg != "" {
			return nil, msg
		}
	} else if e.Landed && e.Anchor.IsPath() != path.IsFolder() {
		// An abandoned rescue has no landed anchor to compare kinds with —
		// the named path decides (§8.3: k mints an RA with a live origin).
		return nil, fmt.Sprintf("#%s: entry and target disagree on file vs folder (trailing /)", ex.handles[id])
	}
	anchor, msg := ex.stampAnchor(path)
	if msg != "" {
		return nil, msg
	}
	recID, err := ex.s.Minter.MintRecID()
	if err != nil {
		return nil, err.Error()
	}
	ra := record.ReAnchor{
		Rec:    recID,
		Entry:  id,
		Anchor: anchor,
		Origin: ex.s.Head,
		Path:   path,
	}
	if err := ex.commit(ra); err != nil {
		return nil, err.Error()
	}
	return &view.Ack{Verb: view.AckReAnchored, Handle: ex.handles[id]}, ""
}

// feedback executes `f` (§7.3): an FB log record, never a counter. Not a
// repair verb — worklisted entries refuse it (§5.3).
func (ex *executor) feedback(c CmdFeedback) (*view.Ack, string) {
	id, msg := ex.resolve(c.Target, false)
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
	from, msg := ex.resolve(c.From, false)
	if msg != "" {
		return nil, msg
	}
	to, msg := ex.resolve(c.To, false)
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
	from, msg := ex.resolve(c.From, false)
	if msg != "" {
		return nil, msg
	}
	to, msg := ex.resolve(c.To, false)
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
		if !e.Path.Under(path) {
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
			rel := string(e.Path)
			if c.Old == record.RootFolder {
				if e.Path == record.RootFolder {
					rel = ""
				}
			} else {
				rel = strings.TrimPrefix(rel, string(c.Old))
			}
			newPath = c.New + record.Path(rel)
		}
		exists, what, err := ex.destinationExists(newPath)
		if err != nil {
			return nil, err.Error()
		}
		if !exists {
			items = append(items, MacroItem{Err: fmt.Sprintf("#%s: %s: no such %s in working tree",
				ex.handles[id], record.EncodePath(newPath), what)})
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

// verdict executes `vd` (§9.4): manual landing disposition, stamped m5.
// The subject bulk-binds its whole segment when its objects are locally
// reachable — every store ∪ pending origin and chained landed-as commit in
// (merge-base..subject] gets a VD — or binds/voids the single origin
// otherwise (the evidence-destroyed case, §11.4). Manual judgment
// overrides inference: existing bindings lose by LWW, and the ladder never
// re-mints over a winning verdict.
func (ex *executor) verdict(c CmdVerdict) (*view.Ack, string) {
	subject, msg := ex.resolveSHA(c.Subject)
	if msg != "" {
		return nil, msg
	}
	if !c.Landed {
		if err := ex.mintVerdict(record.Verdict{Origin: subject}); err != nil {
			return nil, err.Error()
		}
		return &view.Ack{Verb: view.AckVdUnlanded, Subject: subject}, ""
	}
	landedAs, msg := ex.resolveSHA(c.LandedAs)
	if msg != "" {
		return nil, msg
	}
	if subject == landedAs {
		return nil, "vd: subject and landed-as are the same commit"
	}
	// Bulk-bind when the segment is enumerable (both endpoints local).
	bound := []lineage.Binding{{Origin: subject, LandedAs: landedAs, Matcher: record.MatcherManual}}
	if ex.hasCommit(subject) && ex.hasCommit(landedAs) {
		candidates := append(ex.bindCandidates(true), subject)
		bindings, err := lineage.BindSegment(ex.ev, subject, landedAs, candidates, record.MatcherManual)
		if err != nil {
			return nil, err.Error()
		}
		if len(bindings) > 0 {
			bound = bindings
		}
	}
	for _, b := range bound {
		if err := ex.mintVerdict(record.Verdict{Origin: b.Origin, Landed: true, LandedAs: b.LandedAs, Matcher: b.Matcher}); err != nil {
			return nil, err.Error()
		}
	}
	return &view.Ack{Verb: view.AckVdLanded, Subject: landedAs, Count: len(bound)}, ""
}

// resolveSHA expands a (possibly abbreviated) commit sha against the local
// odb; an unknown full-length sha passes verbatim — a degraded clone may
// judge origins it never fetched (§9.4 m5) — while an unknown abbreviation
// errors, since dm cannot know which commit it names.
func (ex *executor) resolveSHA(sha record.SHA) (record.SHA, string) {
	full, err := ex.s.Repo.ResolveCommit(sha)
	if err != nil {
		return "", err.Error()
	}
	if full != nil {
		return *full, ""
	}
	if len(sha) < 40 {
		return "", fmt.Sprintf("%s: unknown commit (abbreviated — use the full sha for commits this clone lacks)", sha)
	}
	return sha, ""
}

func (ex *executor) hasCommit(sha record.SHA) bool {
	full, err := ex.s.Repo.ResolveCommit(sha)
	return err == nil && full != nil
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

// destinationExists checks an mv destination against the working tree:
// the file, or a non-empty folder, must exist (§6.5 — mv describes a move
// that happened; it does not invent one).
func (ex *executor) destinationExists(path record.Path) (bool, string, error) {
	if path.IsFolder() {
		under, err := ex.ev.PathsUnder(path)
		return len(under) > 0, "folder", err
	}
	blob, err := ex.ev.WorkingBlob(path)
	return blob != nil, "file", err
}

// ---- reads ----

// read executes `r:path` (§5.1, §5.4, §8.6): own entries are the notes
// that resolve to this node (rule (a) layers 1–4, §9.1–§9.3), flagged per
// resolution + dispute; parent folder notes cover the node recursively
// (§3.2), split candidates included flagged (§9.3); the link dimension
// shows handles + labels; folder reads add the child inventory. Lists rank
// per §5.6; a depth modifier inlines parent then link bodies within the
// token budget. Every entry shown bumps its impression counter (§7.1).
func (ex *executor) read(c CmdRead) (*view.Surface, string) {
	surface := &view.Surface{Node: c.Path, Depth: c.Depth}
	shown := make(map[record.EntryID]bool)
	var ownIDs []record.EntryID
	children := make(map[record.Path][]int) // child → counts in view.SubjectOrder
	bodyLines := func(body string) int { return strings.Count(body, "\n") + 1 }
	for _, id := range ex.order {
		e, err := ex.fold(id)
		if err != nil {
			return nil, err.Error()
		}
		if e.Deleted || !e.Landed {
			continue
		}
		res, err := ex.resolution(id)
		if err != nil {
			return nil, err.Error()
		}
		if !res.State.Visible() {
			continue
		}
		flags := view.Flags(res.State, e.Disputed)
		if !e.Anchor.IsPath() {
			// File note: own iff it resolves to this exact node; part of
			// the child inventory when the read folder contains it (§5.4).
			if res.Path == c.Path {
				surface.Own = append(surface.Own, view.Preview(e.Subj, ex.handles[id], e.Body, flags))
				ownIDs = append(ownIDs, id)
				shown[id] = true
			} else if c.Path.IsFolder() && res.Path.Under(c.Path) {
				addChild(children, childOf(c.Path, res.Path), e.Subj)
				surface.Hidden += bodyLines(e.Body)
			}
			continue
		}
		// Folder note: own at its home node, parent for everything under
		// its home, child inventory below the read folder; a split covers
		// reads under every candidate (§9.3).
		for _, home := range folderHomes(res) {
			if home == c.Path {
				surface.Own = append(surface.Own, view.Preview(e.Subj, ex.handles[id], e.Body, flags))
				ownIDs = append(ownIDs, id)
				shown[id] = true
				break
			}
			if c.Path.Under(home) {
				surface.Parents = append(surface.Parents, view.ParentPreview{
					Subj: e.Subj, Handle: ex.handles[id], Path: home, Flags: flags,
					Body: e.Body, Lines: bodyLines(e.Body),
				})
				shown[id] = true
				break
			}
			if c.Path.IsFolder() && home != c.Path && home.Under(c.Path) {
				addChild(children, childOf(c.Path, home), e.Subj)
				surface.Hidden += bodyLines(e.Body)
				break
			}
		}
	}
	view.RankOwn(surface.Own)
	view.RankParents(surface.Parents)
	surface.Children = childLines(children)
	linkItems, err := ex.linkItems(ownIDs, shown)
	if err != nil {
		return nil, err.Error()
	}
	surface.Links = linkItems
	edges, err := ex.countLinks(shown)
	if err != nil {
		return nil, err.Error()
	}
	surface.LinkEdges = edges

	// Depth modifier (§5.4): inline parent bodies at depth ≥ 1, link
	// bodies at depth ≥ 2, degrading to collapsed once the budget is
	// spent. Hidden tallies what stayed collapsed (§5.2).
	budget := view.DepthBudgetLines
	for i := range surface.Parents {
		p := &surface.Parents[i]
		if c.Depth >= 1 && p.Lines <= budget {
			budget -= p.Lines
		} else {
			surface.Hidden += p.Lines
			p.Body = ""
		}
	}
	for i := range surface.Links {
		ln := &surface.Links[i]
		if c.Depth >= 2 && ln.Lines <= budget {
			budget -= ln.Lines
		} else {
			surface.Hidden += ln.Lines
			ln.Body = ""
		}
	}
	for _, p := range surface.Own {
		surface.Hidden += p.MoreLines
	}
	for id := range shown {
		ex.s.BumpImpression(id)
	}
	return surface, ""
}

// childOf maps a path under the read folder to its immediate child: the
// path itself when directly under, the first subfolder otherwise (§5.4's
// child inventory navigates down one level).
func childOf(folder, p record.Path) record.Path {
	rel := string(p)
	if folder != record.RootFolder {
		rel = strings.TrimPrefix(rel, string(folder))
	}
	if idx := strings.Index(rel, "/"); idx >= 0 && idx < len(rel)-1 {
		rel = rel[:idx+1]
	}
	if folder == record.RootFolder {
		return record.Path(rel)
	}
	return folder + record.Path(rel)
}

func addChild(children map[record.Path][]int, child record.Path, subj record.Subject) {
	counts := children[child]
	if counts == nil {
		counts = make([]int, len(view.SubjectOrder))
	}
	for i, s := range view.SubjectOrder {
		if s == subj {
			counts[i]++
		}
	}
	children[child] = counts
}

func childLines(children map[record.Path][]int) []view.ChildLine {
	paths := make([]record.Path, 0, len(children))
	for p := range children {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i] < paths[j] })
	var out []view.ChildLine
	for _, p := range paths {
		line := view.ChildLine{Path: p}
		for i, n := range children[p] {
			if n > 0 {
				line.Counts = append(line.Counts, view.SubjectCount{Subj: view.SubjectOrder[i], N: n})
			}
		}
		out = append(out, line)
	}
	return out
}

// linkItems assembles the surface's link dimension (§5.1): the far
// endpoint of every live edge touching an own entry, deduped, labeled by
// the link comment or the far entry's first body line.
func (ex *executor) linkItems(ownIDs []record.EntryID, shown map[record.EntryID]bool) ([]view.LinkItem, error) {
	own := make(map[record.EntryID]bool, len(ownIDs))
	for _, id := range ownIDs {
		own[id] = true
	}
	var items []view.LinkItem
	seen := make(map[record.EntryID]bool)
	for _, ln := range ex.linkSet() {
		var far record.EntryID
		switch {
		case own[ln.From]:
			far = ln.To
		case own[ln.To]:
			far = ln.From
		default:
			continue
		}
		if seen[far] || own[far] {
			continue
		}
		ok, err := ex.live(far)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		seen[far] = true
		shown[far] = true
		e, err := ex.fold(far)
		if err != nil {
			return nil, err
		}
		label := ln.Comment
		if label == "" {
			label, _, _ = strings.Cut(e.Body, "\n")
		}
		items = append(items, view.LinkItem{
			Handle: ex.handles[far],
			Subj:   e.Subj,
			Label:  label,
			Body:   e.Body,
			Lines:  strings.Count(e.Body, "\n") + 1,
		})
	}
	return items, nil
}

// folderHomes lists where a folder note currently applies: its resolved
// home, or every split candidate (§9.3 — the readers under the candidates
// are the ones with context to resolve it).
func folderHomes(res resolve.Resolution) []record.Path {
	if res.State == resolve.Split {
		homes := make([]record.Path, len(res.Vote.Candidates))
		for i, c := range res.Vote.Candidates {
			homes[i] = c.Path
		}
		return homes
	}
	if res.Path != "" {
		return []record.Path{res.Path}
	}
	return nil
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
// links one level and relations, with flags and — for inferred follows —
// the vote evidence, shown instead of re-derived (§9.3). Link endpoints
// that are not visible here are omitted (§5.3; dangling ids are ignorable,
// §8.7).
func (ex *executor) expand(c CmdRead) (*view.Expansion, string) {
	id, msg := ex.resolve(c.Target, false)
	if msg != "" {
		return nil, msg
	}
	e, err := ex.fold(id)
	if err != nil {
		return nil, err.Error()
	}
	res, err := ex.resolution(id)
	if err != nil {
		return nil, err.Error()
	}
	node := e.Path // the split case keeps the folded home as the display node
	if res.Path != "" {
		node = res.Path
	}
	exp := &view.Expansion{
		Node:   node,
		Subj:   e.Subj,
		Handle: ex.handles[id],
		Flags:  view.Flags(res.State, e.Disputed),
		Body:   e.Body,
	}
	// The real click (§7.1): count the expansion, refresh last-expanded.
	ex.s.BumpExpansion(id)
	if res.State != resolve.Fresh {
		exp.Vote = res.Vote // judgment evidence rides the drill (§9.3)
	}
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

// search executes `s:path:terms` (§5.5): grep the node's entire
// read-context — own notes, all parent folder notes, linked entries one
// level out — returning matches as handles + snippets. Matches bump the
// search-hit counter, a distinct signal from impressions (§7.1).
func (ex *executor) search(c CmdSearch) (*view.SearchResult, string) {
	context, err := ex.contextEntries(c.Path)
	if err != nil {
		return nil, err.Error()
	}
	result := &view.SearchResult{Node: c.Path, Searched: len(context)}
	for _, id := range context {
		e, err := ex.fold(id)
		if err != nil {
			return nil, err.Error()
		}
		res, err := ex.resolution(id)
		if err != nil {
			return nil, err.Error()
		}
		snippet, more, ok := view.MatchTerms(e.Body, c.Terms)
		if !ok {
			continue
		}
		home := e.Path
		if res.Path != "" {
			home = res.Path
		}
		result.Matches = append(result.Matches, view.SearchMatch{
			Handle:  ex.handles[id],
			Subj:    e.Subj,
			Path:    home,
			Snippet: snippet,
			More:    more,
		})
		ex.s.BumpSearchHit(id)
	}
	return result, ""
}

// contextEntries assembles a node's read-context in disclosure order: own
// entries, covering parent folder notes, then entries linked to any of
// those, one level, deduped — the set `s` greps instead of the agent
// expanding every parent (§5.5).
func (ex *executor) contextEntries(path record.Path) ([]record.EntryID, error) {
	var context []record.EntryID
	in := make(map[record.EntryID]bool)
	add := func(id record.EntryID) {
		if !in[id] {
			in[id] = true
			context = append(context, id)
		}
	}
	var parents []record.EntryID
	for _, id := range ex.order {
		e, err := ex.fold(id)
		if err != nil {
			return nil, err
		}
		if e.Deleted || !e.Landed {
			continue
		}
		res, err := ex.resolution(id)
		if err != nil {
			return nil, err
		}
		if !res.State.Visible() {
			continue
		}
		if !e.Anchor.IsPath() {
			if res.Path == path {
				add(id)
			}
			continue
		}
		for _, home := range folderHomes(res) {
			if home == path {
				add(id)
				break
			}
			if path.Under(home) {
				parents = append(parents, id)
				break
			}
		}
	}
	for _, id := range parents {
		add(id)
	}
	// Linked one level out from the context so far, live endpoints only.
	direct := append([]record.EntryID(nil), context...)
	for _, seed := range direct {
		for _, ln := range ex.linkSet() {
			var far record.EntryID
			switch seed {
			case ln.From:
				far = ln.To
			case ln.To:
				far = ln.From
			default:
				continue
			}
			ok, err := ex.live(far)
			if err != nil {
				return nil, err
			}
			if ok {
				add(far)
			}
		}
	}
	return context, nil
}
