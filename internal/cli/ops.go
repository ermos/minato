package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ermos/minato/internal/daemon"
	"github.com/ermos/minato/internal/render"
	"github.com/ermos/minato/internal/secrets"
	"github.com/ermos/minato/internal/stack"
)

func statusCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Aliases: []string{"ls"},
		Short:   "Show the synced commit and the state of every stack",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client()
			if err != nil {
				return err
			}
			s, err := c.Status()
			if err != nil {
				return err
			}
			fmt.Printf("commit     %s\n", short(s.Rev))
			if !s.LastSync.IsZero() {
				fmt.Printf("last sync  %s (%s ago)\n", s.LastSync.Format(time.RFC3339), ago(s.LastSync))
			}
			if s.LastSyncError != "" {
				fmt.Printf("sync error %s\n", s.LastSyncError)
			}
			fmt.Println()
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 3, ' ', 0)
			fmt.Fprintln(w, "STACK\tMODE\tSTATUS\tCOMMIT\tDEPLOYED\tAUTO")
			for _, st := range s.Stacks {
				deployed := "-"
				if !st.DeployedAt.IsZero() {
					deployed = ago(st.DeployedAt) + " ago"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%v\n", st.Name, st.Mode, st.Status, short(st.Rev), deployed, st.AutoDeploy)
			}
			w.Flush()
			for _, st := range s.Stacks {
				if st.Error != "" {
					fmt.Printf("\n%s: %s\n", st.Name, st.Error)
				}
			}
			return nil
		},
	}
}

func syncCmd(g *globals) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Pull the git repository and reconcile every stack now",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client()
			if err != nil {
				return err
			}
			r, err := c.Sync(force)
			if err != nil {
				return err
			}
			fmt.Printf("synced %s\n", short(r.Rev))
			printList("deployed", r.Deployed)
			printList("removed", r.Removed)
			printList("failed", r.Failed)
			for _, e := range r.Errors {
				fmt.Println("  !", e)
			}
			if len(r.Failed) > 0 {
				return errors.New("some stacks failed, see `minato status`")
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "redeploy every stack even without change")
	return cmd
}

func actionCmd(g *globals, action, short string) *cobra.Command {
	return &cobra.Command{
		Use:   action + " STACK [SERVICE...]",
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client()
			if err != nil {
				return err
			}
			r, err := c.Action(args[0], action, args[1:])
			if r != nil && strings.TrimSpace(r.Output) != "" {
				fmt.Println(strings.TrimSpace(r.Output))
			}
			if err != nil {
				return err
			}
			if action == "down" {
				fmt.Println("note: set `disabled: true` in minato.yml to keep it down, the next sync redeploys it otherwise")
			}
			return nil
		},
	}
}

// stackState fetches the deployed project and mode of a stack.
func stackState(g *globals, name string) (*daemon.StackState, error) {
	c, err := g.client()
	if err != nil {
		return nil, err
	}
	s, err := c.Status()
	if err != nil {
		return nil, err
	}
	for _, st := range s.Stacks {
		if st.Name == name {
			return &st, nil
		}
	}
	return nil, fmt.Errorf("unknown stack %q", name)
}

func logsCmd(g *globals) *cobra.Command {
	var follow bool
	var tail int
	cmd := &cobra.Command{
		Use:   "logs STACK [SERVICE]",
		Short: "Show the logs of a stack or a service",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client()
			if err != nil {
				return err
			}
			service := ""
			if len(args) == 2 {
				service = args[1]
			}
			return c.Logs(args[0], service, tail, follow, os.Stdout)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow log output")
	cmd.Flags().IntVarP(&tail, "tail", "n", 200, "number of lines")
	return cmd
}

func psCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "ps STACK",
		Short: "List the containers or swarm tasks of a stack",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client()
			if err != nil {
				return err
			}
			out, err := c.Ps(args[0])
			fmt.Print(out)
			return err
		},
	}
}

func composeCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "compose STACK -- ARGS...",
		Short: "Run any docker compose command on a deployed stack, with its environment (on the server)",
		Example: `  sudo minato compose blog -- exec db psql -U app
  sudo minato compose blog -- config`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := g.config()
			if err != nil {
				return err
			}
			st, err := stackState(g, args[0])
			if err != nil {
				return err
			}
			if st.Mode == stack.ModeSwarm {
				return errors.New("compose passthrough is only available for compose stacks")
			}
			repoSpec, err := loadRepoSpecFor(cfg.Repo.Dir)
			if err != nil {
				return err
			}
			s, err := stack.Load(filepath.Join(cfg.Repo.Dir, repoSpec.StacksDir, args[0]))
			if err != nil {
				return err
			}
			ids, _ := secrets.LoadIdentities(secrets.DefaultIdentityFiles(cfg.AgeKey)...)
			outDir := filepath.Join(cfg.StacksDir(), s.Name)
			b, err := render.Build(s, render.Options{
				OutDir:        outDir,
				Identities:    ids,
				HostEnv:       environ(),
				SharedSecrets: repoSpec.SharedSecretsPath(cfg.Repo.Dir),
			})
			if err != nil {
				return err
			}
			full := append([]string{"compose", "-p", s.Project(), "-f", filepath.Join(outDir, b.ComposePath)}, args[1:]...)
			c := exec.Command(cfg.Docker.Bin, full...)
			c.Dir = outDir
			c.Env = os.Environ()
			for k, v := range b.Env {
				c.Env = append(c.Env, k+"="+v)
			}
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			return c.Run()
		},
	}
}

func hooksCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "hooks",
		Short: "List the webhook endpoints",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client()
			if err != nil {
				return err
			}
			hooks, err := c.Hooks()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 3, ' ', 0)
			fmt.Fprintln(w, "STACK\tHOOK\tACTION\tSERVICES\tENDPOINT")
			for _, h := range hooks {
				endpoint := h.URL
				if endpoint == "" {
					endpoint = "POST " + h.Path
				}
				svcs := strings.Join(h.Services, ",")
				if svcs == "" {
					svcs = "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", h.Stack, h.Name, h.Action, svcs, endpoint)
			}
			return w.Flush()
		},
	}
}

func printList(label string, items []string) {
	if len(items) > 0 {
		fmt.Printf("  %-9s %s\n", label, strings.Join(items, ", "))
	}
}

func short(rev string) string {
	if len(rev) > 8 {
		return rev[:8]
	}
	if rev == "" {
		return "-"
	}
	return rev
}

func ago(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return d.String()
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func environ() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}
