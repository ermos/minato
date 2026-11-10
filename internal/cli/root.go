// Package cli implements the minato command line.
package cli

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ermos/minato/internal/client"
	"github.com/ermos/minato/internal/config"
)

// Version is set at build time.
var Version = "dev"

type globals struct {
	configPath string
	remote     string
}

func (g *globals) config() (*config.Config, error) { return config.Load(g.configPath) }

// client picks the API endpoint:
//  1. --remote / MINATO_REMOTE: that saved remote,
//  2. the local socket when the daemon runs here and is reachable (root),
//  3. the current remote saved by `minato login`.
func (g *globals) client() (*client.Client, error) {
	if g.remote != "" {
		return remoteClient(g.remote)
	}
	socket := config.DefaultSocket
	if cfg, err := g.config(); err == nil {
		socket = cfg.Socket
	}
	if conn, err := net.DialTimeout("unix", socket, time.Second); err == nil {
		_ = conn.Close()
		return client.NewSocket(socket), nil
	}
	if remotes, err := client.LoadRemotes(); err == nil && remotes.Current != "" {
		return remoteClient(remotes.Current)
	}
	return client.NewSocket(socket), nil
}

func remoteClient(name string) (*client.Client, error) {
	remotes, err := client.LoadRemotes()
	if err != nil {
		return nil, err
	}
	name, rem, err := remotes.Get(name)
	if err != nil {
		return nil, err
	}
	if rem.Token == "" || time.Now().After(rem.Expires) {
		return nil, fmt.Errorf("not logged in to %s, run: minato login %s", name, name)
	}
	return rem.Client(), nil
}

// Execute runs the CLI.
func Execute() {
	if err := newRoot().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	g := &globals{}
	root := &cobra.Command{
		Use:   "minato",
		Short: "Minato: GitOps for Docker and Docker Swarm on a VPS",
		Long: `Minato (港, "harbor") keeps your Docker Compose and Swarm stacks in sync
with a git repository. Secrets are versioned encrypted and decrypted on the
server, files next to the compose file are mounted easily, webhooks let CI
trigger precise updates, and the CLI works locally on the server or remotely
after "minato login".`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
	}
	defaultConfig := config.DefaultPath
	if p := os.Getenv("MINATO_CONFIG"); p != "" {
		defaultConfig = p
	}
	root.PersistentFlags().StringVarP(&g.configPath, "config", "c", defaultConfig, "server configuration file (env MINATO_CONFIG)")
	root.PersistentFlags().StringVarP(&g.remote, "remote", "r", os.Getenv("MINATO_REMOTE"), "saved remote to use instead of the local daemon (env MINATO_REMOTE)")

	root.AddGroup(
		&cobra.Group{ID: "repo", Title: "Repository:"},
		&cobra.Group{ID: "ops", Title: "Operations (local or remote):"},
		&cobra.Group{ID: "remote", Title: "Remote access:"},
		&cobra.Group{ID: "server", Title: "Server administration (on the VPS):"},
	)
	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add("repo", repoInitCmd(), secretsCmd(g), renderCmd(g), validateCmd())
	add("ops", statusCmd(g), statsCmd(g), syncCmd(g),
		actionCmd(g, "deploy", "Deploy a stack (or some services) from the latest commit"),
		actionCmd(g, "pull", "Pull fresh images and update a stack or some services"),
		actionCmd(g, "restart", "Restart a stack or some services"),
		actionCmd(g, "recreate", "Force recreate the containers of a stack or some services"),
		actionCmd(g, "down", "Stop and remove a stack (redeployed on next sync unless disabled in git)"),
		logsCmd(g), psCmd(g), hooksCmd(g), composeCmd(g))
	add("remote", loginCmd(), logoutCmd(), remoteCmd(), whoamiCmd(g))
	add("server", serverCmd(g), userCmd(g))
	root.AddCommand(updateCmd())
	return root
}
