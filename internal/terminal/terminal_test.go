package terminal

import (
	"strings"
	"testing"
)

func TestDefaultIsITerm(t *testing.T) {
	app, script, err := Script(Default, "claude attach abcd1234", "/tmp/work")
	if err != nil {
		t.Fatal(err)
	}
	if app != "iTerm" {
		t.Errorf("the default should be iTerm2, got %q", app)
	}
	for _, want := range []string{`tell application "iTerm"`, "create tab with default profile",
		"create window with default profile", "claude attach abcd1234"} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
}

// Both branches, always: "new tab" with no window to put it in fails rather
// than doing the obvious thing.
func TestEveryDriverHandlesTheNoWindowCase(t *testing.T) {
	for _, name := range Known() {
		_, script, err := Script(name, "claude attach x", "/tmp")
		if err != nil {
			t.Fatal(err)
		}
		if name == "terminal" {
			continue // `do script` opens a window when there is none
		}
		if !strings.Contains(script, "count of windows") {
			t.Errorf("%s does not handle having no window:\n%s", name, script)
		}
	}
}

func TestUnknownTerminalNamesTheKnownOnes(t *testing.T) {
	_, _, err := Script("kitty", "c", "/tmp")
	if err == nil {
		t.Fatal("an unknown terminal must be refused")
	}
	for _, want := range []string{"ghostty", "iterm", "terminal"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should list %q: %v", want, err)
		}
	}
}

func TestNameIsCaseAndSpaceInsensitive(t *testing.T) {
	for _, n := range []string{"iTerm", " ITERM ", "iterm"} {
		if _, _, err := Script(n, "c", "/tmp"); err != nil {
			t.Errorf("%q should resolve: %v", n, err)
		}
	}
}

// A working directory can contain a space, and one that does would otherwise cd
// into the wrong place and run the command there.
func TestWorkingDirectoryWithSpacesIsQuoted(t *testing.T) {
	_, script, err := Script("iterm", "claude attach x", "/tmp/my work/dir")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, `cd '/tmp/my work/dir'`) {
		t.Errorf("cwd should be shell-quoted:\n%s", script)
	}
}

// An unescaped quote ends the AppleScript string early and turns the rest of
// the command into syntax.
func TestQuotesInTheCommandAreEscapedForAppleScript(t *testing.T) {
	_, script, err := Script("iterm", `sh -c "echo hi"`, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, `sh -c "echo hi"`) {
		t.Errorf("inner quotes must be escaped:\n%s", script)
	}
	if !strings.Contains(script, `sh -c \"echo hi\"`) {
		t.Errorf("want escaped quotes:\n%s", script)
	}
}
