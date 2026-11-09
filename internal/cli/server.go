package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ermos/minato/internal/auth"
	"github.com/ermos/minato/internal/config"
	"github.com/ermos/minato/internal/daemon"
	"github.com/ermos/minato/internal/secrets"
	"github.com/ermos/minato/internal/tlsutil"
)

func serverCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Set up and run the minato server (on the VPS)",
	}
	cmd.AddCommand(serverInitCmd(g), serverRunCmd(g), serverInfoCmd(g))
	return cmd
}

func serverRunCmd(g *globals) *cobra.Command {
	var jsonLogs, debug bool
	cmd := &cobra.Command{
		Use:     "run",
		Aliases: []string{"daemon"},
		Short:   "Run the daemon in the foreground (git sync, deployments, API, webhooks)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := g.config()
			if err != nil {
				return err
			}
			opts := &slog.HandlerOptions{}
			if debug {
				opts.Level = slog.LevelDebug
			}
			var handler slog.Handler = slog.NewTextHandler(os.Stderr, opts)
			if jsonLogs {
				handler = slog.NewJSONHandler(os.Stderr, opts)
			}
			daemon.Version = Version
			d, err := daemon.New(cfg, slog.New(handler))
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return d.Run(ctx)
		},
	}
	cmd.Flags().BoolVar(&jsonLogs, "json", false, "log as JSON")
	cmd.Flags().BoolVar(&debug, "debug", false, "debug logs")
	return cmd
}

func serverInfoCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Print what remote users need: URL, certificate fingerprint, age public key",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := g.config()
			if err != nil {
				return err
			}
			printServerInfo(cfg)
			return nil
		},
	}
}

func printServerInfo(cfg *config.Config) {
	if ids, err := secrets.LoadIdentities(cfg.AgeKey); err == nil {
		fmt.Printf("age public key   %s\n", strings.Join(secrets.PublicKeys(ids), ", "))
	}
	if cfg.Server.Listen == "" {
		fmt.Println("remote access    disabled (server.listen is empty)")
		return
	}
	fmt.Printf("remote API       %s (listen %s, tls %s)\n", cfg.Server.PublicURL, cfg.Server.Listen, cfg.Server.TLS)
	if cfg.Server.TLS == config.TLSSelfSigned {
		host, _ := os.Hostname()
		if fp, err := tlsutil.EnsureSelfSigned(cfg.TLSDir(), cfg.Server.Domain, host); err == nil {
			fmt.Printf("fingerprint      %s\n", fp)
		}
	}
	pw := "enabled"
	if !cfg.Auth.PasswordEnabled() {
		pw = "disabled (ssh keys only)"
	}
	fmt.Printf("password login   %s\n", pw)
	fmt.Printf("webhooks         %s/hooks/...\n", cfg.Server.PublicURL)
}

const systemdUnit = `[Unit]
Description=Minato GitOps daemon
Documentation=https://github.com/ermos/minato
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service

[Service]
ExecStart=%s server run --config %s
Restart=always
RestartSec=5
RuntimeDirectory=minato

[Install]
WantedBy=multi-user.target
`

func serverInitCmd(g *globals) *cobra.Command {
	var (
		repoURL, branch, sshKey, listen, domain, tlsMode, localDir string
		admin, adminKey                                            string
		genSSH, systemd, force, noPassword                         bool
		poll                                                       time.Duration
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Configure this server: config, age key, TLS, first admin, systemd unit",
		Example: `  sudo minato server init --repo git@github.com:me/infra.git --generate-ssh-key \
      --admin alice --admin-ssh-key "$(cat alice.pub)" --systemd
  sudo minato server init --repo https://TOKEN@github.com/me/infra.git \
      --domain minato.example.com --tls acme --admin alice`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if repoURL == "" && localDir == "" {
				return errors.New("--repo (or --local-dir) is required")
			}
			if _, err := os.Stat(g.configPath); err == nil && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", g.configPath)
			}
			switch tlsMode {
			case config.TLSSelfSigned, config.TLSAcme, config.TLSOff:
			default:
				return fmt.Errorf("--tls must be self-signed, acme or off")
			}
			if tlsMode == config.TLSAcme && domain == "" {
				return errors.New("--tls acme needs --domain")
			}
			if !cmd.Flags().Changed("listen") {
				switch tlsMode {
				case config.TLSOff:
					// Plain HTTP must only be reachable by the local reverse proxy.
					listen = "127.0.0.1:7373"
				case config.TLSAcme:
					listen = ":443"
				}
			}
			confDir := filepath.Dir(g.configPath)
			if err := os.MkdirAll(confDir, 0o700); err != nil {
				return err
			}

			cfg := config.Config{
				Repo:    config.Repo{URL: repoURL, Branch: branch, Dir: localDir, Poll: poll},
				DataDir: config.DefaultDataDir,
				Socket:  config.DefaultSocket,
				AgeKey:  filepath.Join(confDir, "age.key"),
				Server:  config.Server{Listen: listen, Domain: domain, TLS: tlsMode},
			}
			cfg.Auth.UsersFile = filepath.Join(confDir, "users.yml")
			cfg.Webhooks.Token = randomToken(24)
			cfg.Webhooks.Secret = randomToken(24)
			if genSSH || sshKey != "" {
				if sshKey == "" {
					sshKey = filepath.Join(confDir, "deploy_key")
				}
				cfg.Repo.SSHKey = sshKey
			}

			var sshPub string
			if genSSH {
				if _, err := os.Stat(sshKey); err != nil {
					out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "minato@"+hostname(), "-f", sshKey).CombinedOutput()
					if err != nil {
						return fmt.Errorf("ssh-keygen: %v: %s", err, out)
					}
				}
				pub, err := os.ReadFile(sshKey + ".pub")
				if err != nil {
					return err
				}
				sshPub = strings.TrimSpace(string(pub))
			}
			if _, err := os.Stat(cfg.AgeKey); err != nil {
				if _, err := secrets.GenerateKey(cfg.AgeKey); err != nil {
					return err
				}
			}

			if err := os.WriteFile(g.configPath, []byte(renderConfig(cfg, noPassword)), 0o600); err != nil {
				return err
			}
			loaded, err := config.Load(g.configPath)
			if err != nil {
				return err
			}

			if admin != "" {
				if err := createAdmin(loaded, admin, adminKey, noPassword); err != nil {
					return err
				}
			}

			fmt.Printf("✔ config written to %s\n\n", g.configPath)
			printServerInfo(loaded)
			fmt.Println()
			fmt.Println("Add the age public key to secrets.recipients in .minato.yml (minato init does it with a remote).")
			if sshPub != "" {
				fmt.Printf("\nGit deploy key, add it to your repository (write access enables remote secret edition):\n  %s\n", sshPub)
			}
			fmt.Printf("\nWebhook /hooks/sync token:          %s\n", cfg.Webhooks.Token)
			fmt.Printf("Webhook HMAC secret (GitHub/Gitea): %s\n", cfg.Webhooks.Secret)
			if admin != "" && listen != "" {
				fmt.Printf("\nFrom your laptop:\n  minato login %s -u %s\n", loaded.Server.PublicURL, admin)
			} else if listen != "" {
				fmt.Println("\nCreate a remote user: sudo minato user add alice --role admin --ssh-key \"ssh-ed25519 ...\"")
			}

			if systemd {
				if err := installSystemd(g.configPath); err != nil {
					return err
				}
				fmt.Println("\n✔ systemd service minato enabled and started (journalctl -fu minato)")
			} else {
				fmt.Println("\nStart the daemon with: sudo minato server run (or rerun with --systemd)")
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&repoURL, "repo", "", "git repository URL (ssh or https)")
	f.StringVar(&branch, "branch", "main", "branch to track")
	f.StringVar(&localDir, "local-dir", "", "use a local folder instead of cloning")
	f.StringVar(&sshKey, "ssh-key", "", "SSH private key used for git")
	f.BoolVar(&genSSH, "generate-ssh-key", false, "generate an ed25519 deploy key")
	f.DurationVar(&poll, "poll", time.Minute, "git polling interval (0 to rely on webhooks only)")
	f.StringVar(&listen, "listen", config.DefaultListen, "public API and webhooks address (empty disables remote access)")
	f.StringVar(&domain, "domain", "", "domain name of the server (minato.example.com)")
	f.StringVar(&tlsMode, "tls", config.TLSSelfSigned, "self-signed, acme (Let's Encrypt, needs --domain and port 443) or off (behind a reverse proxy)")
	f.StringVar(&admin, "admin", "", "create a first admin user")
	f.StringVar(&adminKey, "admin-ssh-key", "", "SSH public key of the admin (or @file)")
	f.BoolVar(&noPassword, "no-password", false, "disable password login (SSH keys and certificates only)")
	f.BoolVar(&systemd, "systemd", false, "install and start the systemd service")
	f.BoolVar(&force, "force", false, "overwrite an existing config")
	return cmd
}

func createAdmin(cfg *config.Config, name, key string, noPassword bool) error {
	users, err := auth.LoadUsers(cfg.Auth.UsersFile)
	if err != nil {
		return err
	}
	u := users.Find(name)
	if u == nil {
		if u, err = users.Add(name, auth.RoleAdmin); err != nil {
			return err
		}
	}
	if key != "" {
		line, err := readKeyArg(key)
		if err != nil {
			return err
		}
		if err := u.AddSSHKey(line); err != nil && err.Error() != "key already present" {
			return err
		}
	} else if !noPassword {
		pw, err := promptNewPassword(name)
		if err != nil {
			return err
		}
		if err := u.SetPassword(pw); err != nil {
			return err
		}
	} else {
		return errors.New("--no-password needs --admin-ssh-key")
	}
	return users.Save()
}

func installSystemd(configPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	abs, _ := filepath.Abs(configPath)
	unit := fmt.Sprintf(systemdUnit, exe, abs)
	if err := os.WriteFile("/etc/systemd/system/minato.service", []byte(unit), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", "minato"}} {
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", args[0], err, out)
		}
	}
	return nil
}

func renderConfig(c config.Config, noPassword bool) string {
	q := strconv.Quote
	return fmt.Sprintf(`# Minato daemon configuration. Apply changes with: systemctl restart minato

repo:
  url: %s
  branch: %s
  ssh_key: %s     # private key used for git over ssh (write access enables remote secret edition)
  dir: %s         # local checkout, default <data_dir>/repo (or a local folder without url)
  poll: %s        # polling interval, 0 to rely on webhooks only

data_dir: %s
age_key: %s       # server identity used to decrypt secrets
socket: %s        # local API, root only, no authentication
prune: false      # take down stacks removed from git

# Public endpoint: remote API (minato login) and webhooks.
server:
  listen: %s      # empty disables remote access and webhooks
  domain: %s      # e.g. minato.example.com
  tls: %s         # self-signed (clients pin the fingerprint) | acme (Let's Encrypt) | files | off (behind a TLS reverse proxy)
  cert_file: ""   # tls: files
  key_file: ""
  acme_email: ""
  public_url: ""  # computed from domain/listen when empty
  trust_proxy: %v  # use X-Forwarded-For (behind a reverse proxy)

auth:
  users_file: %s  # manage with: minato user --help
  password: %v    # false allows SSH keys and certificates only
  ssh_cas: []     # trusted SSH certificate authorities, "ssh-ed25519 AAAA..."
  session_ttl: 720h

webhooks:
  token: %s       # for POST /hooks/sync (bearer, X-Minato-Token or ?token=)
  secret: %s      # HMAC secret for GitHub / Gitea push webhooks

notify:
  url: ""         # Slack or Discord incoming webhook
  on: [failure]   # failure, success
`, q(c.Repo.URL), q(c.Repo.Branch), q(c.Repo.SSHKey), q(c.Repo.Dir), c.Repo.Poll,
		q(c.DataDir), q(c.AgeKey), q(c.Socket),
		q(c.Server.Listen), q(c.Server.Domain), q(c.Server.TLS),
		c.Server.TLS == config.TLSOff, q(c.Auth.UsersFile), !noPassword,
		q(c.Webhooks.Token), q(c.Webhooks.Secret))
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}
