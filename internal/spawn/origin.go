package spawn

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jschell12/workforce/internal/registry"
)

// resolveOrigin answers who is starting this reviewer. EVERY reviewer must have
// an answer.
//
// A reviewer named `rev-437` tells you nothing about whose pull request it is
// reading, which is the whole complaint the tag exists to answer. So there is no
// unpaired case: either wf works the origin out, or it refuses and says how to
// tell it. Four routes, most specific first.
//
// Refusing is not a fallback to avoid. An anonymous reviewer is exactly what the
// pairing exists to prevent, so shipping one is worse than not shipping.
func resolveOrigin(req Request, w World) (registry.Entry, error) {
	// 1. Said outright: a person spawning one by hand, or a caller wf cannot see.
	if t := strings.TrimPrefix(strings.TrimSpace(req.ForTag), "@"); t != "" {
		return registry.Entry{ForTag: t, ForName: t, ForKind: "explicit"}, nil
	}

	// 2. The calling session, matched to a registered worker. An identity the
	//    model has to remember and pass is one that goes missing, so wf reads
	//    CLAUDE_CODE_SESSION_ID rather than asking.
	if w.CallerID != "" {
		for _, e := range w.AllRegs {
			if e.BgID == w.CallerID && e.Tag != "" {
				return registry.Entry{ForTag: e.Tag, ForName: e.Name,
					ForSession: e.BgID, ForKind: "worker"}, nil
			}
		}
	}

	// 3. The worktree we are standing in. Survives a session id wf cannot
	//    match, because a worker's cwd IS its worktree and the registry has it.
	if w.Cwd != "" {
		here := filepath.Clean(w.Cwd)
		for _, e := range w.AllRegs {
			if e.Worktree == "" || e.Tag == "" {
				continue
			}
			if under(here, filepath.Clean(e.Worktree)) {
				return registry.Entry{ForTag: e.Tag, ForName: e.Name,
					ForSession: e.BgID, ForKind: "worktree"}, nil
			}
		}
	}

	// 4. The pull request's own head branch, matched to the worker that cut
	//    it. Works from anywhere, including the main checkout, which is where
	//    a reviewer is usually summoned from.
	if w.HeadBranch != nil && req.Slug != "" && req.PR != "" {
		if branch, err := w.HeadBranch(req.Slug, req.PR); err == nil && branch != "" {
			for _, e := range w.AllRegs {
				if e.Branch == branch && e.Tag != "" {
					return registry.Entry{ForTag: e.Tag, ForName: e.Name,
						ForSession: e.BgID, ForKind: "branch"}, nil
				}
			}
		}
	}

	// 5. The calling session's bare ref. Not a worker, but a real identity: it
	//    is in the session list, so the pairing still resolves to something a
	//    person can look up.
	if w.CallerID != "" {
		return registry.Entry{ForTag: firstN(w.CallerID, 4), ForName: "session " + w.CallerID,
			ForSession: w.CallerID, ForKind: "session"}, nil
	}

	return registry.Entry{}, fmt.Errorf(
		"cannot tell which session is starting this reviewer, and every reviewer " +
			"must be identifiable by what started it. Pass --for <tag-or-name>")
}

// under reports whether path p is dir or inside it.
func under(p, dir string) bool {
	if p == dir {
		return true
	}
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
