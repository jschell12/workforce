package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jschell12/workforce/internal/forge"
	"github.com/jschell12/workforce/internal/reconcile"
	"github.com/jschell12/workforce/internal/registry"
	"github.com/jschell12/workforce/internal/session"
	"github.com/jschell12/workforce/internal/spawn"
	"github.com/spf13/cobra"
)

func newSpawnCmd(env *Env) *cobra.Command {
	var req spawn.Request
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "spawn <role> [repo] [brief]",
		Short: "Start one session for a role, with that role's identity",
		Long: `Start one session for a role.

A reviewer's brief is composed here and a caller-supplied one is refused, so an
author who summons the reviewer for its own pull request cannot also frame what
that reviewer looks at.`,
		Args: cobra.RangeArgs(1, 3),
		RunE: func(_ *cobra.Command, args []string) error {
			req.Role = args[0]
			if len(args) > 1 {
				req.Repo = args[1]
			}
			if len(args) > 2 {
				if req.Brief != "" {
					return fmt.Errorf("pass the brief once: positionally or with --brief, not both")
				}
				req.Brief = args[2]
			}
			cfg, err := env.Config()
			if err != nil {
				return err
			}
			role, err := cfg.Lookup(req.Role, req.Repo)
			if err != nil {
				// A machine-scoped role named with a repo is a specific
				// mistake with a specific fix, and "unknown role X for Y" sends
				// the reader looking for a config gap that is not there.
				if req.Repo != "" {
					if _, ok := cfg.Roles[req.Role]; ok {
						return fmt.Errorf("%s is machine-scoped and takes no repo: `wf spawn %s`", req.Role, req.Role)
					}
				}
				return err
			}

			// An unreadable listing is not an empty one, so this refuses rather
			// than spawning into an unknown population. It is the reason a cap
			// means anything at all.
			live, err := session.Client{Bin: env.Paths.Claude}.List()
			if err != nil {
				return fmt.Errorf("%w; refusing to spawn (an unreadable list is not an empty one)", err)
			}

			w := spawn.World{
				Paths: env.Paths, Live: live.Live(), Env: os.Environ(),
				CallerID: callerRef(), Stamp: time.Now().Unix(),
				Root: cfg.WorkspaceRoot,
				HeadBranch: func(slug, pr string) (string, error) {
					return forge.Client{Bin: env.Paths.GH}.HeadBranch(slug, pr)
				},
				ReadToken: func(key string) error {
					out, err := exec.Command(env.Paths.Scredmgr, "get", key).Output()
					if err != nil {
						return err
					}
					if len(strings.TrimSpace(string(out))) == 0 {
						return fmt.Errorf("the key resolved to nothing")
					}
					return nil
				},
			}
			if w.Cwd, err = os.Getwd(); err != nil {
				return err
			}
			var repoPath string
			for name, repo := range cfg.Repos {
				st, err := registry.Open(expand(repo.Path), registry.GitCommonDir)
				if err != nil {
					continue // a repo that has moved must not hide the others
				}
				entries, err := st.Read()
				if err != nil {
					continue
				}
				w.AllRegs = append(w.AllRegs, entries...)
				if name == req.Repo {
					w.Registry, repoPath = entries, expand(repo.Path)
				}
			}

			// The head this reviewer answers for, recorded at spawn. Without
			// it reconcile's signed-at-head retirement is dead code and
			// reviewers accumulate until their pull request closes, which is
			// the failure the timer exists to prevent. A forge that cannot be
			// reached costs the route, not the spawn.
			if role.Name == spawn.RoleReviewer && req.PR != "" && role.Repo != nil {
				if head, herr := (forge.Client{Bin: env.Paths.GH}).HeadOID(role.Repo.Slug, req.PR); herr == nil {
					req.Head = head
				} else {
					fmt.Fprintf(env.Err, "wf: could not read the head of #%s (%v); "+
						"this reviewer will retire only when the PR closes\n", req.PR, herr)
				}
			}

			plan, err := spawn.Build(role, req, w)
			if err != nil {
				return err
			}
			// ABOVE the dry-run branch, not inside it. A warning that only
			// prints on a rehearsal is silent for every real spawn, which is
			// the one that matters. Written inside it first.
			//
			// stderr, and before the rest, because a warning printed under a
			// wall of settings is one nobody reads.
			for _, warn := range plan.Warnings {
				fmt.Fprintf(env.Err, "wf: WARNING: %s\n", warn)
			}
			if dryRun {
				fmt.Fprintf(env.Out, "cwd: %s\n", plan.Workdir)
				if plan.Branch != "" {
					fmt.Fprintf(env.Out, "branch: %s (worktree would be created)\n", plan.Branch)
				}
				fmt.Fprintf(env.Out, "settings: %s WF_GH_TOKEN_KEY=%s\n",
					plan.SettingsPath, plan.Settings["env"].(map[string]any)["WF_GH_TOKEN_KEY"])
				// The settings file is PRINTED, not written. It is the thing
				// that binds the credential and pins the session's permissions,
				// so being able to read exactly what would be written is the
				// point of a dry run; writing it during one is a side effect a
				// dry run should not have.
				blob, err := json.MarshalIndent(plan.Settings, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(env.Out, string(blob))
				fmt.Fprintln(env.Out, quote(plan.Argv))
				return nil
			}

			bgID, err := spawn.Execute(plan, repoPath)
			if err != nil {
				return err
			}
			if bgID == "" {
				// Said out loud, because the entry that gets written cannot be
				// matched by id. It is still matched by name, which is why this
				// is a warning and not a failure.
				fmt.Fprintf(env.Err, "wf: %s started but printed no session id; "+
					"registering %s by name only. `wf sessions` will show it.\n",
					env.Paths.Claude, plan.Name)
			}
			if !role.MachineScoped() {
				if err := spawn.Register(plan, bgID, repoPath); err != nil {
					fmt.Fprintf(env.Err, "wf: spawned but not registered: %v\n", err)
				}
			}
			// A role that accompanies its caller records the pairing here,
			// because this is the only moment both refs are known: the caller
			// from the environment, and the new session from what the client
			// just printed. Reconcile cannot work either of them out later.
			//
			// Without a bgID there is nothing to stop later, so recording it
			// would be a row that never clears. The warning above already said
			// the id was missing.
			if role.RetireWithCaller && bgID != "" {
				caller := callerRef()
				if caller == "" {
					fmt.Fprintf(env.Err, "wf: %s accompanies its caller but the caller could not be "+
						"identified; nothing will retire it. Stop it by hand with `wf stop %s`.\n",
						plan.Name, plan.Name)
				} else {
					reconcile.Record(reconcile.FilePairs{Path: env.Paths.Pairs}, plan.Name,
						reconcile.Pair{BgID: bgID, Watching: caller})
				}
			}
			ref := bgID
			if ref == "" {
				ref = "?"
			}
			fmt.Fprintf(env.Out, "%s [%s]\n  cwd    %s\n", plan.Name, ref, plan.Workdir)
			if plan.Branch != "" {
				fmt.Fprintf(env.Out, "  branch %s\n", plan.Branch)
			}
			key := plan.TokenKey
			if key == "" {
				key = "none"
			}
			fmt.Fprintf(env.Out, "  token  %s\n", key)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&req.Brief, "brief", "", "the session's opening message (refused for a reviewer)")
	f.StringVar(&req.PR, "pr", "", "pull request number; required for a reviewer")
	f.StringVar(&req.Persona, "persona", "", "pick from the role's personas_allowed")
	f.StringVar(&req.Name, "name", "", "override the generated session name")
	f.StringVar(&req.ForTag, "for", "", "tag or name of the session this reviewer answers for")
	f.BoolVar(&dryRun, "dry-run", false, "print the decision and exit without starting anything")
	return cmd
}

// callerRef identifies the session running this command without it telling us.
// An identity a model has to remember and pass is one that goes missing.
func callerRef() string {
	id := os.Getenv("CLAUDE_CODE_SESSION_ID")
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func quote(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if strings.ContainsAny(a, " \t\n'\"") {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		} else {
			out[i] = a
		}
	}
	return strings.Join(out, " ")
}
