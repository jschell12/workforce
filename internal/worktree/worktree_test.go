package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// originAndClone builds a real remote plus a clone, because "pushed" is a
// relationship between two repositories and a single repo cannot express it.
func originAndClone(t *testing.T) (origin, clone string) {
	t.Helper()
	base := t.TempDir()
	origin = filepath.Join(base, "origin.git")
	git(t, base, "init", "-q", "--bare", "-b", "main", origin)
	work := filepath.Join(base, "seed")
	git(t, base, "clone", "-q", origin, work)
	git(t, work, "config", "user.email", "t@example.com")
	git(t, work, "config", "user.name", "t")
	git(t, work, "commit", "-q", "--allow-empty", "-m", "root")
	git(t, work, "push", "-q", "origin", "main")
	clone = filepath.Join(base, "clone")
	git(t, base, "clone", "-q", origin, clone)
	git(t, clone, "config", "user.email", "t@example.com")
	git(t, clone, "config", "user.name", "t")
	return origin, clone
}

func TestCleanAndPushedIsSafe(t *testing.T) {
	_, clone := originAndClone(t)
	if v := SafeToRemove(clone, Git); !v.Safe {
		t.Fatalf("a clean, pushed worktree should be removable: %s", v.Why)
	}
}

func TestMissingDirectoryIsSafe(t *testing.T) {
	if v := SafeToRemove(filepath.Join(t.TempDir(), "nope"), Git); !v.Safe || v.Why != "already gone" {
		t.Fatalf("got %+v", v)
	}
}

func TestUncommittedChangesAreRefused(t *testing.T) {
	_, clone := originAndClone(t)
	if err := os.WriteFile(filepath.Join(clone, "scratch.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}
	v := SafeToRemove(clone, Git)
	if v.Safe || v.Why != "uncommitted changes" {
		t.Fatalf("untracked work must be protected, got %+v", v)
	}
}

// The case the check exists for: committed, and existing nowhere else.
func TestUnpushedCommitsAreRefused(t *testing.T) {
	_, clone := originAndClone(t)
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "local only")
	v := SafeToRemove(clone, Git)
	if v.Safe {
		t.Fatal("an unpushed commit must be protected")
	}
	if v.Why != "1 unpushed commit(s)" {
		t.Errorf("the reason should count them, got %q", v.Why)
	}
}

// A branch with no upstream has commits that exist in exactly one place.
func TestNoUpstreamIsRefused(t *testing.T) {
	_, clone := originAndClone(t)
	git(t, clone, "checkout", "-q", "-b", "detached-work")
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "on a branch nobody has")
	v := SafeToRemove(clone, Git)
	if v.Safe || v.Why != "no upstream branch" {
		t.Fatalf("got %+v", v)
	}
}

// Not readable is not the same as clean.
func TestUnreadableRepoIsRefused(t *testing.T) {
	dir := t.TempDir() // a directory, but not a git repository
	v := SafeToRemove(dir, Git)
	if v.Safe || v.Why != "cannot read git status" {
		t.Fatalf("a directory git cannot answer for must be kept, got %+v", v)
	}
}
