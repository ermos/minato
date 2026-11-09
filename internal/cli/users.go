package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"github.com/ermos/minato/internal/auth"
)

// userCmd manages remote users. It edits the users file directly, so it runs
// on the server as root and needs no authentication.
func userCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage remote users (on the server, as root)",
		Long: `Remote users log in with "minato login" using a password, an SSH key or an
SSH certificate signed by a CA listed in auth.ssh_cas.

Roles:
  viewer     status, hooks, logs, ps, secret names
  operator   + sync, deploy, pull, restart, down, write and rotate secrets
  admin      + read secret values and rendered compose files`,
	}
	cmd.AddCommand(userAddCmd(g), userListCmd(g), userRmCmd(g), userPasswdCmd(g), userRoleCmd(g), userKeyCmd(g))
	return cmd
}

func loadUsers(g *globals) (*auth.Users, error) {
	cfg, err := g.config()
	if err != nil {
		return nil, err
	}
	return auth.LoadUsers(cfg.Auth.UsersFile)
}

func mustFind(users *auth.Users, name string) (*auth.User, error) {
	u := users.Find(name)
	if u == nil {
		return nil, fmt.Errorf("user %s not found", name)
	}
	return u, nil
}

func revokeSessions(g *globals, user string) {
	cfg, err := g.config()
	if err != nil {
		return
	}
	if s, err := auth.LoadSessions(cfg.SessionsPath()); err == nil {
		if n := s.RevokeUser(user); n > 0 {
			fmt.Printf("revoked %d session(s)\n", n)
		}
	}
}

func userAddCmd(g *globals) *cobra.Command {
	var role string
	var keys []string
	var password bool
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Create a user",
		Example: `  sudo minato user add alice --role admin --ssh-key "$(cat alice.pub)"
  sudo minato user add ci --role operator --password`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := auth.ParseRole(role)
			if err != nil {
				return err
			}
			users, err := loadUsers(g)
			if err != nil {
				return err
			}
			u, err := users.Add(args[0], r)
			if err != nil {
				return err
			}
			for _, k := range keys {
				line, err := readKeyArg(k)
				if err != nil {
					return err
				}
				if err := u.AddSSHKey(line); err != nil {
					return err
				}
			}
			if password || len(keys) == 0 {
				pw, err := promptNewPassword(u.Name)
				if err != nil {
					return err
				}
				if err := u.SetPassword(pw); err != nil {
					return err
				}
			}
			if err := users.Save(); err != nil {
				return err
			}
			fmt.Printf("✔ user %s (%s) created\n", u.Name, u.Role)
			return nil
		},
	}
	cmd.Flags().StringVar(&role, "role", "operator", "viewer, operator or admin")
	cmd.Flags().StringArrayVar(&keys, "ssh-key", nil, "authorized SSH public key, or @file (repeatable)")
	cmd.Flags().BoolVar(&password, "password", false, "also set a password")
	return cmd
}

func userListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List users",
		RunE: func(cmd *cobra.Command, args []string) error {
			users, err := loadUsers(g)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 3, ' ', 0)
			fmt.Fprintln(w, "USER\tROLE\tPASSWORD\tSSH KEYS")
			for _, u := range users.List {
				pw := "no"
				if u.PasswordHash != "" {
					pw = "yes"
				}
				var fps []string
				for _, k := range u.SSHKeys {
					if pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(k)); err == nil {
						fps = append(fps, strings.TrimSpace(ssh.FingerprintSHA256(pub)+" "+comment))
					}
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", u.Name, u.Role, pw, strings.Join(fps, ", "))
			}
			return w.Flush()
		},
	}
}

func userRmCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "rm NAME",
		Short: "Delete a user and revoke its sessions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			users, err := loadUsers(g)
			if err != nil {
				return err
			}
			if !users.Remove(args[0]) {
				return fmt.Errorf("user %s not found", args[0])
			}
			if err := users.Save(); err != nil {
				return err
			}
			revokeSessions(g, args[0])
			fmt.Printf("✔ user %s deleted\n", args[0])
			return nil
		},
	}
}

func userPasswdCmd(g *globals) *cobra.Command {
	var remove bool
	cmd := &cobra.Command{
		Use:   "passwd NAME",
		Short: "Change (or remove) a password and revoke sessions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			users, err := loadUsers(g)
			if err != nil {
				return err
			}
			u, err := mustFind(users, args[0])
			if err != nil {
				return err
			}
			pw := ""
			if !remove {
				if pw, err = promptNewPassword(u.Name); err != nil {
					return err
				}
			}
			if err := u.SetPassword(pw); err != nil {
				return err
			}
			if err := users.Save(); err != nil {
				return err
			}
			revokeSessions(g, u.Name)
			fmt.Println("✔ password updated")
			return nil
		},
	}
	cmd.Flags().BoolVar(&remove, "remove", false, "remove the password (SSH only)")
	return cmd
}

func userRoleCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "role NAME ROLE",
		Short: "Change the role of a user",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := auth.ParseRole(args[1])
			if err != nil {
				return err
			}
			users, err := loadUsers(g)
			if err != nil {
				return err
			}
			u, err := mustFind(users, args[0])
			if err != nil {
				return err
			}
			u.Role = r
			return users.Save()
		},
	}
}

func userKeyCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "key", Short: "Manage the SSH keys of a user"}
	cmd.AddCommand(&cobra.Command{
		Use:   "add NAME KEY|@FILE",
		Short: "Authorize an SSH public key",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			users, err := loadUsers(g)
			if err != nil {
				return err
			}
			u, err := mustFind(users, args[0])
			if err != nil {
				return err
			}
			line, err := readKeyArg(args[1])
			if err != nil {
				return err
			}
			if err := u.AddSSHKey(line); err != nil {
				return err
			}
			return users.Save()
		},
	}, &cobra.Command{
		Use:   "rm NAME FINGERPRINT|COMMENT",
		Short: "Remove an SSH key (see fingerprints with `minato user ls`)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			users, err := loadUsers(g)
			if err != nil {
				return err
			}
			u, err := mustFind(users, args[0])
			if err != nil {
				return err
			}
			if !u.RemoveSSHKey(args[1]) {
				return errors.New("no matching key")
			}
			if err := users.Save(); err != nil {
				return err
			}
			revokeSessions(g, u.Name)
			return nil
		},
	})
	return cmd
}

// readKeyArg accepts a public key or @path to a public key file.
func readKeyArg(arg string) (string, error) {
	if p, ok := strings.CutPrefix(arg, "@"); ok {
		data, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
	return strings.TrimSpace(arg), nil
}

func promptPassword(prompt string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("a terminal is required to type a password")
	}
	fmt.Fprint(os.Stderr, prompt)
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(pw), err
}

func promptNewPassword(user string) (string, error) {
	pw, err := promptPassword("New password for " + user + ": ")
	if err != nil {
		return "", err
	}
	again, err := promptPassword("Confirm: ")
	if err != nil {
		return "", err
	}
	if pw != again {
		return "", errors.New("passwords do not match")
	}
	return pw, nil
}
