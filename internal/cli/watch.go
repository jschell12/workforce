package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/jschell12/workforce/internal/terminal"
	"github.com/spf13/cobra"
)

func newWatchCmd(env *Env) *cobra.Command {
	var dryRun bool
	var term string
	cmd := &cobra.Command{
		Use:   "watch <session>",
		Short: "Open a terminal tab attached to a session",
		Long: fmt.Sprintf(`Open a terminal tab attached to a session.

The only command here that touches a terminal. A session that needs no eyes
wants no tab; this is for watching one you have decided to watch.

Defaults to iTerm2. --terminal picks another: %s. The tab is opened with
AppleScript rather than the terminal's own CLI, because those do not agree and
several do not work (ghostty +new-window answers "not supported on this
platform" on macOS).`, strings.Join(terminal.Known(), ", ")),
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			f, err := resolveTarget(env, args[0])
			if err != nil {
				return err
			}
			ref := f.Row.Ref()
			cwd := f.Row.Cwd
			if cwd == "" {
				if cwd, err = os.UserHomeDir(); err != nil {
					cwd = "/"
				}
			}
			attach := fmt.Sprintf("claude attach %s", ref)

			// Resolved before the dry-run branch so an unknown terminal is
			// refused either way. A dry run that accepts what a real run
			// rejects is worse than no dry run.
			app, _, err := terminal.Script(term, attach, cwd)
			if err != nil {
				return err
			}
			if dryRun {
				_, err := fmt.Fprintf(env.Out,
					"would open %s %s tab in %s running: %s\n", article(app), app, cwd, attach)
				return err
			}
			app, out, err := terminal.Open(term, attach, cwd)
			if err != nil {
				// Always name the manual route. This usually fails because the
				// app is not running or automation is not permitted, and
				// neither is a reason to leave someone without a way in.
				return fmt.Errorf("could not open %s %s tab: %s\n  attach it yourself with: %s",
					article(app), app, clip(strings.TrimSpace(string(out)), 200), attach)
			}
			_, err = fmt.Fprintf(env.Out, "watching %s [%s] in a new %s tab\n", dash(f.Row.Name), ref, app)
			return err
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen and change nothing")
	cmd.Flags().StringVarP(&term, "terminal", "t", terminal.Default,
		"terminal app to open: "+strings.Join(terminal.Known(), ", "))
	return cmd
}

// article picks a or an. "a iTerm tab" is the kind of thing that makes a tool
// read as unfinished.
func article(app string) string {
	if app == "" {
		return "a"
	}
	if strings.ContainsRune("AEIOUaeiou", rune(app[0])) {
		return "an"
	}
	return "a"
}
