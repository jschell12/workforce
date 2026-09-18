// Package spawn builds and launches one session for a role.
//
// The work is split so that everything decided BEFORE a process exists is a
// value: Plan holds the exact argv, environment and settings file, and Execute
// merely runs it. Every refusal this tool owes its users is therefore testable
// without starting a session, which matters because the failures worth pinning
// (a cap that stops counting, an unbound token, an anonymous reviewer) are all
// decisions rather than outcomes.
package spawn

import "fmt"

// ReviewerBrief is composed here and a caller-supplied one is refused.
//
// An author may summon the reviewer for its own pull request. It must not also
// frame what that reviewer looks at, or "review this" quietly becomes "confirm
// this". The refusal is the mechanism; this constant is only its content.
const ReviewerBrief = "Review pull request #%s in %s. You are the reviewer session for this " +
	"pull request and nothing else. Read the repo's own review layer first if it " +
	"has one, then the diff, then the premise the pull request was built on. " +
	"Post exactly one verdict comment naming the exact head commit you reviewed, " +
	"and refresh the merge gate if the repo has one. Do not fix what you find and " +
	"do not merge. When the verdict is posted you are done."

// CallerLine is appended to the brief of a role that accompanies its caller.
//
// Without it such a session cannot address the one it accompanies, and the
// failure is not obvious from inside: its brief carries a SESSION ID, while
// SendMessage takes a NAME from the agent listing, and the ref that listing
// shows is a third namespace again. There is no mapping between them available
// to the spawned session.
//
// Measured 2026-09-18, the first time a watcher had something to say. It tried
// `to: "main"` and was told "You are the main conversation", then picked the
// only other name it could see and delivered a finding about one session to a
// different one. Right finding, wrong session, and nothing about it looked like
// an error at the time.
const CallerLine = "\n\nThe session you are accompanying is named `%s`. That name, exactly, " +
	"is what SendMessage takes; its session id and its listing ref are different " +
	"namespaces and neither addresses it. If you have no name here, say nothing " +
	"and stop: a finding delivered to the wrong session is worse than one not " +
	"delivered, because it is somebody else's business."

// brief returns the user message the session opens with.
func brief(role, slug, pr, supplied string) (string, error) {
	if role == RoleReviewer {
		if supplied != "" {
			return "", fmt.Errorf("a reviewer's brief is composed by wf, not by the caller. " +
				"An author who summons its own reviewer must not also frame what it looks at")
		}
		if pr == "" {
			return "", fmt.Errorf("wf spawn reviewer needs --pr N")
		}
		return fmt.Sprintf(ReviewerBrief, pr, slug), nil
	}
	if supplied == "" {
		return "", fmt.Errorf("wf spawn %s needs a brief", role)
	}
	return supplied, nil
}

// RoleReviewer is special-cased in exactly two places: the brief above, and
// origin resolution. Named so both are greppable.
const RoleReviewer = "reviewer"
