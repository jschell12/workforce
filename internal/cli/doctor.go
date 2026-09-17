package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jschell12/workforce/internal/config"
	"github.com/jschell12/workforce/internal/registry"
	"github.com/jschell12/workforce/internal/session"
	"github.com/spf13/cobra"
)

// doctor answers "why did that spawn fail for a reason that is not obviously
// mine". Every check is something a spawn depends on and none of which says so
// when it is missing: a persona file that does not exist, a permission profile
// that was never deployed, a credential key that has expired.
//
// It exits non-zero when anything failed, so a launchd job or a CI step can use
// it as a gate rather than something a person has to read.
func newDoctorCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check config, personas, permission profiles and credentials",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := &doc{env: env}
			fmt.Fprintln(env.Out, "workforce doctor")

			if _, err := os.Stat(env.Paths.Config); err != nil {
				d.check("config at "+env.Paths.Config, false, "not found")
				return d.finish(cmd)
			}
			d.check("config at "+env.Paths.Config, true, "")
			cfg, err := env.Config()
			if err != nil {
				d.check("config parses", false, err.Error())
				return d.finish(cmd)
			}

			d.check(env.Paths.Claude+" on PATH", exec.Command(env.Paths.Claude, "--version").Run() == nil, "")
			live, listErr := session.Client{Bin: env.Paths.Claude}.List()
			d.check("agent listing readable", listErr == nil, errText(listErr))

			for name, role := range cfg.Roles {
				d.role(cfg, "", name, role)
				if listErr == nil {
					up := false
					for _, s := range live.Live() {
						if s.Name == name {
							up = true
						}
					}
					// Not a failure anyone needs to act on immediately: the
					// keepalive restarts it on its own timer. Say which.
					d.check("machine role "+name+" session is up", up,
						"launchd should restart it within 2 minutes")
				}
			}

			for repoName, repo := range cfg.Repos {
				path := expand(repo.Path)
				ok := isDir(path)
				d.check(fmt.Sprintf("repo %s at %s", repoName, path), ok, "")
				if !ok {
					continue
				}
				common, err := registry.GitCommonDir(path)
				writable := err == nil && unixVar.Access(common) == nil
				d.check(fmt.Sprintf("repo %s registry writable", repoName), writable, errText(err))
				for roleName, role := range repo.Roles {
					d.role(cfg, repoName, roleName, role)
				}
			}
			return d.finish(cmd)
		},
	}
}

type doc struct {
	env      *Env
	problems int
}

func (d *doc) check(label string, ok bool, detail string) {
	word := "ok"
	if !ok {
		word = "FAIL"
		d.problems++
	}
	if detail != "" && !ok {
		fmt.Fprintf(d.env.Out, "  [%s] %s -- %s\n", word, label, detail)
		return
	}
	fmt.Fprintf(d.env.Out, "  [%s] %s\n", word, label)
}

// role checks everything one role needs before it can be spawned.
func (d *doc) role(cfg *config.Config, repoName, roleName string, role *config.Role) {
	prefix := roleName
	if repoName != "" {
		prefix = repoName + "/" + roleName
	}

	// Personas: every one the role may be asked for, not just its default.
	// --persona picks from personas_allowed, so a missing file there fails only
	// for the caller who asks for it, which is the worst time to find out.
	for _, p := range personasOf(role) {
		found := false
		for _, dir := range d.personaDirs(cfg, repoName) {
			if _, err := os.Stat(filepath.Join(dir, p+".md")); err == nil {
				found = true
				break
			}
		}
		d.check(fmt.Sprintf("%s persona %s", prefix, p), found,
			"no such agent file in the repo or ~/.claude/agents")
	}

	perm := role.Perm
	if perm == "" {
		perm = roleName
	}
	permFile := filepath.Join(d.env.Paths.PermDir, perm+".json")
	_, err := os.Stat(permFile)
	d.check(fmt.Sprintf("%s permission profile", prefix), err == nil, permFile)

	if role.Workspace == config.WorkspaceRoot {
		root := expand(cfg.WorkspaceRoot)
		d.check(fmt.Sprintf("%s workspace %s", prefix, root), isDir(root),
			"a directory, not necessarily a repo")
	}

	// The credential is read to prove it resolves, and the VALUE is discarded
	// on the spot. A key that has expired looks exactly like a working one
	// until a session tries to use it and signs as nobody.
	if role.Token.Kind == config.TokenKey {
		out, err := exec.Command(d.env.Paths.Scredmgr, "get", role.Token.Key).Output()
		d.check(fmt.Sprintf("%s token %s", prefix, role.Token.Key),
			err == nil && len(out) > 0, "could not be read from the credential store")
	}
}

func (d *doc) personaDirs(_ *config.Config, repoName string) []string {
	home, _ := os.UserHomeDir()
	dirs := []string{filepath.Join(home, ".claude", "agents")}
	if repoName != "" {
		if cfg, err := d.env.Config(); err == nil {
			if r, ok := cfg.Repos[repoName]; ok {
				dirs = append(dirs, filepath.Join(expand(r.Path), ".claude", "agents"))
			}
		}
	}
	return dirs
}

func personasOf(r *config.Role) []string {
	if len(r.PersonasAllowed) > 0 {
		return r.PersonasAllowed
	}
	if r.Persona == "" {
		return nil // a plain session has no agent file to find
	}
	return []string{r.Persona}
}

func (d *doc) finish(cmd *cobra.Command) error {
	fmt.Fprintf(d.env.Out, "\n%d problem(s)\n", d.problems)
	if d.problems > 0 {
		// Silence usage: a failing health check is not a usage error, and
		// printing the help text after it buries the findings.
		cmd.SilenceUsage = true
		return fmt.Errorf("%d problem(s)", d.problems)
	}
	return nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
