// Package cli wires the command surface.
package cli

import (
	"fmt"
	"io"

	"github.com/jschell12/workforce/internal/config"
	"github.com/jschell12/workforce/internal/paths"
	"github.com/spf13/cobra"
)

// Env is everything a command needs that is not a flag. Passed explicitly so
// tests construct a command against a scratch tree instead of the real home
// directory, and so no command reaches for a global.
type Env struct {
	Paths  paths.Set
	Out    io.Writer
	Err    io.Writer
	JSON   bool
	loaded *config.Config
}

// Config loads roles.toml once per invocation.
func (e *Env) Config() (*config.Config, error) {
	if e.loaded != nil {
		return e.loaded, nil
	}
	c, err := config.Load(e.Paths.Config)
	if err != nil {
		return nil, fmt.Errorf("%w\n\nwf reads %s; set WORKFORCE_CONFIG to point elsewhere", err, e.Paths.Config)
	}
	e.loaded = c
	return c, nil
}

// New builds the root command.
func New(env *Env, version string) *cobra.Command {
	root := &cobra.Command{
		Use:   "wf",
		Short: "Start and supervise Claude Code sessions with a role's identity",
		Long: `wf starts the Claude Code sessions that do work on this machine.

It exists because three things cannot be left to a session to decide for
itself: which credential a role holds, how many of a role may run, and what a
reviewer is asked to look at.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&env.Paths.Config, "config", env.Paths.Config, "path to roles.toml")
	root.PersistentFlags().BoolVar(&env.JSON, "json", false, "machine-readable output")
	root.SetOut(env.Out)
	root.SetErr(env.Err)

	root.AddCommand(newVersionCmd(env, version), newRolesCmd(env), newSessionsCmd(env), newSpawnCmd(env), newReconcileCmd(env),
		newStopCmd(env), newRmCmd(env), newDoctorCmd(env), newTailCmd(env), newWatchCmd(env))
	return root
}

func newVersionCmd(env *Env, version string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			_, err := fmt.Fprintln(env.Out, version)
			return err
		},
	}
}
