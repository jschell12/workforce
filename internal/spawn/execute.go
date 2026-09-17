package spawn

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jschell12/workforce/internal/registry"
)

// Execute runs a plan: create the worktree if one is wanted, write the settings
// file, launch, and register the result.
func Execute(p *Plan, repoPath string) (bgID string, err error) {
	if p.Branch != "" {
		cmd := exec.Command("git", "-C", repoPath, "worktree", "add", p.Workdir, "-b", p.Branch, "origin/main")
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git worktree add failed: %s", strings.TrimSpace(string(out)))
		}
	}
	if err := os.MkdirAll(filepath.Dir(p.SettingsPath), 0o755); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(p.Settings, "", "  ")
	if err != nil {
		return "", err
	}
	// 0600: the file names a credential key and pins a session's permissions.
	if err := os.WriteFile(p.SettingsPath, append(b, '\n'), 0o600); err != nil {
		return "", err
	}

	cmd := exec.Command(p.Argv[0], p.Argv[1:]...)
	cmd.Dir = p.Workdir
	cmd.Env = p.Env
	// Separate buffers, deliberately. Merging them means any stderr line
	// carrying an 8-character hex token becomes the recorded bg_id, and a
	// launcher that prints nothing hex-shaped on stdout yields "". Either way
	// the registry entry never joins a live session: the cap relaxes and
	// reconcile can never retire it. Keeping them apart preserves the error
	// message AND the parse.
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return "", fmt.Errorf("%s failed: %s", p.Argv[0], truncate(msg, 400))
	}
	// An id that will not parse is NOT an error, and the distinction costs a
	// session. The launcher exited 0, so a session IS running holding a
	// role-bound token; returning an error here made the caller return before
	// Register, leaving that session with no registry entry at all. Nothing can
	// then count it, reap it, or pair a reviewer with it, and the natural
	// response to the failure message is to run the spawn again, which puts a
	// second session past the cap. The cap is the one thing wf holds that a
	// session cannot hold for itself.
	//
	// So: register with an empty bg_id and let the caller warn. liveForRole
	// joins on bg_id OR name for exactly this shape.
	return firstShortHex(stdout.String()), nil
}

// Register records the spawned session so caps, pairing and reconcile can see
// it. A machine-scoped role has no repository and therefore no registry.
func Register(p *Plan, bgID, repoPath string) error {
	e := p.Entry
	e.BgID = bgID
	e.AgentID = bgID
	if e.AgentID == "" {
		e.AgentID = p.Name
	}
	st, err := registry.Open(repoPath, registry.GitCommonDir)
	if err != nil {
		return err
	}
	_, err = st.Write(e)
	return err
}

// firstShortHex finds the 8-character hex id the launcher prints.
func firstShortHex(s string) string {
	for _, tok := range strings.Fields(s) {
		if len(tok) != 8 {
			continue
		}
		ok := true
		for _, c := range tok {
			if !strings.ContainsRune("0123456789abcdef", c) {
				ok = false
				break
			}
		}
		if ok {
			return tok
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
