package spawn

import (
	"strings"
	"testing"

	"github.com/jschell12/workforce/internal/registry"
	"github.com/jschell12/workforce/internal/session"
)

func TestWorkerPoolDefaultAndExplicit(t *testing.T) {
	w := world(t)
	w.Live = []session.Session{{ID: "abc12345", Name: "permitguv1", Cwd: "/tmp/alpha", Status: "idle"}}
	r := role(t, "alpha", "worker")
	p, err := Build(r, Request{Persona: "coordinator", Brief: "coordinate"}, w)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(p.Argv, " ")
	if !strings.Contains(argv, freshWorkerPolicy) || strings.Contains(argv, "permitguv1") {
		t.Fatal(argv)
	}
	p, err = Build(r, Request{Persona: "coordinator", Brief: "coordinate", SessionsSet: true, Sessions: "permitguv1"}, w)
	if err != nil {
		t.Fatal(err)
	}
	argv = strings.Join(p.Argv, " ")
	if !strings.Contains(argv, `name "permitguv1", ref "abc12345"`) || !strings.Contains(argv, "Before every dispatch") {
		t.Fatal(argv)
	}
	if p.TokenKey != "store:ALPHA_WORKER" || !strings.Contains(argv, "--permission-mode acceptEdits") {
		t.Fatal("identity/permissions changed")
	}
}

func TestWorkerPoolRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, pool, state, status, cwd, persona string
		ambiguous                               bool
	}{
		{"empty", "", "", "idle", "/tmp/alpha", "coordinator", false},
		{"empty element", "one,", "", "idle", "/tmp/alpha", "coordinator", false},
		{"duplicate aliases", "one,abc12345", "", "idle", "/tmp/alpha", "coordinator", false},
		{"missing", "missing", "", "idle", "/tmp/alpha", "coordinator", false},
		{"busy", "one", "", "busy", "/tmp/alpha", "coordinator", false},
		{"contradiction", "one", "blocked", "idle", "/tmp/alpha", "coordinator", false},
		{"unknown", "one", "", "", "/tmp/alpha", "coordinator", false},
		{"parent cwd", "one", "", "idle", "/tmp", "coordinator", false},
		{"wrong persona", "one", "", "idle", "/tmp/alpha", "implementer", false},
		{"ambiguous", "one", "", "idle", "/tmp/alpha", "coordinator", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := world(t)
			w.Live = []session.Session{{ID: "abc12345", Name: "one", Cwd: tc.cwd, State: tc.state, Status: tc.status}}
			if tc.ambiguous {
				w.Live = append(w.Live, session.Session{ID: "other", Name: "one", Cwd: tc.cwd, Status: "idle"})
			}
			_, err := Build(role(t, "alpha", "worker"), Request{Brief: "go", Persona: tc.persona, SessionsSet: true, Sessions: tc.pool}, w)
			if err == nil {
				t.Fatal("accepted invalid pool")
			}
		})
	}
}

func TestWorkerPoolRegisteredWorktreeAndReviewerRefusal(t *testing.T) {
	w := world(t)
	w.Live = []session.Session{{ID: "abc12345", Name: "one", Cwd: "/tmp/alpha-worker", Status: "idle"}}
	w.AllRegs = []registry.Entry{{BgID: "abc12345", Name: "one", Role: "worker", Repo: "alpha", Worktree: "/tmp/alpha-worker"}}
	req := Request{Persona: "coordinator", Brief: "go", SessionsSet: true, Sessions: "one"}
	if _, err := Build(role(t, "alpha", "worker"), req, w); err != nil {
		t.Fatal(err)
	}
	w.AllRegs[0].Role = "reviewer"
	if _, err := Build(role(t, "alpha", "worker"), req, w); err == nil {
		t.Fatal("reused reviewer")
	}
	if _, err := Build(role(t, "alpha", "reviewer"), Request{PR: "1", SessionsSet: true, Sessions: "one"}, w); err == nil {
		t.Fatal("reviewer accepted pool")
	}
}

func TestMachineCoordinatorFreshPolicy(t *testing.T) {
	r := role(t, "", "base")
	r.Name = "workforce"
	w := world(t)
	p, err := Build(r, Request{Brief: "coordinate"}, w)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(p.Argv, " "), freshWorkerPolicy) {
		t.Fatal("missing fresh policy")
	}
	if _, err := Build(r, Request{Brief: "coordinate", SessionsSet: true, Sessions: "one"}, w); err == nil {
		t.Fatal("unscoped pool accepted")
	}
}
