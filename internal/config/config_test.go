package config

import (
	"path/filepath"
	"strings"
	"testing"
)

const mini = `
[defaults]
workspace_root = "~/src"

[role.base]
persona = ""
token = "none"
cap = 1
cap_scope = "machine"
workspace = "workspace_root"
perm = "base"

[repo.alpha]
path = "~/src/alpha"
slug = "owner/alpha"

[repo.alpha.role.worker]
persona = "implementer"
personas_allowed = ["implementer", "coordinator"]
token = "store:ALPHA_WORKER"
cap = 3
cap_scope = "repo"
workspace = "worktree"
perm = "worker"
permission_mode = "acceptEdits"

[repo.alpha.role.reviewer]
persona = "reviewer"
cap = 1
cap_scope = "pr"
workspace = "checkout"
perm = "reviewer"
`

func load(t *testing.T, s string) *Config {
	t.Helper()
	c, err := Parse([]byte(s))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

// The distinction this asserts is the one the whole file exists to protect:
// an omitted token and token = "none" are different instructions.
func TestTokenTriState(t *testing.T) {
	c := load(t, mini)
	if got := c.Roles["base"].Token; got.Kind != TokenNone {
		t.Errorf(`token = "none" parsed as %v, want TokenNone`, got.Kind)
	}
	if got := c.Repos["alpha"].Roles["worker"].Token; got.Kind != TokenKey || got.Key != "store:ALPHA_WORKER" {
		t.Errorf("key token parsed as %+v", got)
	}
	// reviewer omits `token` entirely.
	if got := c.Repos["alpha"].Roles["reviewer"].Token; got.Kind != TokenUnset {
		t.Errorf("absent token parsed as %v, want TokenUnset", got.Kind)
	}
}

// An empty string is neither instruction and is almost certainly a typo for
// "none". Refuse it rather than silently picking one.
func TestEmptyTokenRejected(t *testing.T) {
	_, err := Parse([]byte("[role.x]\ntoken = \"\"\n"))
	if err == nil || !strings.Contains(err.Error(), `token = "none"`) {
		t.Fatalf("want a refusal naming the fix, got %v", err)
	}
}

func TestMachineScoped(t *testing.T) {
	c := load(t, mini)
	if !c.Roles["base"].MachineScoped() {
		t.Error("top-level role should be machine-scoped")
	}
	if c.Repos["alpha"].Roles["worker"].MachineScoped() {
		t.Error("repo role should not be machine-scoped")
	}
}

func TestInvalidEnums(t *testing.T) {
	for _, tc := range []struct{ name, toml, want string }{
		{"cap_scope", "[role.x]\ncap_scope = \"galaxy\"\n", "machine, repo, pr"},
		{"workspace", "[role.x]\nworkspace = \"elsewhere\"\n", "workspace_root, worktree, checkout"},
		{"cap", "[role.x]\ncap = 0\n", "at least 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.toml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

// The error text is the feature: it names what exists, because the usual cause
// is a missing config block rather than a typo.
func TestLookupErrorsNameWhatExists(t *testing.T) {
	c := load(t, mini)
	if _, err := c.Lookup("reviewer", "nope"); err == nil || !strings.Contains(err.Error(), "Known: alpha") {
		t.Errorf("unknown repo error should list repos, got %v", err)
	}
	if _, err := c.Lookup("supervisor", "alpha"); err == nil ||
		!strings.Contains(err.Error(), "Known: reviewer, worker") {
		t.Errorf("unknown role error should list that repo's roles, got %v", err)
	}
	if _, err := c.Lookup("worker", "alpha"); err != nil {
		t.Errorf("valid lookup failed: %v", err)
	}
}

func TestPersonaAllowed(t *testing.T) {
	w := load(t, mini).Repos["alpha"].Roles["worker"]
	for _, p := range []string{"", "implementer", "coordinator"} {
		if !w.PersonaAllowed(p) {
			t.Errorf("persona %q should be allowed", p)
		}
	}
	if w.PersonaAllowed("reviewer") {
		t.Error("persona outside personas_allowed must be refused")
	}
	// A role with no personas_allowed still accepts its own persona.
	r := load(t, mini).Repos["alpha"].Roles["reviewer"]
	if !r.PersonaAllowed("reviewer") || r.PersonaAllowed("implementer") {
		t.Error("bare role should accept only its own persona")
	}
}

func TestDefaults(t *testing.T) {
	c := load(t, "[role.x]\n")
	r := c.Roles["x"]
	if r.Cap != 1 || r.CapScope != ScopeRepo || r.Workspace != WorkspaceCheckout {
		t.Errorf("unexpected defaults: cap=%d scope=%s workspace=%s", r.Cap, r.CapScope, r.Workspace)
	}
}

// Parsing a fixture proves the parser reads TOML. Parsing the file this repo
// actually ships proves it reads THAT, which is the claim that matters: the
// example is what a new machine copies, and an example that does not parse is
// worse than none. A fixture written alongside the parser agrees with the
// parser by construction and is not evidence.
func TestParsesShippedConfig(t *testing.T) {
	c, err := Load(filepath.Join("..", "..", "config", "roles.example.toml"))
	if err != nil {
		t.Fatalf("shipped roles.example.toml did not parse: %v", err)
	}
	if len(c.Repos) == 0 {
		t.Fatal("shipped config declared no repos")
	}
	for name, repo := range c.Repos {
		if repo.Slug == "" {
			t.Errorf("repo %q has no slug; spawn would have nothing to pass to the forge", name)
		}
		for rn, role := range repo.Roles {
			if role.Perm == "" {
				t.Errorf("%s/%s names no permission profile", name, rn)
			}
			if role.CapScope == ScopePR && role.Cap != 1 {
				t.Errorf("%s/%s is pr-scoped with cap %d; more than one per PR defeats the scope", name, rn, role.Cap)
			}
		}
	}
}
