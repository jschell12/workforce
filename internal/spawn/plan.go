package spawn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jschell12/workforce/internal/config"
	"github.com/jschell12/workforce/internal/paths"
	"github.com/jschell12/workforce/internal/registry"
	"github.com/jschell12/workforce/internal/session"
)

// Request is what the caller asked for.
type Request struct {
	Role    string
	Repo    string
	Brief   string
	PR      string
	Persona string
	Name    string
	Slug    string // the repo's forge slug, for origin route 4
	ForTag  string // --for: the caller naming its own origin
	Head    string // the PR head this reviewer answers for, if known
}

// Plan is a fully decided spawn. Nothing here has run yet.
type Plan struct {
	Name         string
	Tag          string
	Persona      string
	Workdir      string
	Branch       string // non-empty when a worktree must be created first
	Argv         []string
	Env          []string
	SettingsPath string
	Settings     map[string]any
	Entry        registry.Entry
	TokenKey     string // the KEY; the value is never held

	// Warnings are conditions a caller must see but that do not stop the spawn.
	// A plan that degrades silently is worse than one that refuses: the refusal
	// is noticed.
	Warnings []string
}

// World is the observed state a plan is built against. Passing it in keeps Build
// a pure function of (config, request, world), which is what makes the refusals
// testable without a machine to observe.
type World struct {
	Paths    paths.Set
	Live     []session.Session // from the PLAIN listing only
	Registry []registry.Entry  // this repo's entries
	AllRegs  []registry.Entry  // every repo's entries, for caller lookup
	Env      []string          // the ambient environment
	CallerID string            // CLAUDE_CODE_SESSION_ID, truncated to 8
	Cwd      string
	Stamp    int64  // unix seconds; names the worktree branch
	Root     string // defaults.workspace_root, expanded

	// HeadBranch resolves a pull request's head branch. Origin route 4 uses it
	// to pair a reviewer with the worker that cut that branch. Nil disables the
	// route rather than failing the spawn: it is one of five, and a forge that
	// cannot be reached should cost precision, not the whole operation.
	HeadBranch func(slug, pr string) (string, error)

	// ReadToken proves a credential key resolves. It returns ONLY an error:
	// the value must not cross this boundary, so there is nowhere for it to be
	// held, logged or written by accident.
	ReadToken func(key string) error
}

// Build decides everything about a spawn, or refuses.
func Build(role *config.Role, req Request, w World) (*Plan, error) {
	// The credential is resolved early to fail here rather than inside a
	// session that has already started and will sign as nobody. The VALUE is
	// discarded on the spot and never held, logged or written.
	if role.Token.Kind == config.TokenKey && w.ReadToken != nil {
		if err := w.ReadToken(role.Token.Key); err != nil {
			return nil, fmt.Errorf("could not read %s from the credential store: %w", role.Token.Key, err)
		}
	}
	if !role.PersonaAllowed(req.Persona) {
		return nil, fmt.Errorf("persona %q is not allowed for %s; allowed: %s",
			req.Persona, role.Name, strings.Join(role.PersonasAllowed, ", "))
	}
	persona := req.Persona
	if persona == "" {
		persona = role.Persona
	}

	slug := ""
	repoName := "(machine)"
	if role.Repo != nil {
		slug, repoName = role.Repo.Slug, role.Repo.Name
	}
	req.Slug = slug
	text, err := brief(role.Name, slug, req.PR, req.Brief)
	if err != nil {
		return nil, err
	}
	if err := checkCap(role, req, w); err != nil {
		return nil, err
	}

	var tag string
	var pairing registry.Entry
	switch {
	case role.MachineScoped():
		// A fixed name: launchd looks for it by name, and the whole point is
		// that there is exactly one and it is always addressable.
		if req.Name == "" {
			req.Name = role.Name
		}
	case role.Name == RoleReviewer:
		pairing, err = resolveOrigin(req, w)
		if err != nil {
			return nil, err
		}
		tag = pairing.ForTag
		if req.Name == "" {
			req.Name = fmt.Sprintf("rev-%s-%s", tag, req.PR)
		}
	default:
		tag = MintTag(knownTags(w.AllRegs))
		if req.Name == "" {
			req.Name = fmt.Sprintf("%s-%s", repoName, tag)
		}
	}

	p := &Plan{Name: req.Name, Tag: tag, Persona: persona, TokenKey: tokenKeyOf(role)}
	p.Workdir, p.Branch = workdir(role, req, w)

	settings, permWarn, err := loadPerm(w.Paths.PermDir, role)
	if err != nil {
		return nil, err
	}
	if permWarn != "" {
		p.Warnings = append(p.Warnings, permWarn)
	}
	// "none" rather than an absent key, deliberately. The wrapper must tell
	// "this role holds no token" from "wf did not say": the first keeps gh
	// unauthenticated, the second falls back to the default account. The one
	// role that exists to hold neither account is the one a silent fallback
	// would hand the broadest one.
	env, _ := settings["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	key := p.TokenKey
	if key == "" {
		key = "none"
	}
	env["WF_GH_TOKEN_KEY"] = key
	settings["env"] = env
	p.Settings = settings
	p.SettingsPath = filepath.Join(w.Paths.SessionDir, p.Name+".json")

	p.Argv = []string{w.Paths.Claude, "--bg", "-n", p.Name}
	if persona != "" {
		p.Argv = append(p.Argv, "--agent", persona)
	}
	// A role's standing instruction goes in the SYSTEM prompt, not the brief.
	// A brief is a user message and it decays: the first base session was told
	// to dispatch rather than do the work, obeyed on turn one, and on turn two
	// ran fourteen commands against a production database. Nothing held it,
	// because nothing could.
	if role.SystemPrompt != "" {
		p.Argv = append(p.Argv, "--append-system-prompt", role.SystemPrompt)
	}
	p.Argv = append(p.Argv, "--settings", p.SettingsPath)
	// Before --permission-mode only because argv order is asserted in tests;
	// the client does not care.
	if role.Model != "" {
		p.Argv = append(p.Argv, "--model", role.Model)
	}
	if role.PermissionMode != "" {
		p.Argv = append(p.Argv, "--permission-mode", role.PermissionMode)
	}
	p.Argv = append(p.Argv, text)
	p.Env = scrubForgeTokens(w.Env)

	p.Entry = registry.Entry{
		Name: p.Name, Role: role.Name, Persona: persona, Repo: repoName,
		Worktree: p.Workdir, Branch: p.Branch, PR: req.PR, Head: req.Head,
		Tag: tag, TokenKey: p.TokenKey, Status: "working",
		ForTag: pairing.ForTag, ForName: pairing.ForName,
		ForSession: pairing.ForSession, ForKind: pairing.ForKind,
	}
	return p, nil
}

func tokenKeyOf(r *config.Role) string {
	if r.Token.Kind == config.TokenKey {
		return r.Token.Key
	}
	return ""
}

// scrubForgeTokens removes forge credentials from the environment a spawn runs
// with.
//
// NOTHING FORGE-SHAPED GOES IN THIS ENVIRONMENT, and that is the fix rather
// than an omission. `claude --bg` is a client: if no daemon is running it
// STARTS one that inherits this environment and keeps it for its lifetime, so
// exporting a role's token here bound the DAEMON, which then handed it to every
// session it spawned afterwards whatever role was asked for. Measured
// 2026-09-12, a reviewer for one repo came up holding another repo's account and
// 404'd on the pull request it was sent to read.
//
// The Python popped exactly GH_TOKEN and GITHUB_TOKEN. That is a denylist of
// two spellings, and gh reads more than two: GH_ENTERPRISE_TOKEN and
// GITHUB_ENTERPRISE_TOKEN among them. A pattern covers the ones that exist now
// and the ones added later, and over-stripping here costs nothing because the
// binding travels in --settings.
func scrubForgeTokens(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if ok && isForgeToken(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func isForgeToken(name string) bool {
	if !strings.HasPrefix(name, "GH_") && !strings.HasPrefix(name, "GITHUB_") {
		return false
	}
	return strings.Contains(name, "TOKEN")
}

// workdir decides where the session is rooted.
//
// The worktree PATH and branch are decided here rather than in Execute so that
// --dry-run prints what would actually happen. A dry run that shows a blank cwd
// is a dry run nobody can check the interesting case with.
func workdir(role *config.Role, req Request, w World) (dir, branch string) {
	switch role.Workspace {
	case config.WorkspaceWorktree:
		repo := expandHome(role.Repo.Path, "")
		branch = fmt.Sprintf("agent/%d-%s", w.Stamp, role.Name)
		name := filepath.Base(repo) + "-" + strings.ReplaceAll(branch, "/", "-")
		return filepath.Join(filepath.Dir(repo), name), branch
	case config.WorkspaceRoot:
		return expandHome(w.Root, ""), ""
	default:
		if role.Repo != nil {
			return expandHome(role.Repo.Path, ""), ""
		}
		return w.Cwd, ""
	}
}

func expandHome(p, _ string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}

func loadPerm(dir string, role *config.Role) (map[string]any, string, error) {
	name := role.Perm
	if name == "" {
		name = role.Name
	}
	f := filepath.Join(dir, name+".json")
	b, err := os.ReadFile(f)
	if err != nil {
		if os.IsNotExist(err) {
			// Still not fatal, and for the original reason: refusing over a
			// missing file would make the keepalive fail closed on a fresh
			// machine, which is worse than starting with no rules.
			//
			// It is no longer SILENT, which is the part that was wrong. A role
			// names a permission profile in order to be constrained by it, and
			// a session that starts with `{}` has no deny list and no allow
			// list while looking exactly like one that does. Whoever spawned it
			// is the only one who can notice, so tell them.
			return map[string]any{}, fmt.Sprintf(
				"permission profile %s not found; session starts with NO permission rules "+
					"(deny list absent). Deploy it, or spawn again once it is on the machine.", f), nil
		}
		return nil, "", fmt.Errorf("read %s: %w", f, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, "", fmt.Errorf("%s is not valid JSON: %w", f, err)
	}
	return m, "", nil
}

func knownTags(entries []registry.Entry) map[string]bool {
	out := map[string]bool{}
	for _, e := range entries {
		if e.Tag != "" {
			out[e.Tag] = true
		}
	}
	return out
}

// checkCap refuses when the role is already at its limit.
//
// The cap counts LIVE sessions joined to the registry, and the scope decides
// the population. Refusing on an unreadable listing rather than assuming zero
// is handled by the caller, which cannot even construct a World without one.
func checkCap(role *config.Role, req Request, w World) error {
	live := liveForRole(role, w)
	if role.CapScope == config.ScopePR {
		var keep []registry.Entry
		for _, e := range live {
			if e.PR == req.PR {
				keep = append(keep, e)
			}
		}
		live = keep
	}
	if len(live) < role.Cap {
		return nil
	}
	held := make([]string, len(live))
	for i, e := range live {
		held[i] = fmt.Sprintf("%s [%s]", e.Name, e.BgID)
	}
	sort.Strings(held)
	where := "on this machine"
	if role.Repo != nil {
		where = fmt.Sprintf("per %s in %s", role.CapScope, role.Repo.Name)
	}
	return fmt.Errorf("cap reached: %d/%d %s session(s) %s (%s)",
		len(live), role.Cap, role.Name, where, strings.Join(held, ", "))
}

// liveForRole is the registry entries for this role that a live session still
// backs. A machine-scoped role has no registry, so it is counted by name.
func liveForRole(role *config.Role, w World) []registry.Entry {
	byRef := map[string]bool{}
	for _, s := range w.Live {
		byRef[s.Ref()] = true
	}
	if role.MachineScoped() {
		var out []registry.Entry
		for _, s := range w.Live {
			if s.Name == role.Name {
				out = append(out, registry.Entry{Name: s.Name, BgID: s.Ref()})
			}
		}
		return out
	}
	byName := map[string]bool{}
	for _, s := range w.Live {
		byName[s.Name] = true
	}
	var out []registry.Entry
	for _, e := range w.Registry {
		if e.Role != role.Name {
			continue
		}
		// Either key, as the Python did. An entry whose bg_id never got
		// recorded is still a live session, and joining on the id alone counts
		// it as zero forever: the cap relaxes and nothing ever notices.
		if (e.BgID != "" && byRef[e.BgID]) || (e.Name != "" && byName[e.Name]) {
			out = append(out, e)
		}
	}
	return out
}
