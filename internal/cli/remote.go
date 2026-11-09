package cli

import (
	"bufio"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/ermos/minato/internal/auth"
	"github.com/ermos/minato/internal/client"
	"github.com/ermos/minato/internal/config"
	"github.com/ermos/minato/internal/daemon"
)

func loginCmd() *cobra.Command {
	var name, user, keyFile, fingerprint string
	var usePassword, yes bool
	cmd := &cobra.Command{
		Use:   "login SERVER",
		Short: "Log in to a remote minato server (SSH key, SSH certificate or password)",
		Long: `Log in to a remote server and save it as the current remote. SERVER is a URL
(https://vps.example.com:7373), a host (port 7373 is assumed) or the name of a
saved remote.

SSH keys are tried first (ssh-agent, ~/.ssh/id_*, their -cert.pub
certificates, or -i), then the password if the server allows it.

Self-signed servers are pinned on first login: compare the fingerprint with
the output of "sudo minato server info" on the server.`,
		Example: `  minato login vps.example.com -u alice
  minato login https://minato.example.com -u alice --password
  minato login prod                      # renew the session of a saved remote`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			remotes, err := client.LoadRemotes()
			if err != nil {
				return err
			}
			rem := &client.Remote{}
			if saved, ok := remotes.Servers[args[0]]; ok {
				name, rem = args[0], saved
			} else {
				if rem.URL, err = normalizeURL(args[0]); err != nil {
					return err
				}
				if name == "" {
					u, _ := url.Parse(rem.URL)
					name = u.Hostname()
				}
				if existing, ok := remotes.Servers[name]; ok && existing.URL == rem.URL {
					rem = existing
				}
			}
			if user != "" {
				rem.User = user
			}
			if rem.User == "" {
				rem.User = os.Getenv("USER")
			}

			fp, trusted, err := client.ProbeCertificate(rem.URL)
			if err != nil {
				return fmt.Errorf("cannot connect to %s: %w", rem.URL, err)
			}
			switch {
			case trusted:
				rem.Fingerprint = ""
			case fingerprint != "":
				if fingerprint != fp {
					return fmt.Errorf("fingerprint mismatch: server presents %s", fp)
				}
				rem.Fingerprint = fp
			case rem.Fingerprint == fp:
			default:
				if rem.Fingerprint != "" {
					fmt.Fprintf(os.Stderr, "WARNING: the certificate of %s CHANGED (was %s).\n", rem.URL, rem.Fingerprint)
				}
				fmt.Fprintf(os.Stderr, "The server uses a self-signed certificate.\n  fingerprint: %s\n", fp)
				fmt.Fprintln(os.Stderr, "Compare it with `sudo minato server info` on the server.")
				if !yes && !confirm("Trust this server?") {
					return errors.New("aborted")
				}
				rem.Fingerprint = fp
			}

			c := client.NewRemote(rem.URL, rem.Fingerprint, "")
			info, err := c.Info()
			if err != nil {
				return err
			}
			var resp *daemon.LoginResponse
			if !usePassword {
				resp, err = sshLogin(c, rem.User, keyFile)
				if err != nil && keyFile != "" {
					return err
				}
			}
			if resp == nil {
				if !info.Password {
					return errors.New("SSH login failed and password login is disabled on this server")
				}
				pw, err := promptPassword(fmt.Sprintf("Password for %s@%s: ", rem.User, name))
				if err != nil {
					return err
				}
				if resp, err = c.PasswordLogin(rem.User, pw); err != nil {
					return err
				}
			}
			rem.Token, rem.Expires = resp.Token, resp.Expires
			remotes.Servers[name] = rem
			remotes.Current = name
			if err := remotes.Save(); err != nil {
				return err
			}
			fmt.Printf("✔ logged in to %s as %s (%s), session valid until %s\n", name, resp.User, resp.Role, resp.Expires.Format("2006-01-02"))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&user, "user", "u", "", "user name (default $USER)")
	f.StringVar(&name, "name", "", "name of the saved remote (default: host)")
	f.StringVarP(&keyFile, "identity", "i", "", "SSH private key to use")
	f.BoolVar(&usePassword, "password", false, "skip SSH keys and use a password")
	f.StringVar(&fingerprint, "fingerprint", "", "expected certificate fingerprint (sha256:...)")
	f.BoolVarP(&yes, "yes", "y", false, "trust a self-signed certificate without asking")
	return cmd
}

func normalizeURL(s string) (string, error) {
	if !strings.Contains(s, "://") {
		host := s
		if _, _, err := net.SplitHostPort(s); err != nil {
			_, port, _ := net.SplitHostPort(config.DefaultListen)
			host = net.JoinHostPort(s, port)
		}
		s = "https://" + host
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid server %q", s)
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

// sshLogin tries every available signer until one is accepted.
func sshLogin(c *client.Client, user, keyFile string) (*daemon.LoginResponse, error) {
	signers, err := sshSigners(keyFile)
	if err != nil {
		return nil, err
	}
	if len(signers) == 0 {
		return nil, errors.New("no SSH key available")
	}
	var lastErr error
	for _, s := range signers {
		nonce, err := c.Challenge(user)
		if err != nil {
			return nil, err
		}
		sig, err := signWith(s, auth.SignedData(user, nonce))
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := c.SSHLogin(daemon.SSHLogin{
			User: user, Nonce: nonce, PublicKey: s.PublicKey().Marshal(), Signature: ssh.Marshal(sig),
		})
		if err == nil {
			return resp, nil
		}
		lastErr = err
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 429 {
			break
		}
	}
	return nil, fmt.Errorf("SSH login failed: %w", lastErr)
}

func signWith(s ssh.Signer, data []byte) (*ssh.Signature, error) {
	t := s.PublicKey().Type()
	if as, ok := s.(ssh.AlgorithmSigner); ok && (t == ssh.KeyAlgoRSA || t == ssh.CertAlgoRSAv01) {
		return as.SignWithAlgorithm(rand.Reader, data, ssh.KeyAlgoRSASHA256)
	}
	return s.Sign(rand.Reader, data)
}

// sshSigners returns certificates first, then plain keys: the explicit key
// file, or ssh-agent keys and unencrypted default keys.
func sshSigners(keyFile string) ([]ssh.Signer, error) {
	if keyFile != "" {
		s, err := loadKeyFile(keyFile, true)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	var out []ssh.Signer
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			if signers, err := agent.NewClient(conn).Signers(); err == nil {
				out = append(out, signers...)
			}
		}
	}
	home, _ := os.UserHomeDir()
	for _, n := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		if s, err := loadKeyFile(filepath.Join(home, ".ssh", n), false); err == nil {
			out = append(out, s...)
		}
	}
	certsFirst := make([]ssh.Signer, 0, len(out))
	for _, s := range out {
		if _, ok := s.PublicKey().(*ssh.Certificate); ok {
			certsFirst = append(certsFirst, s)
		}
	}
	for _, s := range out {
		if _, ok := s.PublicKey().(*ssh.Certificate); !ok {
			certsFirst = append(certsFirst, s)
		}
	}
	return certsFirst, nil
}

func loadKeyFile(path string, prompt bool) ([]ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(data)
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		if !prompt {
			return nil, err
		}
		pw, perr := promptPassword("Passphrase for " + path + ": ")
		if perr != nil {
			return nil, perr
		}
		signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(pw))
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := []ssh.Signer{}
	if certData, err := os.ReadFile(path + "-cert.pub"); err == nil {
		if pub, _, _, _, err := ssh.ParseAuthorizedKey(certData); err == nil {
			if cert, ok := pub.(*ssh.Certificate); ok {
				if cs, err := ssh.NewCertSigner(cert, signer); err == nil {
					out = append(out, cs)
				}
			}
		}
	}
	return append(out, signer), nil
}

func confirm(q string) bool {
	fmt.Fprintf(os.Stderr, "%s [y/N] ", q)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout [REMOTE]",
		Short: "Revoke the session of a remote (current by default)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			remotes, err := client.LoadRemotes()
			if err != nil {
				return err
			}
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			name, rem, err := remotes.Get(name)
			if err != nil {
				return err
			}
			if rem.Token != "" {
				_ = rem.Client().Logout()
			}
			rem.Token = ""
			if err := remotes.Save(); err != nil {
				return err
			}
			fmt.Printf("✔ logged out of %s\n", name)
			return nil
		},
	}
}

func whoamiCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the server and identity used by the CLI",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client()
			if err != nil {
				return err
			}
			id, err := c.Whoami()
			if err != nil {
				return err
			}
			fmt.Printf("%s (%s) on %s\n", id.Name, id.Role, c.Where())
			return nil
		},
	}
}

func remoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Manage saved remote servers",
	}
	cmd.AddCommand(&cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List saved remotes",
		RunE: func(cmd *cobra.Command, args []string) error {
			remotes, err := client.LoadRemotes()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 3, ' ', 0)
			fmt.Fprintln(w, "\tNAME\tURL\tUSER\tSESSION")
			for _, n := range remotes.Names() {
				r := remotes.Servers[n]
				cur, session := "", "logged out"
				if n == remotes.Current {
					cur = "*"
				}
				if r.Token != "" {
					session = "until " + r.Expires.Format("2006-01-02")
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", cur, n, r.URL, r.User, session)
			}
			return w.Flush()
		},
	}, &cobra.Command{
		Use:   "use NAME",
		Short: "Select the current remote",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			remotes, err := client.LoadRemotes()
			if err != nil {
				return err
			}
			if _, _, err := remotes.Get(args[0]); err != nil {
				return err
			}
			remotes.Current = args[0]
			return remotes.Save()
		},
	}, &cobra.Command{
		Use:   "rm NAME",
		Short: "Forget a remote",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			remotes, err := client.LoadRemotes()
			if err != nil {
				return err
			}
			_, rem, err := remotes.Get(args[0])
			if err != nil {
				return err
			}
			if rem.Token != "" {
				_ = rem.Client().Logout()
			}
			delete(remotes.Servers, args[0])
			if remotes.Current == args[0] {
				remotes.Current = ""
			}
			return remotes.Save()
		},
	})
	return cmd
}
