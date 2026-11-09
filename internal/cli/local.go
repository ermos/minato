package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/spf13/cobra"

	"github.com/ermos/minato/internal/config"
	"github.com/ermos/minato/internal/render"
	"github.com/ermos/minato/internal/secrets"
	"github.com/ermos/minato/internal/stack"
)

func loadRepoSpecFor(dir string) (*config.RepoSpec, error) { return config.LoadRepoSpec(dir) }

func renderCmd(g *globals) *cobra.Command {
	var showSecrets, deployed bool
	var out string
	cmd := &cobra.Command{
		Use:   "render STACK_DIR",
		Short: "Render a stack locally and print the final compose file",
		Long: `Render a stack from a working copy, exactly like the daemon would.
Secrets are replaced by placeholders unless --show-secrets is set (which needs
an age identity). With --deployed, STACK_DIR is a stack name and the compose
file currently rendered by the daemon is printed.`,
		Example: `  minato render stacks/blog
  minato render stacks/blog --out /tmp/blog && docker compose -f /tmp/blog/compose.yml config
  sudo minato render --deployed blog`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if deployed {
				c, err := g.client()
				if err != nil {
					return err
				}
				s, err := c.Compose(args[0])
				if err != nil {
					return err
				}
				fmt.Print(s)
				return nil
			}
			st, err := stack.Load(args[0])
			if err != nil {
				return err
			}
			opts := render.Options{OutDir: out, HostEnv: environ(), Redact: !showSecrets}
			if root, ok := config.FindRepoRoot(st.Dir); ok {
				spec, err := config.LoadRepoSpec(root)
				if err != nil {
					return err
				}
				opts.SharedSecrets = spec.SharedSecretsPath(root)
			}
			if opts.OutDir == "" {
				opts.OutDir = filepath.Join(config.DefaultDataDir, "stacks", st.Name)
			}
			opts.OutDir, _ = filepath.Abs(opts.OutDir)
			if showSecrets {
				ids, err := secrets.LoadIdentities(secrets.DefaultIdentityFiles(config.DefaultAgeKey)...)
				if err != nil {
					return err
				}
				opts.Identities = ids
			}
			b, err := render.Build(st, opts)
			if err != nil {
				return err
			}
			if out != "" {
				if _, err := b.Write(); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "written to %s\n", opts.OutDir)
			}
			fmt.Fprintf(os.Stderr, "# stack %s (%s, project %s)\n", st.Name, st.Spec.Mode, st.Project())
			keys := make([]string, 0, len(b.Env))
			for k := range b.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(os.Stderr, "# env %s=%s\n", k, b.Env[k])
			}
			fmt.Print(string(b.Files[b.ComposePath].Data))
			return nil
		},
	}
	cmd.Flags().BoolVar(&showSecrets, "show-secrets", false, "decrypt secrets instead of using placeholders")
	cmd.Flags().BoolVar(&deployed, "deployed", false, "print the compose file rendered by the daemon")
	cmd.Flags().StringVarP(&out, "out", "o", "", "also write the rendered stack to this folder")
	return cmd
}

func validateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [REPO_DIR]",
		Short: "Validate every stack of a repository (no key needed, CI friendly)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			spec, err := config.LoadRepoSpec(root)
			if err != nil {
				return err
			}
			if len(spec.Secrets.Recipients) > 0 {
				if _, err := secrets.ParseRecipients(spec.Secrets.Recipients); err != nil {
					return fmt.Errorf("%s: %w", config.RepoFileName, err)
				}
			}
			stacks, loadErrs, err := stack.Discover(filepath.Join(root, spec.StacksDir))
			if err != nil {
				return err
			}
			failed := len(loadErrs)
			shared := spec.SharedSecretsPath(root)
			if shared != "" && len(spec.Secrets.Recipients) > 0 {
				if e, err := secrets.ReadFile(shared); err != nil {
					failed++
					fmt.Printf("✘ %s: %v\n", filepath.Base(shared), err)
				} else if !sameSet(e.Meta.Recipients, spec.Secrets.Recipients) {
					fmt.Printf("  ! %s recipients differ from %s, run: minato secrets rotate %s\n",
						filepath.Base(shared), config.RepoFileName, shared)
				}
			}
			for name, e := range loadErrs {
				fmt.Printf("✘ %s: %v\n", name, e)
			}
			for _, st := range stacks {
				_, err := render.Build(st, render.Options{
					OutDir:        "/var/lib/minato/stacks/" + st.Name,
					Redact:        true,
					SharedSecrets: shared,
				})
				if err != nil {
					failed++
					fmt.Printf("✘ %s: %v\n", st.Name, err)
					continue
				}
				fmt.Printf("✔ %s (%s)\n", st.Name, st.Spec.Mode)
				if p := st.SecretsPath(); p != "" && len(spec.Secrets.Recipients) > 0 {
					if e, err := secrets.ReadFile(p); err == nil && !sameSet(e.Meta.Recipients, spec.Secrets.Recipients) {
						fmt.Printf("  ! %s recipients differ from %s, run: minato secrets rotate %s\n",
							filepath.Base(p), config.RepoFileName, p)
					}
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d invalid stack(s)", failed)
			}
			if len(stacks) == 0 {
				return errors.New("no stack found in " + filepath.Join(root, spec.StacksDir))
			}
			return nil
		},
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}
