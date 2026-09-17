// Package config loads roles.toml: which persona a role runs, which credential
// it holds, how many may run at once, and where it works.
//
// The file is the authority on things a session must not decide for itself, so
// the parser's job is to turn loose TOML into types that cannot express a
// meaningless state. Two distinctions earn their own types:
//
//   - A token that is ABSENT and a token that is the literal string "none" are
//     different instructions, not the same one written two ways. Absent means
//     "whatever account the environment already has"; "none" means "no forge
//     account at all", and the one role defined by holding neither account is
//     the one that must not get that wrong.
//   - A cap is meaningless without the scope it counts within. One reviewer per
//     PULL REQUEST and one reviewer per MACHINE are different rules, and a bare
//     integer invites reading them as the same.
package config

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// TokenKind distinguishes the three states a role's `token` field can be in.
type TokenKind int

const (
	// TokenUnset is an absent `token` field: inherit whatever is ambient.
	TokenUnset TokenKind = iota
	// TokenNone is `token = "none"`: the session gets no forge credential.
	TokenNone
	// TokenKey is `token = "<provider>:<NAME>"`: a credential-store KEY. Never
	// a secret value; key names are not sensitive and this file is readable.
	TokenKey
)

// Token is a role's credential instruction.
type Token struct {
	Kind TokenKind
	Key  string // set only when Kind is TokenKey
}

func (t Token) String() string {
	switch t.Kind {
	case TokenNone:
		return "none"
	case TokenKey:
		return t.Key
	default:
		return "<unset>"
	}
}

// CapScope is the population a role's cap counts within.
type CapScope string

const (
	ScopeMachine CapScope = "machine" // one per host
	ScopeRepo    CapScope = "repo"    // one per repository
	ScopePR      CapScope = "pr"      // one per pull request
)

func (s CapScope) valid() bool {
	return s == ScopeMachine || s == ScopeRepo || s == ScopePR
}

// Workspace is where a role's session is rooted.
type Workspace string

const (
	// WorkspaceRoot is the shared parent directory; not a repository.
	WorkspaceRoot Workspace = "workspace_root"
	// WorkspaceWorktree cuts a fresh git worktree so concurrent sessions in one
	// repo cannot edit each other's files.
	WorkspaceWorktree Workspace = "worktree"
	// WorkspaceCheckout uses the repository as it stands. Correct for a role
	// that only reads, and wrong for one that writes.
	WorkspaceCheckout Workspace = "checkout"
)

func (w Workspace) valid() bool {
	return w == WorkspaceRoot || w == WorkspaceWorktree || w == WorkspaceCheckout
}

// Role is one spawnable kind of session.
type Role struct {
	Name            string
	Persona         string   // "" means a plain session with no replacement prompt
	PersonasAllowed []string // what --persona may select; empty means Persona only
	Token           Token
	Cap             int
	CapScope        CapScope
	Workspace       Workspace
	Perm            string // permission profile basename under PermDir
	PermissionMode  string // passed to the session; empty means the default
	SystemPrompt    string // --append-system-prompt; standing instruction, not a brief
	Owner           string // documentation: "user" marks a seat that is not a machine account

	// Model pins the session's model. Empty inherits whatever the client would
	// pick, which is right for a role doing the work and wrong for one that
	// watches somebody else do it: a watcher runs as long as the session it
	// watches, so the model it runs on is the difference between leaving it on
	// and not affording to.
	Model string

	// Repo is empty for a machine-scoped role.
	Repo *Repo
}

// MachineScoped reports whether this role takes no repository argument.
func (r *Role) MachineScoped() bool { return r.Repo == nil }

// Repo is a repository wf can run roles against.
type Repo struct {
	Name  string
	Path  string
	Slug  string
	Roles map[string]*Role
}

// Config is a parsed roles.toml.
type Config struct {
	WorkspaceRoot string
	Roles         map[string]*Role // machine-scoped only
	Repos         map[string]*Repo
}

// ---- wire types: the shape TOML actually has ----

type wireRole struct {
	Persona         *string  `toml:"persona"`
	PersonasAllowed []string `toml:"personas_allowed"`
	Token           *string  `toml:"token"`
	Cap             *int     `toml:"cap"`
	CapScope        *string  `toml:"cap_scope"`
	Workspace       *string  `toml:"workspace"`
	Perm            *string  `toml:"perm"`
	PermissionMode  *string  `toml:"permission_mode"`
	SystemPrompt    *string  `toml:"system_prompt"`
	Owner           *string  `toml:"owner"`
	Model           *string  `toml:"model"`
}

type wireRepo struct {
	Path string               `toml:"path"`
	Slug string               `toml:"slug"`
	Role map[string]*wireRole `toml:"role"`
}

type wireConfig struct {
	Defaults struct {
		WorkspaceRoot string `toml:"workspace_root"`
	} `toml:"defaults"`
	Role map[string]*wireRole `toml:"role"`
	Repo map[string]*wireRepo `toml:"repo"`
}

// Load reads and validates roles.toml.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(b)
}

// Parse validates an in-memory roles.toml. Split from Load so tests do not
// need a file and so a caller can validate a candidate before installing it.
func Parse(b []byte) (*Config, error) {
	var w wireConfig
	if err := toml.Unmarshal(b, &w); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg := &Config{
		WorkspaceRoot: w.Defaults.WorkspaceRoot,
		Roles:         map[string]*Role{},
		Repos:         map[string]*Repo{},
	}
	for name, wr := range w.Role {
		r, err := convert(name, wr)
		if err != nil {
			return nil, fmt.Errorf("role %q: %w", name, err)
		}
		cfg.Roles[name] = r
	}
	for rname, wrepo := range w.Repo {
		if wrepo.Path == "" {
			return nil, fmt.Errorf("repo %q: path is required", rname)
		}
		repo := &Repo{Name: rname, Path: wrepo.Path, Slug: wrepo.Slug, Roles: map[string]*Role{}}
		for roleName, wr := range wrepo.Role {
			r, err := convert(roleName, wr)
			if err != nil {
				return nil, fmt.Errorf("repo %q role %q: %w", rname, roleName, err)
			}
			r.Repo = repo
			repo.Roles[roleName] = r
		}
		cfg.Repos[rname] = repo
	}
	return cfg, nil
}

func convert(name string, w *wireRole) (*Role, error) {
	r := &Role{Name: name, Cap: 1, CapScope: ScopeRepo, Workspace: WorkspaceCheckout}
	if w == nil {
		return r, nil
	}
	if w.Persona != nil {
		r.Persona = *w.Persona
	}
	r.PersonasAllowed = w.PersonasAllowed
	// Absent and "none" are deliberately different; see the package comment.
	switch {
	case w.Token == nil:
		r.Token = Token{Kind: TokenUnset}
	case *w.Token == "none":
		r.Token = Token{Kind: TokenNone}
	case strings.TrimSpace(*w.Token) == "":
		return nil, fmt.Errorf(`token is empty; write token = "none" to mean no account, or omit it to inherit`)
	default:
		r.Token = Token{Kind: TokenKey, Key: *w.Token}
	}
	if w.Cap != nil {
		if *w.Cap < 1 {
			return nil, fmt.Errorf("cap must be at least 1, got %d", *w.Cap)
		}
		r.Cap = *w.Cap
	}
	if w.CapScope != nil {
		r.CapScope = CapScope(*w.CapScope)
		if !r.CapScope.valid() {
			return nil, fmt.Errorf("cap_scope %q is not one of machine, repo, pr", *w.CapScope)
		}
	}
	if w.Workspace != nil {
		r.Workspace = Workspace(*w.Workspace)
		if !r.Workspace.valid() {
			return nil, fmt.Errorf("workspace %q is not one of workspace_root, worktree, checkout", *w.Workspace)
		}
	}
	if w.Perm != nil {
		r.Perm = *w.Perm
	}
	if w.PermissionMode != nil {
		r.PermissionMode = *w.PermissionMode
	}
	if w.Model != nil {
		r.Model = *w.Model
	}
	if w.SystemPrompt != nil {
		r.SystemPrompt = *w.SystemPrompt
	}
	if w.Owner != nil {
		r.Owner = *w.Owner
	}
	return r, nil
}

// Lookup resolves a role, with or without a repository.
//
// The error text names what IS available, because the failure this replaces
// ("unknown repo 'workforce'") is most often a config gap rather than a typo,
// and the list is what tells them apart.
func (c *Config) Lookup(role, repo string) (*Role, error) {
	if repo == "" {
		r, ok := c.Roles[role]
		if !ok {
			return nil, fmt.Errorf("unknown machine-scoped role %q. Known: %s", role, keys(c.Roles))
		}
		return r, nil
	}
	rp, ok := c.Repos[repo]
	if !ok {
		return nil, fmt.Errorf("unknown repo %q. Known: %s", repo, keys(c.Repos))
	}
	r, ok := rp.Roles[role]
	if !ok {
		return nil, fmt.Errorf("unknown role %q for %s. Known: %s", role, repo, keys(rp.Roles))
	}
	return r, nil
}

// PersonaAllowed reports whether --persona may select p for this role.
func (r *Role) PersonaAllowed(p string) bool {
	if p == "" || p == r.Persona {
		return true
	}
	for _, a := range r.PersonasAllowed {
		if a == p {
			return true
		}
	}
	return false
}

func keys[V any](m map[string]V) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return "(none)"
	}
	return strings.Join(out, ", ")
}
