package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The §11.4 discovery scenario group — M7's retrieval polish: surface
// previews with cost hints, ranking (§5.6), drill-by-handle, the depth
// modifier (§5.4), the folder child inventory, and context search (§5.5).

// newDiscoveryRepo builds the disclosure fixture slice: a file with
// mixed-subject own notes, an arch folder note covering it, and a linked
// note in another folder (§11.2's disclosure dimensions, inline).
// Creation order is d-note before c-note, so ranking is observable.
func newDiscoveryRepo(t *testing.T) (*Repo, map[string]string) {
	t.Helper()
	r := NewRepo(t)
	r.WriteFile("api/handler.rb", "class Handler\nend\n")
	r.WriteFile("api/admin.rb", "class Admin\nend\n")
	r.WriteFile("db/schema.rb", "Schema.define do\nend\n")
	r.Commit("base")
	r.MustDM("", "init")

	res := r.MustDM("a:api/handler.rb:d:bin/test runs handler specs\n" +
		"a:api/handler.rb:c:Validates tenant header before dispatch\\\n" +
			"and rejects missing tenants\\\n" +
			"before any handler code runs\n" +
		"a:api/:a:api reaches db only through repo/\\\n" +
			"handlers never import models directly\n" +
		"a:db/schema.rb:c:single-table inheritance for tenants\n" +
		"al:$2:$4:tenant model background\n")
	hs := createdHandles(t, res.Stdout)
	if len(hs) != 4 {
		t.Fatalf("want 4 creates, got %v", hs)
	}
	return r, map[string]string{"d": hs[0], "c": hs[1], "arch": hs[2], "db": hs[3]}
}

func TestDiscoverySurface(t *testing.T) {
	r, h := newDiscoveryRepo(t)

	// The surface (§5.1): own previews ranked by subject priority (c
	// before d despite creation order), first line + hidden-size hint,
	// collapsed parent with the arch callout, the link dimension as
	// handle + label, and the inventory footer. Golden, end to end.
	got := r.MustDM("r:api/handler.rb\n").Stdout
	want := "▸r:api/handler.rb\n" +
		"c #" + h["c"] + " Validates tenant header before dispatch (+2 lines)\n" +
		"d #" + h["d"] + " bin/test runs handler specs\n" +
		"↑ 1 parent note (1 arch) #" + h["arch"] + " api/\n" +
		"→ #" + h["db"] + " tenant model background\n" +
		"context: 2 own · 1 parent · 1 link · ~5 hidden\n" +
		"◾1 ok\n"
	if got != want {
		t.Errorf("surface:\n%q\nwant:\n%q", got, want)
	}

	// Drill by handle (§5.3): the full body of one entry — expansion is
	// always the same move.
	got = r.MustDM("r:#" + h["c"] + "\n").Stdout
	if !strings.Contains(got, "api/handler.rb [c] #"+h["c"]+"\n"+
		"Validates tenant header before dispatch\nand rejects missing tenants\nbefore any handler code runs\n") {
		t.Errorf("drill:\n%q", got)
	}
}

func TestDiscoveryRankingFlagDemotion(t *testing.T) {
	r, h := newDiscoveryRepo(t)

	// Flag state is the second ranking key (§5.6): a disputed note sinks
	// below unflagged ones within its proximity group, unflagged order
	// untouched — readers see healthy notes first regardless of store
	// debt (§9.6 layer 1).
	r.MustDM("f:#" + h["c"] + ":!:tenant header moved to middleware\n")
	got := r.MustDM("r:api/handler.rb\n").Stdout
	want := "d #" + h["d"] + " bin/test runs handler specs\n" +
		"c #" + h["c"] + " Validates tenant header before dispatch (+2 lines) ⚠disputed\n"
	if !strings.Contains(got, want) {
		t.Errorf("flag demotion:\n%q\nwant to contain:\n%q", got, want)
	}
}

func TestDiscoveryDepthModifier(t *testing.T) {
	r, h := newDiscoveryRepo(t)

	// Depth 1 (§5.4): parent notes inline, link bodies stay collapsed as
	// labels — orientation in fewer round-trips.
	got := r.MustDM("r:api/handler.rb:1\n").Stdout
	if !strings.Contains(got, "↑ a #"+h["arch"]+" api/\n"+
		"api reaches db only through repo/\nhandlers never import models directly\n") {
		t.Errorf("depth 1 should inline the parent body:\n%q", got)
	}
	if !strings.Contains(got, "→ #"+h["db"]+" tenant model background\n") ||
		strings.Contains(got, "single-table inheritance") {
		t.Errorf("depth 1 must keep link bodies collapsed:\n%q", got)
	}

	// Depth 2: link bodies expand one level too.
	got = r.MustDM("r:api/handler.rb:2\n").Stdout
	if !strings.Contains(got, "→ #"+h["db"]+" tenant model background\n"+
		"single-table inheritance for tenants\n") {
		t.Errorf("depth 2 should expand the link body:\n%q", got)
	}
}

func TestDiscoveryFolderChildInventory(t *testing.T) {
	r, h := newDiscoveryRepo(t)

	// A folder read navigates down (§5.4): the folder's own notes plus a
	// child inventory — which subpaths carry notes, counts by subject —
	// never the child notes themselves.
	got := r.MustDM("r:api/\n").Stdout
	want := "a #" + h["arch"] + " api reaches db only through repo/ (+1 lines)\n" +
		"↓ api/handler.rb 1c 1d\n" +
		"context: 1 own · 0 parent · 0 links · ~5 hidden\n"
	if !strings.Contains(got, want) {
		t.Errorf("folder read:\n%q\nwant to contain:\n%q", got, want)
	}
	if strings.Contains(got, "Validates tenant header") {
		t.Errorf("folder read must not include child notes:\n%q", got)
	}
}

func TestDiscoverySearch(t *testing.T) {
	r, h := newDiscoveryRepo(t)

	// `s` greps the file's entire read-context (§5.5): own notes, parent
	// folder notes, linked entries — the escape hatch when a file sits
	// under many arch notes. Matches are handles + snippets; the linked
	// db note matches without ever being expanded.
	got := r.MustDM("s:api/handler.rb:tenant|repo/\n").Stdout
	want := "▸s:api/handler.rb:tenant|repo/\n" +
		"#" + h["c"] + " c api/handler.rb …Validates tenant header before dispatch… (+1 more)\n" +
		"#" + h["arch"] + " a api/ …api reaches db only through repo/…\n" +
		"#" + h["db"] + " c db/schema.rb …single-table inheritance for tenants…\n" +
		"3 matches · searched 4 entries in context\n" +
		"◾1 ok\n"
	if got != want {
		t.Errorf("search:\n%q\nwant:\n%q", got, want)
	}

	// `+` binds tighter than `|` (§5.5): `a+b|c` is (a AND b) OR c.
	got = r.MustDM("s:api/handler.rb:tenant+dispatch|specs\n").Stdout
	if !strings.Contains(got, "#"+h["c"]+" c api/handler.rb") ||
		!strings.Contains(got, "#"+h["d"]+" d api/handler.rb") ||
		strings.Contains(got, "#"+h["db"]) {
		t.Errorf("AND/OR grammar:\n%q", got)
	}
	if !strings.Contains(got, "2 matches · searched 4 entries in context\n") {
		t.Errorf("search footer:\n%q", got)
	}

	// No match: the footer still reports the searched context size.
	got = r.MustDM("s:api/handler.rb:nonexistent\n").Stdout
	if !strings.Contains(got, "0 matches · searched 4 entries in context\n") {
		t.Errorf("empty search:\n%q", got)
	}
}

func TestDiscoveryStatsCollected(t *testing.T) {
	r, h := newDiscoveryRepo(t)

	// Reads mutate state (§7): the surface read above logs impressions,
	// the drill an expansion + last-expanded, the search a search-hit —
	// all as pending deltas that ride the next sync (§8.1). The dump
	// shows the merged store ∪ pending stat rows (§11.3).
	r.MustDM("r:api/handler.rb\nr:#" + h["c"] + "\ns:db/schema.rb:tenant\n")
	dump := r.MustDM("", "dump").Stdout
	// The c note: 1 impression (surface), 1 expansion with a timestamp,
	// 1 search-hit (it is in db/schema.rb's context via the link and
	// matches "tenant").
	cRow := regexp.MustCompile(`stat E2EREPL1 \S+ i:1 x:1 h:1 t:[1-9]\d*\n`)
	if !cRow.MatchString(dump) {
		t.Errorf("dump lacks the expanded note's row:\n%s", dump)
	}
	// The db note: 1 impression (shown collapsed on the surface's link
	// dimension) and 1 search-hit — distinct counters, never correlated
	// (§7.1) — but never expanded: x and t stay zero.
	dbRow := regexp.MustCompile(`stat E2EREPL1 \S+ i:1 x:0 h:1 t:0\n`)
	if !dbRow.MatchString(dump) {
		t.Errorf("dump lacks the search-hit row:\n%s", dump)
	}

	// Stats travel: sync folds the deltas into this replica's register
	// file in the store tree and clears the accumulator (§8.1, §8.4).
	r.MustDM("", "sync")
	if _, err := os.Stat(filepath.Join(r.DMDir(), "pending", "stats")); err == nil {
		t.Errorf("pending stats survived the push")
	}
	if tree := r.Git("ls-tree", "-r", "--name-only", "refs/dm/store"); !strings.Contains(tree, "stats/E2EREPL1") {
		t.Errorf("store tree lacks the replica stats file:\n%s", tree)
	}
	after := r.MustDM("", "dump").Stdout
	if !cRow.MatchString(after) {
		t.Errorf("stat rows lost across sync:\n%s", after)
	}
}
