package reconcile

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/jschell12/workforce/internal/registry"
	"github.com/jschell12/workforce/internal/session"
)

type fakeStore struct {
	entries []registry.Entry
	removed []string
}

func (f *fakeStore) Read() ([]registry.Entry, error) { return f.entries, nil }
func (f *fakeStore) Remove(e registry.Entry) error {
	if !e.OwnedByWF() {
		return fmt.Errorf("refused: not ours")
	}
	f.removed = append(f.removed, e.AgentID)
	return nil
}

type fakeForge struct {
	state  map[string]string // pr -> state
	signed map[string]bool   // pr -> verdict at head exists
	fail   bool
}

func (f fakeForge) PRState(_, pr string) (string, error) {
	if f.fail {
		return "", fmt.Errorf("network")
	}
	s, ok := f.state[pr]
	if !ok {
		return "", fmt.Errorf("no such pr")
	}
	return s, nil
}
func (f fakeForge) SignedAt(_, pr, _ string) (bool, error) {
	if f.fail {
		return false, fmt.Errorf("network")
	}
	return f.signed[pr], nil
}

type fakeCtl struct{ stopped, removed []string }

func (c *fakeCtl) Stop(r string) error   { c.stopped = append(c.stopped, r); return nil }
func (c *fakeCtl) Remove(r string) error { c.removed = append(c.removed, r); return nil }

func ours(e registry.Entry) registry.Entry { e.ManagedBy = registry.Marker; return e }

func run(t *testing.T, entries []registry.Entry, live []session.Session, forge Forge, dry bool) (Result, *fakeStore, *fakeCtl, string) {
	t.Helper()
	st := &fakeStore{entries: entries}
	ctl := &fakeCtl{}
	var buf bytes.Buffer
	repos := []Repo{{Name: "alpha", Slug: "o/alpha", Path: "/tmp/alpha", Store: st}}
	res, err := Run(repos, session.Plain(live), nil, forge, ctl, Options{DryRun: dry, Out: &buf})
	if err != nil {
		t.Fatal(err)
	}
	return res, st, ctl, buf.String()
}

// The incident: the first dry run against a real registry proposed reaping ten
// foreign entries alongside its own three. Deleting one deregisters a live
// agent mid-task.
func TestForeignEntriesAreNeverTouched(t *testing.T) {
	entries := []registry.Entry{
		{AgentID: "foreign1", Name: "other-agent", Worktree: "/tmp/x"}, // protocol schema: no bg_id, no marker
		ours(registry.Entry{AgentID: "mine0001", BgID: "aaaaaaaa", Name: "alpha-k3n8", Role: "worker"}),
	}
	res, st, _, out := run(t, entries, nil, fakeForge{}, false)
	if len(st.removed) != 1 || st.removed[0] != "mine0001" {
		t.Fatalf("only wf's own entry may be removed, removed %v", st.removed)
	}
	if strings.Contains(out, "other-agent") {
		t.Errorf("a foreign entry should not even be reported as actionable:\n%s", out)
	}
	if res.Reaped != 1 {
		t.Errorf("want 1 reaped, got %d", res.Reaped)
	}
}

// A pre-marker entry is recognisable by a shape the protocol schema lacks.
func TestLegacyEntriesRecognisedByShape(t *testing.T) {
	e := registry.Entry{AgentID: "old00001", BgID: "aaaaaaaa", TokenKey: "store:K", Name: "old"}
	if !ownedByWF(e) {
		t.Error("bg_id plus token_key is a wf entry written before the marker existed")
	}
	if ownedByWF(registry.Entry{AgentID: "x", Worktree: "/tmp", Branch: "b"}) {
		t.Error("a protocol entry must not be claimed")
	}
}

func TestReviewerRetiredWhenPRClosed(t *testing.T) {
	entries := []registry.Entry{ours(registry.Entry{
		AgentID: "r1", BgID: "aaaaaaaa", Name: "rev-k3n8-7", Role: "reviewer", PR: "7"})}
	live := []session.Session{{ID: "aaaaaaaa", Name: "rev-k3n8-7", State: "idle"}}
	res, st, ctl, out := run(t, entries, live, fakeForge{state: map[string]string{"7": "MERGED"}}, false)
	if res.Retired != 1 || len(ctl.stopped) != 1 || len(st.removed) != 1 {
		t.Fatalf("want a retire; got %+v stopped=%v removed=%v", res, ctl.stopped, st.removed)
	}
	if !strings.Contains(out, "PR #7 is MERGED") {
		t.Errorf("reason should be stated: %s", out)
	}
}

// A verdict may be posted before the session has finished writing up. Stopping
// it there truncates the one artifact anyone reads.
func TestSignedReviewerNotRetiredWhileMidTurn(t *testing.T) {
	entries := []registry.Entry{ours(registry.Entry{
		AgentID: "r1", BgID: "aaaaaaaa", Name: "rev-k3n8-7", Role: "reviewer", PR: "7",
		Head: "beef5588b031d6dbce20cb40e0814c829319ff1"})}
	forge := fakeForge{state: map[string]string{"7": "OPEN"}, signed: map[string]bool{"7": true}}

	for _, st := range []string{"working", "busy"} {
		live := []session.Session{{ID: "aaaaaaaa", Name: "rev-k3n8-7", State: st}}
		res, _, ctl, _ := run(t, entries, live, forge, false)
		if res.Retired != 0 || len(ctl.stopped) != 0 {
			t.Errorf("state %q is mid-turn and must not be retired", st)
		}
	}
	live := []session.Session{{ID: "aaaaaaaa", Name: "rev-k3n8-7", State: "idle"}}
	res, _, _, out := run(t, entries, live, forge, false)
	if res.Retired != 1 {
		t.Fatalf("an idle signed reviewer should retire: %+v", res)
	}
	if !strings.Contains(out, "signed at beef5588") {
		t.Errorf("reason should name the head: %s", out)
	}
}

// "cannot read" and "not open" lead to opposite actions. Conflating them
// retires a live reviewer whenever the network hiccups.
func TestForgeFailureSkipsRatherThanRetires(t *testing.T) {
	entries := []registry.Entry{ours(registry.Entry{
		AgentID: "r1", BgID: "aaaaaaaa", Name: "rev-k3n8-7", Role: "reviewer", PR: "7"})}
	live := []session.Session{{ID: "aaaaaaaa", Name: "rev-k3n8-7", State: "idle"}}
	res, st, ctl, out := run(t, entries, live, fakeForge{fail: true}, false)
	if res.Retired != 0 || len(ctl.stopped) != 0 || len(st.removed) != 0 {
		t.Fatalf("an unreadable forge must not retire anything: %+v", res)
	}
	if res.Skipped != 1 || !strings.Contains(out, "cannot read PR #7") {
		t.Errorf("want a stated skip, got %+v\n%s", res, out)
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	entries := []registry.Entry{ours(registry.Entry{
		AgentID: "r1", BgID: "aaaaaaaa", Name: "rev-k3n8-7", Role: "reviewer", PR: "7"})}
	live := []session.Session{{ID: "aaaaaaaa", Name: "rev-k3n8-7", State: "idle"}}
	res, st, ctl, out := run(t, entries, live, fakeForge{state: map[string]string{"7": "CLOSED"}}, true)
	if len(st.removed) != 0 || len(ctl.stopped) != 0 {
		t.Fatal("a dry run must not touch anything")
	}
	if res.Retired != 1 || !strings.Contains(out, "would stop+rm") {
		t.Errorf("a dry run should still report what it would do: %+v\n%s", res, out)
	}
}

// The double-count bug: the orphan pass reads a snapshot taken before the
// registry pass deleted anything, so a session retired above came back round as
// an orphan. It only shows on a real run, because a dry run deletes nothing.
func TestRetiredSessionDoesNotReturnAsAnOrphan(t *testing.T) {
	entries := []registry.Entry{ours(registry.Entry{
		AgentID: "r1", BgID: "aaaaaaaa", Name: "rev-k3n8-7", Role: "reviewer", PR: "7"})}
	live := []session.Session{{ID: "aaaaaaaa", Name: "rev-k3n8-7", State: "idle", Cwd: "/tmp/alpha"}}
	res, _, ctl, out := run(t, entries, live, fakeForge{state: map[string]string{"7": "MERGED"}}, false)
	if res.Retired != 1 {
		t.Fatalf("want exactly one retire, got %d:\n%s", res.Retired, out)
	}
	if len(ctl.stopped) != 1 {
		t.Fatalf("session stopped %d times, want once: %v", len(ctl.stopped), ctl.stopped)
	}
}

// Its trailing number is a timestamp. Reading it as a pull request number would
// stop a live reviewer on a pull request nobody named.
func TestOrphanWithoutAPRNumberIsLeftAlone(t *testing.T) {
	live := []session.Session{{ID: "cccccccc", Name: "reviewer-alpha-79497", State: "idle", Cwd: "/tmp/alpha"}}
	res, _, ctl, out := run(t, nil, live, fakeForge{state: map[string]string{"79497": "MERGED"}}, false)
	if len(ctl.stopped) != 0 {
		t.Fatal("a timestamp must never be read as a PR number")
	}
	if res.Orphans != 1 || !strings.Contains(out, "name carries no PR number") {
		t.Errorf("want it reported and left alone: %+v\n%s", res, out)
	}
}

func TestOrphanRetiredOnlyWhenItsPRIsClosed(t *testing.T) {
	live := []session.Session{{ID: "cccccccc", Name: "rev-k3n8-12", State: "idle", Cwd: "/tmp/alpha"}}

	_, _, ctl, out := run(t, nil, live, fakeForge{state: map[string]string{"12": "OPEN"}}, false)
	if len(ctl.stopped) != 0 || !strings.Contains(out, "still OPEN, left alone") {
		t.Errorf("an open PR's orphan must be left alone:\n%s", out)
	}

	res, _, ctl2, out2 := run(t, nil, live, fakeForge{state: map[string]string{"12": "MERGED"}}, false)
	if len(ctl2.stopped) != 1 || res.Retired != 1 {
		t.Errorf("a closed PR's orphan should be retired: %+v\n%s", res, out2)
	}
}

func TestOrphanInUnknownRepoIsLeftAlone(t *testing.T) {
	live := []session.Session{{ID: "cccccccc", Name: "rev-k3n8-12", State: "idle", Cwd: "/somewhere/else"}}
	_, _, ctl, out := run(t, nil, live, fakeForge{state: map[string]string{"12": "MERGED"}}, false)
	if len(ctl.stopped) != 0 || !strings.Contains(out, "cwd is not a known repo") {
		t.Errorf("want it left alone:\n%s", out)
	}
}

func TestNonReviewerSessionsAreKeptNotRetired(t *testing.T) {
	entries := []registry.Entry{ours(registry.Entry{
		AgentID: "w1", BgID: "aaaaaaaa", Name: "alpha-k3n8", Role: "worker"})}
	live := []session.Session{{ID: "aaaaaaaa", Name: "alpha-k3n8", State: "idle"}}
	res, st, ctl, _ := run(t, entries, live, fakeForge{}, false)
	if res.Kept != 1 || len(st.removed) != 0 || len(ctl.stopped) != 0 {
		t.Errorf("a live worker must be kept: %+v", res)
	}
}

// runAbsent drives only the clear pass.
func runAbsent(t *testing.T, all []session.Session, abs Absence, now int64, dry bool) (Result, *fakeCtl, string) {
	t.Helper()
	ctl := &fakeCtl{}
	var buf bytes.Buffer
	res, err := Run(nil, session.Plain{}, all, fakeForge{}, ctl,
		Options{DryRun: dry, Out: &buf, Absence: abs, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return res, ctl, buf.String()
}

// ABSENT ONCE IS NOT FINISHED. The first version of this deleted a reviewer
// nine minutes after it was spawned, mid-review, and took its transcript with
// it.
func TestAbsentOnceIsNotCleared(t *testing.T) {
	all := []session.Session{{ID: "aaaaaaaa", Name: "rev-k3n8-7", State: "done"}}
	abs := &MemAbsence{}
	res, ctl, out := runAbsent(t, all, abs, 1000, false)
	if res.Cleared != 0 || len(ctl.removed) != 0 {
		t.Fatalf("a first sighting must never clear: %+v", res)
	}
	if !strings.Contains(out, "not clearing before 1800s") {
		t.Errorf("want the wait reported: %s", out)
	}
	if abs.M["aaaaaaaa"] != 1000 {
		t.Errorf("the clock should start at the first sighting, got %v", abs.M)
	}
}

func TestClearedOnlyAfterTheSustainedWindow(t *testing.T) {
	all := []session.Session{{ID: "aaaaaaaa", Name: "rev-k3n8-7", State: "done"}}

	// One second short.
	abs := &MemAbsence{M: map[string]int64{"aaaaaaaa": 1000}}
	res, ctl, _ := runAbsent(t, all, abs, 1000+AbsentBeforeClear-1, false)
	if res.Cleared != 0 || len(ctl.removed) != 0 {
		t.Fatalf("one second short must not clear: %+v", res)
	}

	abs = &MemAbsence{M: map[string]int64{"aaaaaaaa": 1000}}
	res, ctl, out := runAbsent(t, all, abs, 1000+AbsentBeforeClear, false)
	if res.Cleared != 1 || len(ctl.removed) != 1 {
		t.Fatalf("the window having elapsed must clear: %+v", res)
	}
	if !strings.Contains(out, "absent 1800s") {
		t.Errorf("want the age reported: %s", out)
	}
}

// A session that came back must start counting again from zero, not resume an
// old clock.
func TestReturningSessionResetsTheClock(t *testing.T) {
	abs := &MemAbsence{M: map[string]int64{"aaaaaaaa": 1000}}
	// It is live now, so it is not absent and its history must be dropped.
	ctl := &fakeCtl{}
	var buf bytes.Buffer
	live := session.Plain{{ID: "aaaaaaaa", Name: "rev-k3n8-7", State: "idle"}}
	all := []session.Session{{ID: "aaaaaaaa", Name: "rev-k3n8-7", State: "idle"}}
	if _, err := Run(nil, live, all, fakeForge{}, ctl,
		Options{Out: &buf, Absence: abs, Now: 2000}); err != nil {
		t.Fatal(err)
	}
	if _, ok := abs.M["aaaaaaaa"]; ok {
		t.Fatalf("a live session must not keep an absence clock: %v", abs.M)
	}
	// Disappearing later starts from the new sighting, so it is not instantly
	// eligible on the strength of the old one.
	res, ctl2, _ := runAbsent(t, all, abs, 2500, false)
	if res.Cleared != 0 || len(ctl2.removed) != 0 {
		t.Fatalf("the clock must restart, not resume: %+v", res)
	}
}

// Reading a dry run must not bring a real deletion closer.
func TestDryRunDoesNotAdvanceTheClock(t *testing.T) {
	all := []session.Session{{ID: "aaaaaaaa", Name: "rev-k3n8-7", State: "done"}}
	abs := &MemAbsence{}
	res, ctl, out := runAbsent(t, all, abs, 1000, true)
	if len(ctl.removed) != 0 {
		t.Fatal("a dry run must remove nothing")
	}
	if len(abs.M) != 0 {
		t.Fatalf("a dry run must not persist the clock, got %v", abs.M)
	}
	_ = res
	_ = out
}

func TestNonReviewerSessionsAreNeverCleared(t *testing.T) {
	all := []session.Session{{ID: "aaaaaaaa", Name: "alpha-k3n8", State: "done"}}
	abs := &MemAbsence{M: map[string]int64{"aaaaaaaa": 1}}
	res, ctl, _ := runAbsent(t, all, abs, 999999, false)
	if res.Cleared != 0 || len(ctl.removed) != 0 {
		t.Fatalf("only reviewer-shaped names are cleared: %+v", res)
	}
}

// A blind sweep must not read as a quiet, successful one.
func TestSkippedIsShouted(t *testing.T) {
	got := Result{Kept: 1, Skipped: 2}.String()
	if !strings.Contains(got, "SKIPPED 2") {
		t.Errorf("want the skip shouted, got %q", got)
	}
}

// An entry with no bg_id joins no live session by construction, so reaping on
// it removes the registration of a session that is running.
func TestEntryWithNoIDIsSkippedNotReaped(t *testing.T) {
	entries := []registry.Entry{ours(registry.Entry{AgentID: "x1", Name: "alpha-noid", Role: "worker"})}
	res, st, _, out := run(t, entries, nil, fakeForge{}, false)
	if len(st.removed) != 0 {
		t.Fatal("an entry with no bg_id must not be reaped")
	}
	if res.Skipped != 1 || !strings.Contains(out, "no bg_id") {
		t.Errorf("want a stated skip: %+v\n%s", res, out)
	}
}

// "Working" is mid-turn. A case-sensitive check stops a session that is still
// writing up, and takes the one artifact anyone reads with it.
func TestMidTurnCheckIsCaseInsensitive(t *testing.T) {
	entries := []registry.Entry{ours(registry.Entry{
		AgentID: "r1", BgID: "aaaaaaaa", Name: "rev-k3n8-7", Role: "reviewer", PR: "7", Head: "abc1234"})}
	forge := fakeForge{state: map[string]string{"7": "OPEN"}, signed: map[string]bool{"7": true}}
	for _, state := range []string{"Working", "BUSY", " working ", "thinking", "compacting"} {
		live := []session.Session{{ID: "aaaaaaaa", Name: "rev-k3n8-7", State: state}}
		res, _, ctl, _ := run(t, entries, live, forge, false)
		if res.Retired != 0 || len(ctl.stopped) != 0 {
			t.Errorf("state %q is mid-turn and must not be retired", state)
		}
	}
}

// ---- pairing ----

type memPairs struct{ M map[string]Pair }

func (m *memPairs) Load() map[string]Pair {
	out := map[string]Pair{}
	for k, v := range m.M {
		out[k] = v
	}
	return out
}
func (m *memPairs) Update(fn func(map[string]Pair) bool) {
	cur := m.Load()
	if fn(cur) {
		m.M = cur
	}
}

func runPairs(t *testing.T, live session.Plain, pairs *memPairs, abs *MemAbsence, now int64, dry bool) (Result, *fakeCtl, string) {
	t.Helper()
	ctl := &fakeCtl{}
	var buf bytes.Buffer
	res, err := Run(nil, live, live, nil, ctl,
		Options{DryRun: dry, Out: &buf, Absence: abs, Pairs: pairs, Now: now})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res, ctl, buf.String()
}

// The whole point: a watcher outlives nothing. Once the session it accompanies
// has been gone for the sustained window, it is stopped and the record cleared,
// so the next spawn is not refused by a cap held against a session that ended.
func TestPairedSessionRetiresAfterItsCallerIsGone(t *testing.T) {
	live := session.Plain{{ID: "bbbbbbbb", Name: "assistant"}}
	pairs := &memPairs{M: map[string]Pair{"assistant": {BgID: "bbbbbbbb", Watching: "aaaaaaaa", MissingSince: 1000}}}
	abs := &MemAbsence{}

	res, ctl, out := runPairs(t, live, pairs, abs, 1000+AbsentBeforeClear, false)
	if res.Unpaired != 1 {
		t.Fatalf("want one unpaired, got %+v", res)
	}
	if len(ctl.stopped) != 1 || ctl.stopped[0] != "bbbbbbbb" {
		t.Errorf("the accompanying session should have been stopped, got %v", ctl.stopped)
	}
	if _, still := pairs.M["assistant"]; still {
		t.Error("record kept after retiring; the cap would stay held")
	}
	if !strings.Contains(out, "unpair") {
		t.Errorf("want the reason printed: %s", out)
	}
}

// ABSENT ONCE IS NOT GONE. This is the case the clock exists for, and getting
// it wrong once cost a live review, so it is pinned here too rather than
// trusted to the pass that shares the constant.
func TestPairedSessionSurvivesASingleAbsence(t *testing.T) {
	live := session.Plain{{ID: "bbbbbbbb", Name: "assistant"}}
	pairs := &memPairs{M: map[string]Pair{"assistant": {BgID: "bbbbbbbb", Watching: "aaaaaaaa"}}}

	// First sighting of the absence: starts the clock, retires nothing.
	res, ctl, _ := runPairs(t, live, pairs, &MemAbsence{}, 5000, false)
	if res.Unpaired != 0 || len(ctl.stopped) != 0 {
		t.Fatalf("a first absence must not retire anything: %+v", res)
	}
	if pairs.M["assistant"].MissingSince != 5000 {
		t.Errorf("the clock should have started at 5000, got %+v", pairs.M["assistant"])
	}

	// One second short of the window.
	res, ctl, _ = runPairs(t, live, pairs, &MemAbsence{}, 5000+AbsentBeforeClear-1, false)
	if res.Unpaired != 0 || len(ctl.stopped) != 0 {
		t.Fatalf("one second short must not retire: %+v", res)
	}
}

// A caller that comes back clears the clock, so a watcher is not retired for an
// absence that did not stick.
func TestReturningCallerClearsThePairingClock(t *testing.T) {
	live := session.Plain{
		{ID: "bbbbbbbb", Name: "assistant"},
		{ID: "aaaaaaaa", Name: "dev"},
	}
	pairs := &memPairs{M: map[string]Pair{"assistant": {BgID: "bbbbbbbb", Watching: "aaaaaaaa", MissingSince: 1000}}}

	res, ctl, _ := runPairs(t, live, pairs, &MemAbsence{}, 1000+AbsentBeforeClear, false)
	if res.Unpaired != 0 || len(ctl.stopped) != 0 {
		t.Fatalf("the caller is live; nothing should retire: %+v", res)
	}
	if pairs.M["assistant"].MissingSince != 0 {
		t.Error("clock not reset for a session that came back")
	}
}

// A dry run says what it would do and does none of it.
func TestPairingDryRunTouchesNothing(t *testing.T) {
	live := session.Plain{{ID: "bbbbbbbb", Name: "assistant"}}
	pairs := &memPairs{M: map[string]Pair{"assistant": {BgID: "bbbbbbbb", Watching: "aaaaaaaa", MissingSince: 1000}}}

	res, ctl, out := runPairs(t, live, pairs, &MemAbsence{}, 1000+AbsentBeforeClear, true)
	if len(ctl.stopped) != 0 || len(ctl.removed) != 0 {
		t.Errorf("a dry run stopped something: %v %v", ctl.stopped, ctl.removed)
	}
	if _, still := pairs.M["assistant"]; !still {
		t.Error("a dry run deleted the record")
	}
	if res.Unpaired != 1 || !strings.Contains(out, "unpair") {
		t.Errorf("a dry run must still report what it would do: %+v %s", res, out)
	}
}

// A watcher can wedge while the session it watches carries on. `blocked` is
// deliberately not a dead state, so it stays listed, keeps its record, and holds
// a cap of one against a session doing nothing. Seen on the first live run, when
// the machine slept mid-response.
func TestWedgedWatcherIsRetiredOnItsOwnClock(t *testing.T) {
	live := session.Plain{
		{ID: "bbbbbbbb", Name: "assistant", State: "blocked"},
		{ID: "aaaaaaaa", Name: "dev", State: "working"},
	}
	pairs := &memPairs{M: map[string]Pair{
		"assistant": {BgID: "bbbbbbbb", Watching: "aaaaaaaa", WedgedSince: 1000},
	}}

	res, ctl, out := runPairs(t, live, pairs, &MemAbsence{}, 1000+AbsentBeforeClear, false)
	if res.Unpaired != 1 || len(ctl.stopped) != 1 {
		t.Fatalf("a wedged watcher should be retired: %+v stopped=%v", res, ctl.stopped)
	}
	if !strings.Contains(out, "wedged") {
		t.Errorf("want the reason named: %s", out)
	}
	if _, still := pairs.M["assistant"]; still {
		t.Error("record kept; the cap would stay held")
	}
}

// BLOCKED ONCE IS NOT WEDGED. Same discipline as absence: stopping a session
// that was briefly slow takes whatever it was about to say with it.
func TestBlockedOnceDoesNotRetire(t *testing.T) {
	live := session.Plain{
		{ID: "bbbbbbbb", Name: "assistant", State: "blocked"},
		{ID: "aaaaaaaa", Name: "dev", State: "working"},
	}
	pairs := &memPairs{M: map[string]Pair{"assistant": {BgID: "bbbbbbbb", Watching: "aaaaaaaa"}}}

	res, ctl, _ := runPairs(t, live, pairs, &MemAbsence{}, 5000, false)
	if res.Unpaired != 0 || len(ctl.stopped) != 0 {
		t.Fatalf("a first sighting must not retire: %+v", res)
	}
	if pairs.M["assistant"].WedgedSince != 5000 {
		t.Errorf("clock should have started: %+v", pairs.M["assistant"])
	}

	res, ctl, _ = runPairs(t, live, pairs, &MemAbsence{}, 5000+AbsentBeforeClear-1, false)
	if res.Unpaired != 0 || len(ctl.stopped) != 0 {
		t.Fatalf("one second short must not retire: %+v", res)
	}
}

// A watcher that comes back is not wedged, and its clock must not survive.
func TestRecoveredWatcherClearsTheWedgeClock(t *testing.T) {
	live := session.Plain{
		{ID: "bbbbbbbb", Name: "assistant", State: "working"},
		{ID: "aaaaaaaa", Name: "dev", State: "working"},
	}
	pairs := &memPairs{M: map[string]Pair{
		"assistant": {BgID: "bbbbbbbb", Watching: "aaaaaaaa", WedgedSince: 1000},
	}}

	res, ctl, _ := runPairs(t, live, pairs, &MemAbsence{}, 1000+AbsentBeforeClear, false)
	if res.Unpaired != 0 || len(ctl.stopped) != 0 {
		t.Fatalf("a recovered watcher must not be retired: %+v", res)
	}
	if pairs.M["assistant"].WedgedSince != 0 {
		t.Errorf("wedge clock not cleared: %+v", pairs.M["assistant"])
	}
}
