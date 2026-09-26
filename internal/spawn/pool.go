package spawn

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jschell12/workforce/internal/config"
	"github.com/jschell12/workforce/internal/session"
)

const freshWorkerPolicy = "Worker session selection: create fresh sessions with wf spawn for new assignments. Do not discover, reuse, or assign existing user sessions by name or availability. Only an explicit --sessions pool authorizes reuse. Reviewers must always be fresh independent reviewer sessions. This policy does not authorize additional work or change role caps, credentials, or permissions."

// This is dispatch guidance, not a reservation or a permission-mode switch.
// The coordinator must recheck identity and availability when it dispatches.
func workerPoolPolicy(role *config.Role, persona string, req Request, w World) (string, error) {
	if !req.SessionsSet && req.Sessions == "" {
		if persona == "coordinator" || role.Name == "workforce" || role.Name == "supervisor" {
			return freshWorkerPolicy, nil
		}
		return "", nil
	}
	if role.Name != "worker" || persona != "coordinator" || role.Repo == nil {
		return "", fmt.Errorf("--sessions requires a repo-scoped worker with --persona coordinator; reviewers always start fresh")
	}
	seen := map[string]bool{}
	var selected []session.Session
	for _, target := range strings.Split(req.Sessions, ",") {
		target = strings.TrimSpace(target)
		if target == "" {
			return "", fmt.Errorf("--sessions must contain nonempty names or refs")
		}
		s, err := session.Find(w.Live, target)
		if err != nil {
			return "", fmt.Errorf("--sessions: %w", err)
		}
		if s.Ref() == "" || s.Name == "" {
			return "", fmt.Errorf("--sessions: %q needs both a stable ref and a name", target)
		}
		if seen[s.Ref()] {
			return "", fmt.Errorf("--sessions: duplicate session %q", target)
		}
		seen[s.Ref()] = true
		if s.Ref() == w.CallerID {
			return "", fmt.Errorf("--sessions: cannot assign the calling session %q", target)
		}
		if (s.State != "" && s.State != "idle") || (s.Status != "" && s.Status != "idle") || s.StateWord() != "idle" {
			return "", fmt.Errorf("--sessions: %q is not unambiguously idle", target)
		}
		inRepo := s.Cwd != "" && filepath.Clean(s.Cwd) == filepath.Clean(expandHome(role.Repo.Path, ""))
		for _, e := range w.AllRegs {
			if e.BgID != s.Ref() && e.Name != s.Name {
				continue
			}
			if e.Role != "worker" || e.Repo != role.Repo.Name {
				return "", fmt.Errorf("--sessions: %q is registered to another repo or a non-worker role", target)
			}
			if e.Worktree != "" && filepath.Clean(e.Worktree) == filepath.Clean(s.Cwd) {
				inRepo = true
			}
		}
		if !inRepo {
			return "", fmt.Errorf("--sessions: %q is not rooted in this repo or its registered worker worktree", target)
		}
		selected = append(selected, s)
	}
	var b strings.Builder
	b.WriteString("Explicit existing worker pool (only these sessions may receive worker assignments):")
	for _, s := range selected {
		fmt.Fprintf(&b, "\n- name %q, ref %q, cwd %q", s.Name, s.Ref(), s.Cwd)
	}
	b.WriteString("\nBefore every dispatch re-list sessions and verify the same name, ref, repo scope, idle status, and appropriate worker identity/credentials. If unavailable, wait or report the blocker; do not substitute other existing sessions or create additional workers. Do not repurpose a reviewer. Reviewers always start as fresh independent wf reviewer sessions. This pool is not a reservation and does not change caps, credentials, or permissions.")
	return b.String(), nil
}
