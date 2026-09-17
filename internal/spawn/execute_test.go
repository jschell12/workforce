package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// launcher writes a fake `claude` that prints out on stdout, err on stderr, and
// exits with code. Stubbing the binary rather than mocking the call is what
// makes the stdout/stderr distinction testable at all.
func launcher(t *testing.T, stdout, stderr string, code int) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "claude-stub")
	script := "#!/bin/sh\nprintf '%s\\n' " + shq(stdout) + "\nprintf '%s\\n' " + shq(stderr) + " >&2\nexit " + itoa(code) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	return string(rune('0' + i))
}

func plan(t *testing.T, bin string) *Plan {
	t.Helper()
	dir := t.TempDir()
	return &Plan{
		Name:         "rev-k3n8-7",
		Workdir:      dir,
		Argv:         []string{bin, "--bg", "-n", "rev-k3n8-7", "brief"},
		Env:          []string{"PATH=/usr/bin:/bin"},
		SettingsPath: filepath.Join(dir, "session", "rev-k3n8-7.json"),
		Settings:     map[string]any{"env": map[string]any{"WF_GH_TOKEN_KEY": "store:K"}},
	}
}

func TestExecuteReturnsTheSessionID(t *testing.T) {
	id, err := Execute(plan(t, launcher(t, "started a1b2c3d4", "", 0)), "")
	if err != nil || id != "a1b2c3d4" {
		t.Fatalf("got %q, %v", id, err)
	}
}

// THE REGRESSION. The launcher exited 0, so a session IS running holding a
// role-bound token. Treating an unparseable id as an error made the caller
// return before Register, leaving that session with no registry entry at all:
// nothing could count it, reap it, or pair a reviewer with it, and the obvious
// response to the error was to spawn again, past the cap.
func TestUnparseableIDIsNotAnError(t *testing.T) {
	id, err := Execute(plan(t, launcher(t, "started session for review", "", 0)), "")
	if err != nil {
		t.Fatalf("a session that started must not report failure: %v", err)
	}
	if id != "" {
		t.Fatalf("no hex id to find, got %q", id)
	}
}

// A stderr line carrying a hex token must not become the recorded bg_id: the
// entry would then join no live session, so its cap slot leaks and reconcile
// could never retire it.
func TestStderrCannotSupplyTheSessionID(t *testing.T) {
	id, err := Execute(plan(t, launcher(t, "starting", "warning: cafebabe is deprecated", 0)), "")
	if err != nil {
		t.Fatal(err)
	}
	if id == "cafebabe" {
		t.Fatal("the id was taken from stderr")
	}
	if id != "" {
		t.Fatalf("got %q", id)
	}
}

// A real failure must still fail, and say what stderr said.
func TestLauncherFailureReportsStderr(t *testing.T) {
	_, err := Execute(plan(t, launcher(t, "irrelevant stdout", "boom: no such agent", 1)), "")
	if err == nil {
		t.Fatal("a nonzero exit must be an error")
	}
	if !strings.Contains(err.Error(), "boom: no such agent") {
		t.Errorf("the message should come from stderr, got %v", err)
	}
}

// The settings file names a credential key and pins a session's permissions.
func TestSettingsFileWrittenPrivately(t *testing.T) {
	p := plan(t, launcher(t, "a1b2c3d4", "", 0))
	if _, err := Execute(p, ""); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p.SettingsPath)
	if err != nil {
		t.Fatalf("settings file not written: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("settings mode is %o, want 600", mode)
	}
	b, _ := os.ReadFile(p.SettingsPath)
	if !strings.Contains(string(b), "WF_GH_TOKEN_KEY") {
		t.Errorf("binding missing from the settings file: %s", b)
	}
}
