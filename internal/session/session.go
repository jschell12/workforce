// Package session reads the list of Claude Code sessions on this machine.
//
// Two rules here are not style. Each was a production failure first.
//
// LIVENESS IS MEMBERSHIP IN THE PLAIN LISTING, NOT A STATE WORD. `claude agents
// --json` lists active sessions; `--all` adds finished ones. A session in the
// plain listing is running whatever word it carries. Measured 2026-09-11: one
// session read `done` in --all and was absent from the plain listing, so it had
// finished; another read `done` in both and was live. `done` settles nothing in
// either direction. That is why Plain and All are different types and only
// Plain can answer the question: calling the old is_live() on a --all row was a
// documented footgun, and a footgun a comment warns about is one the type
// system should refuse.
//
// AN UNREADABLE LISTING IS NOT AN EMPTY ONE. The Python returned None or a
// list and warned callers not to conflate them. Go returns an error, so the
// distinction survives without anyone remembering it. Treating a failed read as
// "nothing is running" is how a cap becomes decorative and a keepalive starts a
// duplicate every tick.
package session

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// deadStates is a DENYLIST, and the polarity is the whole point.
//
// An allowlist of live states lived here until 2026-09-10 and shipped a bug the
// same day: `blocked` appeared, was in no vocabulary anyone had enumerated, fell
// through to "not running", and a keepalive started a second base session two
// minutes after the first. The vocabularies are neither documented nor stable,
// so an allowlist is a promise about a set nobody has seen the whole of. A
// denylist fails safe: an unrecognised state counts as ALIVE, so the worst case
// is refusing to start something rather than starting a duplicate.
//
// Known live: busy, working, idle, done, waiting, blocked.
var deadStates = map[string]bool{
	"completed": true, "failed": true, "stopped": true,
	"removed": true, "error": true, "killed": true,
}

// Session is one row of `claude agents --json`.
type Session struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Cwd       string `json:"cwd"`
	Kind      string `json:"kind"`

	// Background rows carry State; interactive rows carry Status. Read both.
	State  string `json:"state"`
	Status string `json:"status"`
}

// Ref is the short handle that identifies a session everywhere else.
func (s Session) Ref() string {
	if s.ID != "" {
		return s.ID
	}
	if len(s.SessionID) > 8 {
		return s.SessionID[:8]
	}
	return s.SessionID
}

// StateWord is whichever of the two fields this row kind uses.
func (s Session) StateWord() string {
	switch {
	case s.State != "":
		return s.State
	case s.Status != "":
		return s.Status
	default:
		return "unknown"
	}
}

// Plain is the active listing. Membership in it means running.
type Plain []Session

// All includes finished sessions. It deliberately has no liveness method: a
// finished row here carries a state word that looks alive.
type All []Session

// Live drops any row that says outright that it finished. Membership is the
// real test; this is only a second filter for a row that contradicts itself.
func (p Plain) Live() []Session {
	out := make([]Session, 0, len(p))
	for _, s := range p {
		if !deadStates[strings.ToLower(s.StateWord())] {
			out = append(out, s)
		}
	}
	return out
}

// Find returns the session whose ref or name matches target exactly, or whose
// ref has it as an unambiguous prefix.
// Names are reused by construction: a finished session stays in the --all
// listing until the clear pass, so a respawned reviewer of the same name
// overlaps a stale row. Exact matches are therefore COLLECTED and an ambiguous
// one refused, the same as prefixes. Returning the first is how a destructive
// command acts on the wrong session.
func Find(rows []Session, target string) (Session, error) {
	var exact, pre []Session
	for _, s := range rows {
		switch {
		case s.Ref() == target || s.Name == target:
			exact = append(exact, s)
		case strings.HasPrefix(s.Ref(), target):
			pre = append(pre, s)
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	if len(exact) > 1 {
		return Session{}, ambiguous(target, exact)
	}
	switch len(pre) {
	case 1:
		return pre[0], nil
	case 0:
		return Session{}, fmt.Errorf("no session matching %q", target)
	default:
		return Session{}, ambiguous(target, pre)
	}
}

func ambiguous(target string, hits []Session) error {
	names := make([]string, len(hits))
	for i, s := range hits {
		names[i] = fmt.Sprintf("%s [%s]", s.Name, s.Ref())
	}
	return fmt.Errorf("%q matches %d sessions: %s. Use a ref", target, len(hits), strings.Join(names, ", "))
}

// Client runs the claude binary.
type Client struct{ Bin string }

// List returns the active sessions.
func (c Client) List() (Plain, error) {
	rows, err := c.run()
	return Plain(rows), err
}

// ListAll returns active and finished sessions.
func (c Client) ListAll() (All, error) {
	rows, err := c.run("--all")
	return All(rows), err
}

func (c Client) run(extra ...string) ([]Session, error) {
	args := append([]string{"agents", "--json"}, extra...)
	out, err := exec.Command(c.Bin, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("cannot read `%s agents --json`: %w", c.Bin, err)
	}
	var rows []Session
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("cannot read `%s agents --json`: it ran but did not parse: %w", c.Bin, err)
	}
	return rows, nil
}
