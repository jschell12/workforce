// Package paths resolves every file wf reads or writes.
//
// All of them are environment-overridable, because the test suite runs the real
// binary against a scratch tree rather than mocking the filesystem. A path that
// cannot be redirected is a path the tests have to either skip or pollute.
package paths

import (
	"os"
	"path/filepath"
)

// Set is the resolved location of everything wf touches.
type Set struct {
	Config     string // roles.toml
	PermDir    string // per-role permission profiles
	SessionDir string // per-session --settings files
	Log        string
	Absence    string // the absent-since clock; outlives any one sweep
	Claude     string // executable name or path
	Scredmgr   string
	GH         string
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Resolve reads the WORKFORCE_* overrides, falling back to the installed
// layout under the user's home directory.
func Resolve() Set {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	claudeDir := filepath.Join(home, ".claude", "workforce")
	return Set{
		Config:     env("WORKFORCE_CONFIG", filepath.Join(claudeDir, "roles.toml")),
		PermDir:    env("WORKFORCE_PERM_DIR", filepath.Join(claudeDir, "perm")),
		SessionDir: env("WORKFORCE_SESSION_DIR", filepath.Join(claudeDir, "session")),
		Log:        env("WORKFORCE_LOG", filepath.Join(home, "data", "workforce", "workforce.log")),
		Absence:    env("WORKFORCE_ABSENCE", filepath.Join(home, "data", "workforce", ".absent-since")),
		Claude:     env("WORKFORCE_CLAUDE", "claude"),
		Scredmgr:   env("WORKFORCE_SCREDMGR", "scredmgr"),
		GH:         env("WORKFORCE_GH", "gh"),
	}
}
