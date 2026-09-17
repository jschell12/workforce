package cli

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jschell12/workforce/internal/registry"
	"github.com/jschell12/workforce/internal/session"
	"github.com/jschell12/workforce/internal/worktree"
	"github.com/spf13/cobra"
)

// found is a session resolved to its registry entry, if it has one.
type found struct {
	Row      session.Session
	Entry    registry.Entry
	Store    registry.Store
	RepoPath string
	HasEntry bool
}

// resolveTarget finds one session by ref, name, or an unambiguous prefix.
//
// People say names and read refs: a name is not unique over time, a ref is not
// what anyone remembers, so both are accepted. An ambiguous match is refused
// rather than guessed, because stopping the wrong session is not recoverable by
// the person who typed the wrong thing.
//
// It searches the --all listing on purpose. Stopping something already finished
// should say so, not report that no such session exists.
func resolveTarget(env *Env, target string) (found, error) {
	cfg, err := env.Config()
	if err != nil {
		return found{}, err
	}
	all, err := session.Client{Bin: env.Paths.Claude}.ListAll()
	if err != nil {
		return found{}, err
	}
	row, err := session.Find(all, target)
	if err != nil {
		return found{}, fmt.Errorf("%w. `wf sessions --all` lists them", err)
	}
	f := found{Row: row}
	for _, repo := range cfg.Repos {
		path := expand(repo.Path)
		st, err := registry.Open(path, registry.GitCommonDir)
		if err != nil {
			continue
		}
		entries, err := st.Read()
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.BgID == row.Ref() {
				f.Entry, f.Store, f.RepoPath, f.HasEntry = e, st, path, true
			}
		}
	}
	return f, nil
}

func newStopCmd(env *Env) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "stop <session>",
		Short: "Stop a session, keeping its conversation",
		Long: `Stop a session, keeping its conversation and its registry entry.

claude stop keeps the conversation so --resume works, so this does too. The
entry stays and is marked stopped, because the session may come back and because
reconcile reaps an entry on its own once nothing backs it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			f, err := resolveTarget(env, args[0])
			if err != nil {
				return err
			}
			ref, name := f.Row.Ref(), dash(f.Row.Name)
			if dryRun {
				_, err := fmt.Fprintf(env.Out, "would stop %s [%s]\n", name, ref)
				return err
			}
			if out, err := exec.Command(env.Paths.Claude, "stop", ref).CombinedOutput(); err != nil {
				return fmt.Errorf("claude stop failed: %s", clip(strings.TrimSpace(string(out)), 200))
			}
			if f.HasEntry {
				e := f.Entry
				e.Status = "stopped"
				e.HeartbeatAt = time.Now().Unix()
				if _, err := f.Store.Write(e); err != nil {
					fmt.Fprintf(env.Err, "wf: stopped, but the registry entry was not updated: %v\n", err)
				}
			}
			suffix := ""
			if !f.HasEntry {
				suffix = "  (no registry entry; nothing else to do)"
			}
			_, err = fmt.Fprintf(env.Out, "stopped %s [%s]%s\n", name, ref, suffix)
			return err
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen and change nothing")
	return cmd
}

func newRmCmd(env *Env) *cobra.Command {
	var dryRun, force bool
	cmd := &cobra.Command{
		Use:   "rm <session>",
		Short: "Stop and delete a session, drop its entry, free its worktree",
		Long: `Stop and delete a session, drop its registry entry, and free its worktree.

The escape hatch for when reconcile's own conditions do not apply, such as a
reviewer on a pull request that is still open with no verdict. reconcile reaps
an entry once nothing live backs it, so a cap is not locked by a dead session;
this is for one you want gone now.

A worktree is removed only when nothing would be lost: a clean tree AND nothing
unpushed. Anything else is kept and the reason is printed. --force overrides,
and says what it overrode.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			f, err := resolveTarget(env, args[0])
			if err != nil {
				return err
			}
			ref, name := f.Row.Ref(), dash(f.Row.Name)

			wt := ""
			if f.HasEntry && f.Entry.Worktree != "" {
				wt = f.Entry.Worktree
				// Never remove the repository itself. A reviewer runs in the
				// main checkout, and its "worktree" is that checkout.
				if a, b := filepath.Clean(wt), filepath.Clean(f.RepoPath); a == b {
					wt = ""
				}
			}

			plan := []string{fmt.Sprintf("stop+rm %s [%s]", name, ref)}
			if f.HasEntry {
				plan = append(plan, "drop its registry entry")
			}
			var verdict worktree.Verdict
			if wt != "" {
				verdict = worktree.SafeToRemove(wt, worktree.Git)
				switch {
				case verdict.Safe:
					plan = append(plan, "remove worktree "+wt)
				case force:
					plan = append(plan, fmt.Sprintf("remove worktree %s (FORCED over: %s)", wt, verdict.Why))
				default:
					plan = append(plan, fmt.Sprintf("KEEP worktree %s (%s)", wt, verdict.Why))
				}
			}
			if dryRun {
				_, err := fmt.Fprintln(env.Out, "would "+strings.Join(plan, "; would "))
				return err
			}

			_ = exec.Command(env.Paths.Claude, "stop", ref).Run()
			if out, err := exec.Command(env.Paths.Claude, "rm", ref).CombinedOutput(); err != nil {
				return fmt.Errorf("claude rm failed: %s", clip(strings.TrimSpace(string(out)), 200))
			}
			if f.HasEntry {
				if err := f.Store.Remove(f.Entry); err != nil {
					fmt.Fprintf(env.Err, "wf: %v\n", err)
				}
			}
			if wt != "" {
				// Re-checked rather than reused: the plan was computed before
				// the session was stopped, and a session writes files.
				verdict = worktree.SafeToRemove(wt, worktree.Git)
				if verdict.Safe || force {
					a := []string{"-C", f.RepoPath, "worktree", "remove", wt}
					if force {
						a = append(a, "--force")
					}
					if out, err := exec.Command("git", a...).CombinedOutput(); err != nil {
						fmt.Fprintf(env.Out, "  worktree left in place: %s\n",
							clip(strings.TrimSpace(string(out)), 160))
					}
				} else {
					fmt.Fprintf(env.Out, "  worktree kept: %s -- %s\n", verdict.Why, wt)
				}
			}
			_, err = fmt.Fprintf(env.Out, "removed %s [%s]\n", name, ref)
			return err
		},
	}
	f := cmd.Flags()
	f.BoolVar(&dryRun, "dry-run", false, "print what would happen and change nothing")
	f.BoolVar(&force, "force", false, "remove the worktree even when work would be lost")
	return cmd
}
