// Package terminal opens a tab in a macOS terminal application.
//
// AppleScript rather than a CLI flag, because the CLIs do not agree and several
// do not work: `ghostty +new-window` answers "not supported on this platform" on
// macOS. Every driver spells out both branches, a tab in an existing window and
// a new window when there are none, because "new tab" with no window to put it
// in fails rather than doing the obvious thing.
package terminal

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Driver builds the AppleScript that opens one tab.
type Driver struct {
	// App is the application name AppleScript addresses.
	App string
	// script renders the command and working directory into a full script.
	script func(cmd, cwd string) string
}

// Default is what `wf watch` uses when no terminal is named.
const Default = "iterm"

var drivers = map[string]Driver{
	// iTerm2 runs the command through the shell with `write text`, so the
	// working directory is a cd rather than a configuration field.
	"iterm": {App: "iTerm", script: func(cmd, cwd string) string {
		run := fmt.Sprintf("cd %s && %s", shellQuote(cwd), cmd)
		return fmt.Sprintf(`
tell application "iTerm"
  activate
  if (count of windows) is 0 then
    set w to (create window with default profile)
    tell current session of w to write text "%s"
  else
    tell current window
      set t to (create tab with default profile)
      tell current session of t to write text "%s"
    end tell
  end if
end tell
`, applescriptQuote(run), applescriptQuote(run))
	}},

	// Ghostty takes the command and the directory as configuration, so nothing
	// is interpreted by a shell twice.
	"ghostty": {App: "Ghostty", script: func(cmd, cwd string) string {
		return fmt.Sprintf(`
tell application "Ghostty"
  activate
  set cfg to new surface configuration
  set command of cfg to "%s"
  set initial working directory of cfg to "%s"
  if (count of windows) is 0 then
    new window with configuration cfg
  else
    new tab in front window with configuration cfg
  end if
end tell
`, applescriptQuote(cmd), applescriptQuote(cwd))
	}},

	"terminal": {App: "Terminal", script: func(cmd, cwd string) string {
		run := fmt.Sprintf("cd %s && %s", shellQuote(cwd), cmd)
		return fmt.Sprintf(`
tell application "Terminal"
  activate
  do script "%s"
end tell
`, applescriptQuote(run))
	}},
}

// Known lists the terminals this understands, for an error message that tells
// someone what to type instead of only what not to.
func Known() []string {
	out := make([]string, 0, len(drivers))
	for k := range drivers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Script returns the AppleScript for a named terminal.
func Script(name, cmd, cwd string) (app, script string, err error) {
	d, ok := drivers[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return "", "", fmt.Errorf("unknown terminal %q. Known: %s", name, strings.Join(Known(), ", "))
	}
	return d.App, d.script(cmd, cwd), nil
}

// Open runs the script.
func Open(name, cmd, cwd string) (app string, out []byte, err error) {
	app, script, err := Script(name, cmd, cwd)
	if err != nil {
		return "", nil, err
	}
	out, err = exec.Command("osascript", "-e", script).CombinedOutput()
	return app, out, err
}

// applescriptQuote escapes for an AppleScript string literal, which is not the
// same job as escaping for a shell: an unescaped quote here ends the string
// early and turns the rest of the command into syntax.
func applescriptQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// shellQuote wraps a path for the shell that `write text` feeds it to. A
// working directory can contain a space, and one that does would otherwise cd
// into the wrong place and run the command there.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
