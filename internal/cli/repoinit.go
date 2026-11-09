package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ermos/minato/internal/client"
	"github.com/ermos/minato/internal/config"
	"github.com/ermos/minato/internal/secrets"
)

const (
	exampleCompose = `services:
  whoami:
    image: traefik/whoami:${WHOAMI_TAG:-latest}
    command: ["--name", "${vars.NAME}"]
    ports:
      - "8080:80"
`
	exampleStack = `# Stack settings, see https://github.com/ermos/minato#stack-settings-minatoyml
mode: compose          # compose | swarm

vars:
  NAME: hello-from-${stack.name}

env:                   # exported to compose, usable as ${NAME} in compose.yml
  WHOAMI_TAG: v1.10

deploy:
  auto: true           # deploy on every git change
  pull: missing        # missing | always | never

# webhooks:
#   update:
#     action: pull
#     services: [whoami]
#     token: ${secrets.HOOK_TOKEN}   # minato secrets set hello HOOK_TOKEN --random 32
`
	exampleGitignore = `# Never commit private keys or decrypted secrets.
*.key
*.dec.yml
.env.local
`
)

func repoInitCmd() *cobra.Command {
	var recipients []string
	var remote, stacksDir string
	var noKey, noExample, noGit, force bool
	cmd := &cobra.Command{
		Use:   "init [DIR]",
		Short: "Create a GitOps repository (.minato.yml, stacks/, example stack)",
		Long: `Create a minato GitOps repository in DIR (current folder by default).

Secret recipients are collected automatically:
  - your personal age key (generated in ~/.config/minato/age.key if missing),
  - the server key of the current remote (after "minato login"), or of the
    local daemon when run on the server,
  - every --recipient.`,
		Example: `  minato init infra
  minato init . --remote prod --recipient age1ci...`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			specPath := filepath.Join(dir, config.RepoFileName)
			if _, err := os.Stat(specPath); err == nil && !force {
				return fmt.Errorf("%s already exists (use --force)", specPath)
			}
			if err := os.MkdirAll(filepath.Join(dir, stacksDir), 0o755); err != nil {
				return err
			}

			add := func(r, origin string) {
				if r != "" && !slices.Contains(recipients, r) {
					recipients = append(recipients, r)
					fmt.Printf("  + %s (%s)\n", r, origin)
				}
			}
			fmt.Println("secret recipients:")
			for _, r := range recipients {
				if _, err := secrets.ParseRecipient(r); err != nil {
					return err
				}
				fmt.Printf("  + %s (--recipient)\n", r)
			}
			if !noKey {
				ids, err := secrets.LoadIdentities(secrets.DefaultIdentityFiles()...)
				if err == nil {
					for _, p := range secrets.PublicKeys(ids) {
						add(p, "your key")
					}
				} else {
					pub, err := secrets.GenerateKey(defaultUserKey())
					if err != nil {
						return err
					}
					add(pub, "your new key "+defaultUserKey())
				}
			}
			for _, p := range serverRecipients(remote) {
				add(p.key, p.origin)
			}
			if len(recipients) == 0 {
				fmt.Println("  (none yet: add the server key from `minato server info` later)")
			}

			var b strings.Builder
			b.WriteString("# Minato GitOps repository settings.\n")
			fmt.Fprintf(&b, "stacks_dir: %s\n\nsecrets:\n", stacksDir)
			b.WriteString("  # Public keys able to decrypt secret files: the server and every person or\n")
			b.WriteString("  # CI job editing secrets locally. Run `minato secrets rotate` after a change.\n")
			b.WriteString("  recipients:\n")
			for _, r := range recipients {
				fmt.Fprintf(&b, "    - %s\n", r)
			}
			if len(recipients) == 0 {
				b.WriteString("    []\n")
			}
			files := map[string]string{specPath: b.String()}
			if gi := filepath.Join(dir, ".gitignore"); !exists(gi) {
				files[gi] = exampleGitignore
			}
			if !noExample {
				ex := filepath.Join(dir, stacksDir, "hello")
				if !exists(ex) {
					files[filepath.Join(ex, "compose.yml")] = exampleCompose
					files[filepath.Join(ex, "minato.yml")] = exampleStack
					files[filepath.Join(ex, "files", ".gitkeep")] = ""
				}
			}
			for p, content := range files {
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					return err
				}
			}
			if !noGit && !exists(filepath.Join(dir, ".git")) {
				if out, err := exec.Command("git", "-C", dir, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
					return fmt.Errorf("git init: %v: %s", err, out)
				}
			}

			fmt.Printf("\n✔ repository ready in %s\n\nNext steps:\n", dir)
			fmt.Println("  1. minato validate " + dir)
			fmt.Println("  2. commit and push it to your git server")
			fmt.Println("  3. on the VPS: sudo minato server init --repo <git url> --generate-ssh-key --admin $USER --systemd")
			fmt.Println("  4. minato login <vps> then minato status")
			return nil
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&recipients, "recipient", nil, "extra age or ssh public key allowed to decrypt (repeatable)")
	f.StringVar(&remote, "remote-key", "", "add the server key of this saved remote (default: current remote)")
	f.StringVar(&stacksDir, "stacks-dir", config.DefaultStackDir, "folder holding the stacks")
	f.BoolVar(&noKey, "no-key", false, "do not add (or generate) your personal age key")
	f.BoolVar(&noExample, "no-example", false, "do not create the example stack")
	f.BoolVar(&noGit, "no-git", false, "do not run git init")
	f.BoolVar(&force, "force", false, "overwrite .minato.yml")
	return cmd
}

type recipient struct{ key, origin string }

// serverRecipients asks the local daemon or a remote for its public key.
// The endpoint is public, no login is needed.
func serverRecipients(remote string) []recipient {
	var c *client.Client
	origin := "local server"
	if remotes, err := client.LoadRemotes(); err == nil {
		if name, rem, err := remotes.Get(remote); err == nil {
			c, origin = client.NewRemote(rem.URL, rem.Fingerprint, ""), "server "+name
		}
	}
	if c == nil {
		if _, err := os.Stat(config.DefaultSocket); err != nil {
			return nil
		}
		c = client.NewSocket(config.DefaultSocket)
	}
	info, err := c.Info()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ! could not get the key of %s: %v\n", origin, err)
		return nil
	}
	out := make([]recipient, 0, len(info.Recipients))
	for _, r := range info.Recipients {
		out = append(out, recipient{r, origin})
	}
	return out
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
