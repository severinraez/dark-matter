package e2e

import (
	"strings"
	"testing"
)

// The §11.4 hygiene scenario group — the §9.6 soft-pressure layers: flag
// demotion rides the discovery tests (§5.6); here the crowding nudge, the
// session-scoped worklist, and the sync health line.

func TestHygieneCrowdingNudge(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("busy.rb", "class Busy\nend\n")
	r.Commit("base")
	r.MustDM("", "init")

	// Nine creates on one node: the nudge appears exactly at the
	// threshold — the ninth ack, and only it, carries the fold hint —
	// and never errors (§9.6 layer 2, §4.3: still one line).
	var b strings.Builder
	for i := 1; i <= 9; i++ {
		b.WriteString("a:busy.rb:c:note number " + strings.Repeat("i", i) + "\n")
	}
	res := r.MustDM(b.String())
	if !strings.HasSuffix(res.Stdout, "◾9 ok\n") {
		t.Fatalf("crowded batch must not error:\n%q", res.Stdout)
	}
	nudge := " · node has 9 notes, consider folding with u\n"
	if strings.Count(res.Stdout, "consider folding") != 1 || !strings.Contains(res.Stdout, nudge) {
		t.Errorf("nudge should fire exactly once, at the threshold:\n%q", res.Stdout)
	}
	handles := createdHandles(t, res.Stdout)
	if !strings.Contains(res.Stdout, "+ #"+handles[7]+" created\n") {
		t.Errorf("the eighth ack must be nudge-free:\n%q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "+ #"+handles[8]+" created"+nudge) {
		t.Errorf("the ninth ack carries the nudge:\n%q", res.Stdout)
	}
}

func TestHygieneScopedWorklistAndHealthLine(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("api/x.rb", "class ApiX\n  def unique_x; end\nend\n")
	r.WriteFile("api/z.rb", "class ApiZ\n  def z1; end\n  def z2; end\n  def z3; end\n  def z4; end\n  def z5; end\n  def z6; end\n  def z7; end\n  def z8; end\nend\n")
	r.WriteFile("lib/y.rb", "module LibY\n  def unique_y; end\nend\n")
	r.Commit("base")
	r.MustDM("", "init")
	res := r.MustDM("a:api/x.rb:c:X gotcha\na:lib/y.rb:c:Y gotcha\n")
	hs := createdHandles(t, res.Stdout)
	hx, hy := hs[0], hs[1]
	r.MustDM("", "sync") // share, clearing the session context

	// Both noted files deleted: two orphans (§9.1 layer 5), worklisted,
	// never destroyed.
	r.Git("rm", "-q", "api/x.rb", "lib/y.rb")
	r.commits++
	r.Git("commit", "-q", "-m", "drop noted files")

	// No session context to scope by → the listing is global.
	wl := r.MustDM("", "worklist").Stdout
	if !strings.Contains(wl, "#"+hx+" c api/x.rb orphaned\n") ||
		!strings.Contains(wl, "#"+hy+" c lib/y.rb orphaned\n") ||
		!strings.Contains(wl, "2 to review\n") {
		t.Errorf("scopeless worklist should list everything:\n%q", wl)
	}

	// A write near api/ scopes the session (§9.6 layer 3): the default
	// listing leads with the orphan near that work — derived from the
	// pending records, no new state — and counts the remainder.
	res = r.MustDM("a:api/z.rb:c:Z gotcha\n")
	hz := createdHandles(t, res.Stdout)[0]
	wl = r.MustDM("", "worklist").Stdout
	if !strings.Contains(wl, "#"+hx+" c api/x.rb orphaned\n") ||
		strings.Contains(wl, "lib/y.rb") ||
		!strings.Contains(wl, "1 to review · 1 elsewhere\n") {
		t.Errorf("scoped worklist:\n%q\nwant the api/ orphan led, lib/ counted", wl)
	}
	// The flag lists everything.
	if wl := r.MustDM("", "worklist", "--all").Stdout; !strings.Contains(wl, "lib/y.rb") ||
		!strings.Contains(wl, "2 to review\n") {
		t.Errorf("worklist --all:\n%q", wl)
	}

	// Stale notes list on request only (§9.6): drift z, and the default
	// listing is unchanged while --stale adds it.
	r.WriteFile("api/z.rb", "class ApiZ\n  def z1; end\n  def z2; end\n  def z3; end\n  def z4; end\n  def z5; end\n  def z6; end\n  def z7; end\n  def z8; end\n  def drifted; end\nend\n")
	wl = r.MustDM("", "worklist").Stdout
	if strings.Contains(wl, "#"+hz) || !strings.Contains(wl, "1 to review · 1 elsewhere\n") {
		t.Errorf("default worklist must withhold stale-only items:\n%q", wl)
	}
	wl = r.MustDM("", "worklist", "--stale").Stdout
	if !strings.Contains(wl, "#"+hz+" c api/z.rb stale\n") ||
		!strings.Contains(wl, "2 to review · 1 elsewhere\n") {
		t.Errorf("worklist --stale:\n%q", wl)
	}

	// The sync health line matches the worklist counts (§8.4): scoped by
	// the work this sync shares (the pending Z note), stale included —
	// the backlog is surfaced at every share point.
	got := r.MustDM("", "sync").Stdout
	if !strings.HasSuffix(got, "1 stale · 1 orphaned near your work · 1 elsewhere\n") {
		t.Errorf("sync health line:\n%q", got)
	}

	// Session scope also derives from the read-stat accumulator: an
	// expansion near api/ re-scopes the next worklist (§9.6 — items land
	// in front of the session best equipped to judge them).
	r.MustDM("r:#" + hz + "\n")
	wl = r.MustDM("", "worklist").Stdout
	if !strings.Contains(wl, "#"+hx+" c api/x.rb orphaned\n") ||
		strings.Contains(wl, "lib/y.rb") ||
		!strings.Contains(wl, "1 to review · 1 elsewhere\n") {
		t.Errorf("stat-accumulator scoping:\n%q", wl)
	}
}
