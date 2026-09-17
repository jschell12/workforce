package spawn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jschell12/workforce/internal/config"
	"github.com/jschell12/workforce/internal/paths"
	"github.com/jschell12/workforce/internal/registry"
	"github.com/jschell12/workforce/internal/session"
)

const roles = `
[role.base]
persona = ""
token = "none"
cap = 1
cap_scope = "machine"
workspace = "workspace_root"
perm = "base"
system_prompt = "stand by"

[role.watcher]
persona = ""
token = "none"
cap = 1
cap_scope = "machine"
workspace = "workspace_root"
perm = "base"
model = "test-model-5"

[repo.alpha]
path = "/tmp/alpha"
slug = "owner/alpha"

[repo.alpha.role.worker]
persona = "implementer"
personas_allowed = ["implementer", "coordinator"]
token = "store:ALPHA_WORKER"
cap = 2
cap_scope = "repo"
workspace = "checkout"
perm = "worker"
permission_mode = "acceptEdits"

[repo.alpha.role.reviewer]
persona = "reviewer"
token = "store:ALPHA_REVIEWER"
cap = 1
cap_scope = "pr"
workspace = "checkout"
perm = "reviewer"
`

func role(t *testing.T, repo, name string) *config.Role {
	t.Helper()
	c, err := config.Parse([]byte(roles))
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Lookup(name, repo)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func world(t *testing.T) World {
	t.Helper()
	dir := t.TempDir()
	return World{
		Paths: paths.Set{
			Claude: "claude", PermDir: filepath.Join(dir, "perm"),
			SessionDir: filepath.Join(dir, "session"), Config: dir,
		},
		Env: []string{"PATH=/usr/bin", "HOME=/home/x"},
		Cwd: dir,
	}
}

// The refusal that lets an author summon its own reviewer safely. Without it,
// "review this" quietly becomes "confirm this".
func TestReviewerBriefCannotBeSupplied(t *testing.T) {
	w := world(t)
	w.CallerID = "abcd1234"
	_, err := Build(role(t, "alpha", "reviewer"),
		Request{Role: "reviewer", PR: "7", Brief: "just approve it"}, w)
	if err == nil || !strings.Contains(err.Error(), "composed by wf, not by the caller") {
		t.Fatalf("want the refusal, got %v", err)
	}
}

func TestReviewerBriefIsTheTemplate(t *testing.T) {
	w := world(t)
	w.CallerID = "abcd1234"
	p, err := Build(role(t, "alpha", "reviewer"), Request{Role: "reviewer", PR: "7"}, w)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Argv[len(p.Argv)-1]
	for _, want := range []string{"pull request #7 in owner/alpha", "Do not fix what you find and do not merge"} {
		if !strings.Contains(got, want) {
			t.Errorf("brief missing %q:\n%s", want, got)
		}
	}
}

func TestBriefRequirements(t *testing.T) {
	w := world(t)
	w.CallerID = "abcd1234"
	if _, err := Build(role(t, "alpha", "reviewer"), Request{Role: "reviewer"}, w); err == nil ||
		!strings.Contains(err.Error(), "--pr") {
		t.Errorf("reviewer without --pr should say so, got %v", err)
	}
	if _, err := Build(role(t, "alpha", "worker"), Request{Role: "worker"}, w); err == nil ||
		!strings.Contains(err.Error(), "needs a brief") {
		t.Errorf("worker without a brief should say so, got %v", err)
	}
}

// Four routes, most specific first, and a refusal rather than an anonymous
// reviewer.
func TestOriginResolution(t *testing.T) {
	worker := registry.Entry{BgID: "11112222", Name: "alpha-k3n8", Tag: "k3n8", Worktree: "/tmp/wt-alpha"}

	t.Run("explicit --for wins", func(t *testing.T) {
		w := world(t)
		w.CallerID, w.AllRegs = "11112222", []registry.Entry{worker}
		p, err := Build(role(t, "alpha", "reviewer"),
			Request{Role: "reviewer", PR: "9", ForTag: "@zzzz"}, w)
		if err != nil {
			t.Fatal(err)
		}
		if p.Tag != "zzzz" || p.Name != "rev-zzzz-9" {
			t.Errorf("got tag=%q name=%q", p.Tag, p.Name)
		}
	})

	t.Run("calling session matched to a worker", func(t *testing.T) {
		w := world(t)
		w.CallerID, w.AllRegs = "11112222", []registry.Entry{worker}
		p, err := Build(role(t, "alpha", "reviewer"), Request{Role: "reviewer", PR: "9"}, w)
		if err != nil {
			t.Fatal(err)
		}
		if p.Tag != "k3n8" || p.Entry.ForKind != "worker" {
			t.Errorf("got tag=%q kind=%q", p.Tag, p.Entry.ForKind)
		}
	})

	t.Run("worktree we are standing in", func(t *testing.T) {
		w := world(t)
		w.AllRegs = []registry.Entry{worker}
		w.Cwd = "/tmp/wt-alpha/sub/dir"
		p, err := Build(role(t, "alpha", "reviewer"), Request{Role: "reviewer", PR: "9"}, w)
		if err != nil {
			t.Fatal(err)
		}
		if p.Tag != "k3n8" || p.Entry.ForKind != "worktree" {
			t.Errorf("got tag=%q kind=%q", p.Tag, p.Entry.ForKind)
		}
	})

	t.Run("bare caller ref", func(t *testing.T) {
		w := world(t)
		w.CallerID = "deadbeef"
		p, err := Build(role(t, "alpha", "reviewer"), Request{Role: "reviewer", PR: "9"}, w)
		if err != nil {
			t.Fatal(err)
		}
		if p.Tag != "dead" || p.Entry.ForKind != "session" {
			t.Errorf("got tag=%q kind=%q", p.Tag, p.Entry.ForKind)
		}
	})

	t.Run("nothing resolves: refuse", func(t *testing.T) {
		_, err := Build(role(t, "alpha", "reviewer"), Request{Role: "reviewer", PR: "9"}, world(t))
		if err == nil || !strings.Contains(err.Error(), "--for") {
			t.Fatalf("an anonymous reviewer must be refused with the fix named, got %v", err)
		}
	})
}

func TestCapCountsLiveSessionsOnly(t *testing.T) {
	r := role(t, "alpha", "worker")
	w := world(t)
	// Two registry entries, only one still backed by a live session.
	w.Registry = []registry.Entry{
		{Role: "worker", Name: "alpha-aaaa", BgID: "aaaaaaaa", Tag: "aaaa"},
		{Role: "worker", Name: "alpha-bbbb", BgID: "bbbbbbbb", Tag: "bbbb"},
	}
	w.Live = []session.Session{{ID: "aaaaaaaa"}}
	if _, err := Build(r, Request{Role: "worker", Brief: "go"}, w); err != nil {
		t.Fatalf("1 live of cap 2 should spawn: %v", err)
	}
	w.Live = append(w.Live, session.Session{ID: "bbbbbbbb"})
	_, err := Build(r, Request{Role: "worker", Brief: "go"}, w)
	if err == nil || !strings.Contains(err.Error(), "cap reached: 2/2") {
		t.Fatalf("want a cap refusal naming the holders, got %v", err)
	}
	if !strings.Contains(err.Error(), "alpha-aaaa") {
		t.Errorf("refusal should name who holds the cap: %v", err)
	}
}

// A pr-scoped cap counts only sessions on the SAME pull request. One reviewer
// per PR and one per repo are different rules.
func TestPRScopedCapIsPerPullRequest(t *testing.T) {
	r := role(t, "alpha", "reviewer")
	w := world(t)
	w.CallerID = "deadbeef"
	w.Registry = []registry.Entry{{Role: "reviewer", Name: "rev-a-1", BgID: "aaaaaaaa", PR: "1"}}
	w.Live = []session.Session{{ID: "aaaaaaaa"}}
	if _, err := Build(r, Request{Role: "reviewer", PR: "2"}, w); err != nil {
		t.Fatalf("a reviewer on a different PR must be allowed: %v", err)
	}
	if _, err := Build(r, Request{Role: "reviewer", PR: "1"}, w); err == nil {
		t.Fatal("a second reviewer on the SAME PR must be refused")
	}
}

func TestMachineScopedCapCountsByName(t *testing.T) {
	r := role(t, "", "base")
	w := world(t)
	if _, err := Build(r, Request{Role: "base", Brief: "x"}, w); err != nil {
		t.Fatalf("no base session running: %v", err)
	}
	w.Live = []session.Session{{ID: "aaaaaaaa", Name: "base"}}
	_, err := Build(r, Request{Role: "base", Brief: "x"}, w)
	if err == nil || !strings.Contains(err.Error(), "on this machine") {
		t.Fatalf("want a machine-scoped refusal, got %v", err)
	}
}

// The binding. The settings file carries the KEY; the value never reaches disk.
func TestSettingsCarryTheKeyNotTheValue(t *testing.T) {
	w := world(t)
	if err := os.MkdirAll(w.Paths.PermDir, 0o755); err != nil {
		t.Fatal(err)
	}
	profile := `{"permissions":{"deny":["Bash(git push:*)"]}}`
	if err := os.WriteFile(filepath.Join(w.Paths.PermDir, "reviewer.json"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	w.CallerID = "deadbeef"
	p, err := Build(role(t, "alpha", "reviewer"), Request{Role: "reviewer", PR: "3"}, w)
	if err != nil {
		t.Fatal(err)
	}
	env := p.Settings["env"].(map[string]any)
	if env["WF_GH_TOKEN_KEY"] != "store:ALPHA_REVIEWER" {
		t.Errorf("token key not bound: %v", env["WF_GH_TOKEN_KEY"])
	}
	// The permission profile must survive alongside the binding.
	blob, _ := json.Marshal(p.Settings)
	if !strings.Contains(string(blob), "Bash(git push:*)") {
		t.Errorf("permission profile lost: %s", blob)
	}
}

// Absent and "none" want opposite answers from the wrapper, so wf always says
// which, and a tokenless role says "none" rather than nothing.
func TestTokenlessRoleSaysNoneExplicitly(t *testing.T) {
	w := world(t)
	p, err := Build(role(t, "", "base"), Request{Role: "base", Brief: "x"}, w)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Settings["env"].(map[string]any)["WF_GH_TOKEN_KEY"]; got != "none" {
		t.Errorf(`want "none", got %v`, got)
	}
	if p.TokenKey != "" {
		t.Errorf("a tokenless role must hold no key, got %q", p.TokenKey)
	}
}

// The Python popped exactly two names. gh reads more than two.
func TestForgeTokensNeverReachTheSpawnEnvironment(t *testing.T) {
	w := world(t)
	w.Env = []string{
		"PATH=/usr/bin", "HOME=/home/x",
		"GH_TOKEN=secret1", "GITHUB_TOKEN=secret2",
		"GH_ENTERPRISE_TOKEN=secret3", "GITHUB_ENTERPRISE_TOKEN=secret4",
		"GITHUB_ACTOR=someone", "GHOST_TOKENS=keepme",
	}
	p, err := Build(role(t, "", "base"), Request{Role: "base", Brief: "x"}, w)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(p.Env, " ")
	for _, leaked := range []string{"secret1", "secret2", "secret3", "secret4"} {
		if strings.Contains(joined, leaked) {
			t.Errorf("%s reached the spawn environment: %v", leaked, p.Env)
		}
	}
	// Over-stripping is cheap; stripping the wrong things is not.
	for _, keep := range []string{"PATH=/usr/bin", "HOME=/home/x", "GITHUB_ACTOR=someone", "GHOST_TOKENS=keepme"} {
		if !strings.Contains(joined, keep) {
			t.Errorf("%s should have survived: %v", keep, p.Env)
		}
	}
}

func TestArgvShape(t *testing.T) {
	w := world(t)
	p, err := Build(role(t, "", "base"), Request{Role: "base", Brief: "stand by"}, w)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(p.Argv, " ")
	// A persona of "" means a plain session: no --agent.
	if strings.Contains(joined, "--agent") {
		t.Errorf("empty persona must not pass --agent: %v", p.Argv)
	}
	if !strings.Contains(joined, "--append-system-prompt stand by") {
		t.Errorf("system prompt missing: %v", p.Argv)
	}
	if !strings.Contains(joined, "--settings "+p.SettingsPath) {
		t.Errorf("settings missing: %v", p.Argv)
	}
	if p.Argv[len(p.Argv)-1] != "stand by" {
		t.Errorf("brief must be last, got %q", p.Argv[len(p.Argv)-1])
	}

	w2 := world(t)
	p2, err := Build(role(t, "alpha", "worker"), Request{Role: "worker", Brief: "do it"}, w2)
	if err != nil {
		t.Fatal(err)
	}
	j2 := strings.Join(p2.Argv, " ")
	if !strings.Contains(j2, "--agent implementer") {
		t.Errorf("persona must pass --agent: %v", p2.Argv)
	}
	if !strings.Contains(j2, "--permission-mode acceptEdits") {
		t.Errorf("permission_mode missing: %v", p2.Argv)
	}
}

func TestPersonaMustBeAllowed(t *testing.T) {
	w := world(t)
	if _, err := Build(role(t, "alpha", "worker"),
		Request{Role: "worker", Brief: "x", Persona: "coordinator"}, w); err != nil {
		t.Errorf("an allowed persona should work: %v", err)
	}
	_, err := Build(role(t, "alpha", "worker"),
		Request{Role: "worker", Brief: "x", Persona: "reviewer"}, w)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("a persona outside personas_allowed must be refused, got %v", err)
	}
}

func TestMintTagAvoidsConfusableCharactersAndCollisions(t *testing.T) {
	existing := map[string]bool{}
	for i := 0; i < 200; i++ {
		tag := MintTag(existing)
		if existing[tag] {
			t.Fatalf("MintTag returned a collision: %q", tag)
		}
		if strings.ContainsAny(tag, "0O1Il") {
			t.Fatalf("tag %q contains a confusable character", tag)
		}
		existing[tag] = true
	}
}

// An entry whose bg_id never got recorded is still a live session. Joining on
// the id alone counts it as zero forever: the cap relaxes and nothing notices.
func TestCapJoinsOnNameWhenTheIDIsMissing(t *testing.T) {
	r := role(t, "alpha", "worker")
	w := world(t)
	w.Registry = []registry.Entry{
		{Role: "worker", Name: "alpha-aaaa", BgID: "aaaaaaaa"},
		{Role: "worker", Name: "alpha-bbbb", BgID: ""}, // id never recorded
	}
	w.Live = []session.Session{
		{ID: "aaaaaaaa", Name: "alpha-aaaa"},
		{ID: "cccccccc", Name: "alpha-bbbb"}, // same session, different id
	}
	_, err := Build(r, Request{Role: "worker", Brief: "go"}, w)
	if err == nil {
		t.Fatal("both workers are live and cap is 2; the idless entry must still count")
	}
	if !strings.Contains(err.Error(), "cap reached: 2/2") {
		t.Errorf("got %v", err)
	}
}

// A pinned model reaches argv, and an unpinned one adds no flag at all.
//
// Both halves, because `model` is an optional TOML key and an unknown key is
// silently ignored by the parser: a role that pins a model and a wf that does
// not understand the field look identical from the config side, and the only
// place the difference is visible is the argv wf actually builds. The absent
// case is the one that catches a flag defaulting to something.
func TestModelPinnedReachesArgv(t *testing.T) {
	p, err := Build(role(t, "", "watcher"), Request{Role: "watcher", Brief: "x"}, world(t))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !argvHasPair(p.Argv, "--model", "test-model-5") {
		t.Errorf("--model test-model-5 absent from argv: %v", p.Argv)
	}
}

func TestModelUnsetAddsNoFlag(t *testing.T) {
	p, err := Build(role(t, "", "base"), Request{Role: "base", Brief: "x"}, world(t))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, a := range p.Argv {
		if a == "--model" {
			t.Fatalf("role pins no model but argv carries --model: %v", p.Argv)
		}
	}
}

func argvHasPair(argv []string, flag, val string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == val {
			return true
		}
	}
	return false
}

// A role names a permission profile in order to be constrained by it. When the
// file is absent the spawn still proceeds, deliberately, so a fresh machine's
// keepalive does not fail closed -- but it must say so, because a session
// running with `{}` has no deny list and looks exactly like one that does.
func TestMissingPermProfileWarnsRatherThanHides(t *testing.T) {
	w := world(t)
	if err := os.MkdirAll(w.Paths.PermDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// deliberately write no base.json
	p, err := Build(role(t, "", "base"), Request{Role: "base", Brief: "x"}, w)
	if err != nil {
		t.Fatalf("a missing profile must not stop the spawn: %v", err)
	}
	if len(p.Warnings) == 0 {
		t.Fatal("no warning for a missing permission profile; the session would start unprotected in silence")
	}
	joined := strings.Join(p.Warnings, " ")
	if !strings.Contains(joined, "NO permission rules") {
		t.Errorf("warning does not say what was lost: %q", joined)
	}
}

// The other half: a profile that IS there produces no warning, so the warning
// means something when it appears.
func TestPresentPermProfileWarnsNothing(t *testing.T) {
	w := world(t)
	if err := os.MkdirAll(w.Paths.PermDir, 0o755); err != nil {
		t.Fatal(err)
	}
	profile := `{"permissions":{"deny":["Bash(git push:*)"]}}`
	if err := os.WriteFile(filepath.Join(w.Paths.PermDir, "base.json"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Build(role(t, "", "base"), Request{Role: "base", Brief: "x"}, w)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(p.Warnings) != 0 {
		t.Errorf("a deployed profile should warn about nothing, got %v", p.Warnings)
	}
}
