package session

import (
	"os"
	"path/filepath"
	"testing"
)

// stubClaude writes a fake `claude` that prints body and exits with code.
// The shell suite stubs the binary rather than mocking the call; doing the same
// here means these tests exercise the real exec path, including the failure
// modes that only exist once a process is involved.
func stubClaude(t *testing.T, body string, code int) Client {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\ncat <<'JSON'\n" + body + "\nJSON\nexit " + itoa(code) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return Client{Bin: bin}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	return string(rune('0' + i))
}

// The bug this pins: `blocked` was in no known vocabulary, fell through an
// allowlist to "not running", and a keepalive started a duplicate. An
// unrecognised state must count as alive.
func TestUnknownStateCountsAsAlive(t *testing.T) {
	c := stubClaude(t, `[{"id":"aaaaaaaa","name":"base","state":"blocked"},
	                     {"id":"bbbbbbbb","name":"x","state":"totally-new-word"}]`, 0)
	p, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(p.Live()); got != 2 {
		t.Fatalf("unknown states must count as alive; got %d live of 2", got)
	}
}

func TestExplicitlyDeadStatesDropped(t *testing.T) {
	c := stubClaude(t, `[{"id":"aaaaaaaa","state":"killed"},{"id":"bbbbbbbb","state":"idle"}]`, 0)
	p, _ := c.List()
	live := p.Live()
	if len(live) != 1 || live[0].Ref() != "bbbbbbbb" {
		t.Fatalf("want only the idle row, got %+v", live)
	}
}

// An unreadable listing and an empty one are opposite answers. Conflating them
// makes every cap decorative.
func TestUnreadableListingIsAnError(t *testing.T) {
	c := stubClaude(t, "", 1)
	if _, err := c.List(); err == nil {
		t.Fatal("a failing claude must produce an error, not an empty listing")
	}
	c = stubClaude(t, "not json at all", 0)
	if _, err := c.List(); err == nil {
		t.Fatal("unparseable output must produce an error, not an empty listing")
	}
	c = Client{Bin: "/nonexistent/claude"}
	if _, err := c.List(); err == nil {
		t.Fatal("a missing binary must produce an error, not an empty listing")
	}
}

func TestStateWordReadsBothFields(t *testing.T) {
	if got := (Session{State: "working"}).StateWord(); got != "working" {
		t.Errorf("background row: got %q", got)
	}
	if got := (Session{Status: "busy"}).StateWord(); got != "busy" {
		t.Errorf("interactive row: got %q", got)
	}
	if got := (Session{}).StateWord(); got != "unknown" {
		t.Errorf("neither field: got %q", got)
	}
}

func TestRefPrefersIDThenTruncatesSessionID(t *testing.T) {
	if got := (Session{ID: "abcd1234"}).Ref(); got != "abcd1234" {
		t.Errorf("got %q", got)
	}
	if got := (Session{SessionID: "0123456789abcdef"}).Ref(); got != "01234567" {
		t.Errorf("got %q", got)
	}
}

func TestFind(t *testing.T) {
	rows := []Session{
		{ID: "aaaa1111", Name: "rev-9320-6"},
		{ID: "aaaa2222", Name: "worker-x"},
		{ID: "bbbb3333", Name: "base"},
	}
	for _, target := range []string{"aaaa1111", "rev-9320-6", "bbbb"} {
		if _, err := Find(rows, target); err != nil {
			t.Errorf("Find(%q): %v", target, err)
		}
	}
	// An ambiguous prefix must refuse and say what it matched, rather than
	// silently acting on whichever row sorted first.
	_, err := Find(rows, "aaaa")
	if err == nil {
		t.Fatal("ambiguous prefix must refuse")
	}
	for _, want := range []string{"rev-9320-6", "worker-x"} {
		if !contains(err.Error(), want) {
			t.Errorf("refusal should name %q; got %v", want, err)
		}
	}
	if _, err := Find(rows, "zzz"); err == nil {
		t.Fatal("no match must be an error")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Names are reused by construction: a finished session stays in the --all
// listing until the clear pass, so a respawned reviewer of the same name
// overlaps a stale row. Returning the first is how `wf rm` removes a worktree
// off the wrong entry.
func TestAmbiguousExactNameIsRefusedNotGuessed(t *testing.T) {
	rows := []Session{
		{ID: "aaaa1111", Name: "rev-k3n8-437"}, // the stale one
		{ID: "bbbb2222", Name: "rev-k3n8-437"}, // the live one
	}
	_, err := Find(rows, "rev-k3n8-437")
	if err == nil {
		t.Fatal("two sessions share this name; picking one is how a destructive command hits the wrong session")
	}
	for _, want := range []string{"aaaa1111", "bbbb2222", "Use a ref"} {
		if !contains(err.Error(), want) {
			t.Errorf("the refusal should name %q and the way out: %v", want, err)
		}
	}
	// A ref still resolves, which is the escape the message points at.
	if _, err := Find(rows, "bbbb2222"); err != nil {
		t.Errorf("an exact ref must still resolve: %v", err)
	}
}
