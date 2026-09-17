package registry

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// realGitCommonDir shells out the way the command does. Tests run against real
// repositories rather than a faked layout, because the behaviour worth pinning
// is that a WORKTREE resolves to the same directory as its main checkout, and
// a fake cannot get that wrong.
func realGitCommonDir(repoPath string) (string, error) {
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(repoPath, p)
	}
	return filepath.Abs(p)
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "root"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func open(t *testing.T, path string) Store {
	t.Helper()
	s, err := Open(path, realGitCommonDir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Reading a repo where nothing ever registered must be empty, not an error.
func TestReadMissingDirectoryIsEmpty(t *testing.T) {
	got, err := open(t, newRepo(t)).Read()
	if err != nil {
		t.Fatalf("Read on a fresh repo: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want no entries, got %d", len(got))
	}
}

func TestWriteThenRead(t *testing.T) {
	s := open(t, newRepo(t))
	if _, err := s.Write(Entry{AgentID: "abc12345", Name: "rev-x-1", Role: "reviewer"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read()
	if err != nil || len(got) != 1 {
		t.Fatalf("Read: %v, %d entries", err, len(got))
	}
	if got[0].Name != "rev-x-1" || got[0].Role != "reviewer" {
		t.Errorf("round trip lost fields: %+v", got[0])
	}
	if !got[0].OwnedByWF() {
		t.Error("Write must stamp the tool marker so a later sweep can tell its own entries apart")
	}
	if got[0].CreatedAt == 0 || got[0].HeartbeatAt == 0 {
		t.Error("Write must stamp created_at and heartbeat_at as unix seconds")
	}
}

// The directory is shared across every worktree of the repo. That is the whole
// mechanism, so it gets a test rather than a comment.
func TestWorktreeSharesTheSameDirectory(t *testing.T) {
	main := newRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	cmd := exec.Command("git", "-C", main, "worktree", "add", "-q", wt, "-b", "side")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	if _, err := open(t, main).Write(Entry{AgentID: "shared01", Name: "from-main"}); err != nil {
		t.Fatal(err)
	}
	got, err := open(t, wt).Read()
	if err != nil || len(got) != 1 || got[0].Name != "from-main" {
		t.Fatalf("worktree should see the main checkout's entry; got %d entries, err %v", len(got), err)
	}
}

// One agent writing a truncated file must not blind wf to every other entry.
func TestCorruptEntrySkippedNotFatal(t *testing.T) {
	s := open(t, newRepo(t))
	if _, err := s.Write(Entry{AgentID: "good0001", Name: "good"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir(), "bad.json"), []byte("{oh no"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read()
	if err != nil {
		t.Fatalf("a corrupt neighbour must not fail the read: %v", err)
	}
	if len(got) != 1 || got[0].Name != "good" {
		t.Fatalf("want the one good entry, got %+v", got)
	}
}

// The failure this prevents: deleting another agent's registration
// deregisters a live agent mid-task.
func TestRemoveRefusesForeignEntries(t *testing.T) {
	s := open(t, newRepo(t))
	if err := os.MkdirAll(s.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(s.Dir(), "someone-else.json")
	if err := os.WriteFile(foreign, []byte(`{"agent_id":"someone-else","managed_by":"some-other-tool"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Read()
	if len(got) != 1 {
		t.Fatalf("setup: %d entries", len(got))
	}
	err := s.Remove(got[0])
	if err == nil {
		t.Fatal("removing an entry wf did not write must be refused")
	}
	if !strings.Contains(err.Error(), "another agent") {
		t.Errorf("refusal should say why; got %v", err)
	}
	if _, statErr := os.Stat(foreign); statErr != nil {
		t.Error("the foreign entry must still be on disk")
	}
}

func TestRemoveOwnEntry(t *testing.T) {
	s := open(t, newRepo(t))
	if _, err := s.Write(Entry{AgentID: "mine0001"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Read()
	if err := s.Remove(got[0]); err != nil {
		t.Fatalf("wf must be able to remove its own entry: %v", err)
	}
	after, _ := s.Read()
	if len(after) != 0 {
		t.Fatalf("entry survived removal: %+v", after)
	}
}

func TestWriteRejectsPathTraversal(t *testing.T) {
	s := open(t, newRepo(t))
	if _, err := s.Write(Entry{AgentID: "../escape"}); err == nil {
		t.Fatal("an agent_id with a path separator must be refused")
	}
	if _, err := s.Write(Entry{}); err == nil {
		t.Fatal("an entry with no agent_id must be refused")
	}
}

// A verbatim entry as the Python implementation writes it. The port dropped
// every real entry until this existed: created_at is a NUMBER, `pr` and `head`
// are null rather than absent, and the ownership marker is managed_by. Read
// skips what will not parse, so the symptom was live workers listed with no
// role rather than any error at all.
const pythonWritten = `{
  "agent_id": "3639b417",
  "bg_id": "3639b417",
  "name": "schellout-zaw5",
  "role": "worker",
  "persona": "implementer",
  "repo": "schellout",
  "worktree": "/tmp/wt",
  "branch": "agent/1789220309-worker",
  "pr": null,
  "head": null,
  "tag": "zaw5",
  "managed_by": "workforce",
  "token_key": "custom:GITHUB_TOKEN",
  "status": "working",
  "created_at": 1789220313,
  "heartbeat_at": 1789220313
}`

func TestReadsEntriesWrittenByThePythonImplementation(t *testing.T) {
	s := open(t, newRepo(t))
	if err := os.MkdirAll(s.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir(), "3639b417.json"), []byte(pythonWritten), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read()
	if err != nil || len(got) != 1 {
		t.Fatalf("a real entry must parse; got %d entries, err %v", len(got), err)
	}
	e := got[0]
	if e.Role != "worker" || e.Tag != "zaw5" || e.CreatedAt != 1789220313 {
		t.Errorf("fields lost: %+v", e)
	}
	if !e.OwnedByWF() {
		t.Error("managed_by=workforce must count as ours, or the port cannot retire what the Python registered")
	}
	if e.PR != "" || e.Head != "" {
		t.Errorf("explicit nulls should read as empty, got pr=%q head=%q", e.PR, e.Head)
	}
}

// The two ownership gates must agree. They did not: reconcile claimed a legacy
// entry by shape and Remove refused it by marker, so the log and the counter
// both reported a drop that never happened, reprinted every 120 seconds.
func TestLegacyEntryIsClaimedAndRemovable(t *testing.T) {
	s := open(t, newRepo(t))
	if err := os.MkdirAll(s.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"agent_id":"old00001","bg_id":"old00001","token_key":"custom:X","name":"pre-marker"}`
	if err := os.WriteFile(filepath.Join(s.Dir(), "old00001.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Read()
	if len(got) != 1 || !got[0].OwnedByWF() {
		t.Fatalf("a pre-marker wf entry must be claimed: %+v", got)
	}
	if err := s.Remove(got[0]); err != nil {
		t.Fatalf("what ownedByWF claims, Remove must accept: %v", err)
	}
}
