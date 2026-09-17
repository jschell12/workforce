package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"text/tabwriter"

	"github.com/jschell12/workforce/internal/config"
	"github.com/spf13/cobra"
)

// `wf roles` is new. Answering "what can I spawn here" previously meant reading
// roles.toml, and the most common failure it produces -- asking for a role or a
// repo that is not configured -- is only discovered by trying to spawn one.
func newRolesCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "roles [repo]",
		Short: "What can be spawned, and with which identity",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := env.Config()
			if err != nil {
				return err
			}
			rows := collect(cfg)
			if len(args) > 0 {
				if _, ok := cfg.Repos[args[0]]; !ok {
					return fmt.Errorf("unknown repo %q", args[0])
				}
				var keep []row
				for _, r := range rows {
					if r.Repo == args[0] {
						keep = append(keep, r)
					}
				}
				rows = keep
			}
			if env.JSON {
				enc := json.NewEncoder(env.Out)
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			w := tabwriter.NewWriter(env.Out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "REPO\tROLE\tPERSONA\tTOKEN\tCAP\tWORKSPACE")
			for _, r := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d per %s\t%s\n",
					dash(r.Repo), r.Role, dash(r.Persona), r.Token, r.Cap, r.CapScope, r.Workspace)
			}
			return w.Flush()
		},
	}
}

type row struct {
	Repo      string `json:"repo,omitempty"`
	Role      string `json:"role"`
	Persona   string `json:"persona,omitempty"`
	Token     string `json:"token"`
	Cap       int    `json:"cap"`
	CapScope  string `json:"cap_scope"`
	Workspace string `json:"workspace"`
}

func collect(c *config.Config) []row {
	var out []row
	add := func(repo string, r *config.Role) {
		out = append(out, row{
			Repo: repo, Role: r.Name, Persona: r.Persona, Token: r.Token.String(),
			Cap: r.Cap, CapScope: string(r.CapScope), Workspace: string(r.Workspace),
		})
	}
	for _, r := range c.Roles {
		add("", r)
	}
	for name, repo := range c.Repos {
		for _, r := range repo.Roles {
			add(name, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Role < out[j].Role
	})
	return out
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
