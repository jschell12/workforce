package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/jschell12/workforce/internal/registry"
	"github.com/jschell12/workforce/internal/session"
	"github.com/spf13/cobra"
)

// `sessions` was `ls`, which stays as an alias: it is worth keeping under the
// fingers and not worth being the documented name.
func newSessionsCmd(env *Env) *cobra.Command {
	var repoFilter string
	var includeAll bool
	cmd := &cobra.Command{
		Use:     "sessions",
		Aliases: []string{"ls"},
		Short:   "Live sessions, joined to what wf registered",
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := env.Config()
			if err != nil {
				return err
			}
			client := session.Client{Bin: env.Paths.Claude}

			// The plain listing is the liveness test. --all is a separate,
			// clearly-labelled view and never feeds Live().
			plain, err := client.List()
			if err != nil {
				return err
			}
			rows := plain.Live()
			ended := map[string]bool{}
			if includeAll {
				all, err := client.ListAll()
				if err != nil {
					return err
				}
				inPlain := map[string]bool{}
				for _, s := range plain {
					inPlain[s.Ref()] = true
				}
				// Absence from the PLAIN listing is what makes a row finished.
				// Its state word does not say so: a finished session carries
				// one that reads alive, which is the trap this whole package is
				// shaped around. The Python relabelled two state words to
				// "idle(done)" and "idle(blkd)" without reading the status
				// field; showing the real word plus an explicit (ended) marker
				// says more and asserts less.
				rows = all
				for _, s := range all {
					if !inPlain[s.Ref()] {
						ended[s.Ref()] = true
					}
				}
			}

			byRef := map[string]registry.Entry{}
			for name, repo := range cfg.Repos {
				if repoFilter != "" && repoFilter != name {
					continue
				}
				st, err := registry.Open(expand(repo.Path), registry.GitCommonDir)
				if err != nil {
					// A repo that has moved is worth saying so about, but it
					// must not hide the sessions of every other repo.
					fmt.Fprintf(env.Err, "wf: %s: %v\n", name, err)
					continue
				}
				entries, err := st.Read()
				if err != nil {
					fmt.Fprintf(env.Err, "wf: %s: %v\n", name, err)
					continue
				}
				for _, e := range entries {
					if e.BgID != "" {
						byRef[e.BgID] = e
					}
					if e.AgentID != "" {
						byRef[e.AgentID] = e
					}
				}
			}

			out := make([]sessionRow, 0, len(rows))
			for _, s := range rows {
				e := byRef[s.Ref()]
				state := s.StateWord()
				if ended[s.Ref()] {
					state += " (ended)"
				}
				out = append(out, sessionRow{
					Name: s.Name, Ref: s.Ref(), Kind: s.Kind, State: state,
					Role: e.Role, PR: e.PR, For: firstNonEmpty(e.ForTag, e.Tag), Cwd: s.Cwd,
				})
			}
			if repoFilter != "" {
				kept := out[:0]
				for _, r := range out {
					if byRef[r.Ref].Repo == repoFilter {
						kept = append(kept, r)
					}
				}
				out = kept
			}
			if env.JSON {
				enc := json.NewEncoder(env.Out)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			if len(out) == 0 {
				_, err := fmt.Fprintln(env.Out, "no sessions")
				return err
			}
			w := tabwriter.NewWriter(env.Out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tREF\tKIND\tSTATE\tROLE\tPR\tFOR\tCWD")
			for _, r := range out {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					clip(dash(r.Name), 32), r.Ref, dash(r.Kind), r.State,
					dash(r.Role), dash(r.PR), dash(r.For), dash(r.Cwd))
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&repoFilter, "repo", "", "only sessions registered for this repo")
	cmd.Flags().BoolVar(&includeAll, "all", false, "include finished sessions (never a liveness signal)")
	return cmd
}

type sessionRow struct {
	Name  string `json:"name"`
	Ref   string `json:"ref"`
	Kind  string `json:"kind,omitempty"`
	State string `json:"state"`
	Role  string `json:"role,omitempty"`
	PR    string `json:"pr,omitempty"`
	For   string `json:"for,omitempty"`
	Cwd   string `json:"cwd,omitempty"`
}

// expand resolves a leading ~, which roles.toml uses throughout.
func expand(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// clip bounds a cell so one pathological value cannot widen the table.
//
// A session's name is whatever started it, and a background session started
// from a prompt can carry that whole prompt as its name. tabwriter sizes every
// column to its widest cell, so one such row pushed CWD off the right of the
// screen for every other row. JSON output is never clipped: truncating data a
// program is going to parse is a different and worse bug.
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "\u2026"
}
