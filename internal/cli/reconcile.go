package cli

import (
	"fmt"
	"os/exec"

	"github.com/jschell12/workforce/internal/forge"
	"github.com/jschell12/workforce/internal/reconcile"
	"github.com/jschell12/workforce/internal/registry"
	"github.com/jschell12/workforce/internal/session"
	"github.com/spf13/cobra"
)

// claudeCtl stops and removes sessions.
type claudeCtl struct{ bin string }

func (c claudeCtl) Stop(ref string) error   { return exec.Command(c.bin, "stop", ref).Run() }
func (c claudeCtl) Remove(ref string) error { return exec.Command(c.bin, "rm", ref).Run() }

func newReconcileCmd(env *Env) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Retire finished sessions and drop registry entries nothing backs",
		Long: `Retire finished sessions and drop registry entries nothing backs.

The only destructive command, and a launchd job runs it on a timer, so a real
sweep can happen shortly whether or not you run one. --dry-run reports what the
NEXT run would do; it does not hold the timer while you read the output.

It touches only entries wf wrote. The coordination directory is shared with
every agent following the multi-agent protocol, and deleting one of those
deregisters a live agent mid-task.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := env.Config()
			if err != nil {
				return err
			}
			// An unreadable listing is not an empty one: every entry would look
			// unbacked and the sweep would reap the entire fleet.
			live, err := session.Client{Bin: env.Paths.Claude}.List()
			if err != nil {
				return fmt.Errorf("%w; skipping (an unreadable list is not an empty one)", err)
			}
			repos := reconcile.Repos(cfg, func(p string) (reconcile.Store, error) {
				st, err := registry.Open(expand(p), registry.GitCommonDir)
				if err != nil {
					return nil, err
				}
				return storeAdapter{st}, nil
			})
			// --all is read for the clear pass only, and never for liveness.
			// A finished row there carries a state word that reads alive.
			all, err := session.Client{Bin: env.Paths.Claude}.ListAll()
			if err != nil {
				fmt.Fprintf(env.Err, "wf: %v; skipping the clear pass\n", err)
			}
			res, err := reconcile.Run(repos, live, all,
				forge.Client{Bin: env.Paths.GH}, claudeCtl{env.Paths.Claude},
				reconcile.Options{DryRun: dryRun, Out: env.Out,
					Absence: reconcile.FileAbsence{Path: env.Paths.Absence}})
			if err != nil {
				return err
			}
			line := res.String()
			if dryRun {
				// Say so on the summary line too. The per-item lines carry
				// "would", and a summary without it reads like a real sweep
				// when it scrolls past on its own.
				line += " (dry run; changed nothing)"
			}
			_, err = fmt.Fprintln(env.Out, line)
			return err
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what the next sweep would do, and change nothing")
	return cmd
}

type storeAdapter struct{ s registry.Store }

func (a storeAdapter) Read() ([]registry.Entry, error) { return a.s.Read() }
func (a storeAdapter) Remove(e registry.Entry) error   { return a.s.Remove(e) }
