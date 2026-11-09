package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"filippo.io/age"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ermos/minato/internal/client"
	"github.com/ermos/minato/internal/config"
	"github.com/ermos/minato/internal/daemon"
	"github.com/ermos/minato/internal/secrets"
)

type secretsOpts struct {
	g          *globals
	recipients []string
}

func secretsCmd(g *globals) *cobra.Command {
	o := &secretsOpts{g: g}
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage encrypted secrets, in a working copy or on the server",
		Long: `Secret files keep their keys in clear text and encrypt every value with age.
They are safe to commit and decrypted on the server only.

TARGET is either:
  a file path (stacks/blog/secrets.enc.yml)  edited locally in your working copy.
      Recipients come from .minato.yml (secrets.recipients) or --recipient.
      Reading needs a private key: $MINATO_AGE_KEY, $MINATO_AGE_KEY_FILE,
      ~/.config/minato/age.key or /etc/minato/age.key.
  a stack name (blog)                        edited by the server through the API
      (local socket or "minato login"). The server decrypts with its own key,
      commits, pushes to git and redeploys: no age key needed on your laptop.
  @shared                                    the shared file of the repository
      (secrets.enc.yml at the root), through the API like a stack. Every stack
      can use its values, unless its own file defines the same key.`,
		Example: `  minato secrets set blog DB_PASSWORD --random 32      # on the server, through the API
  minato secrets set @shared POSTGRES_PASSWORD           # shared by every stack
  minato secrets ls blog
  minato secrets edit stacks/blog/secrets.enc.yml        # local working copy
  minato secrets set secrets.enc.yml POSTGRES_PASSWORD   # local shared file`,
	}
	cmd.PersistentFlags().StringArrayVar(&o.recipients, "recipient", nil, "age or ssh public key for local files (repeatable, overrides .minato.yml)")
	cmd.AddCommand(o.keygen(), o.pubkey(), o.set(), o.get(), o.unset(), o.list(), o.show(), o.edit(), o.encrypt(), o.rotate())
	return cmd
}

// isFile reports whether target designates a local file rather than a stack.
func isFile(target string) bool {
	if strings.ContainsAny(target, `/\`) || strings.HasSuffix(target, ".yml") || strings.HasSuffix(target, ".yaml") {
		return true
	}
	_, err := os.Stat(target)
	return err == nil
}

func (o *secretsOpts) api() (*client.Client, error) { return o.g.client() }

func (o *secretsOpts) identities() ([]age.Identity, error) {
	return secrets.LoadIdentities(secrets.DefaultIdentityFiles(config.DefaultAgeKey)...)
}

// recipientsFor resolves recipients for a local secret file.
func (o *secretsOpts) recipientsFor(file string) ([]string, error) {
	if len(o.recipients) > 0 {
		return o.recipients, nil
	}
	if root, ok := config.FindRepoRoot(filepath.Dir(file)); ok {
		spec, err := config.LoadRepoSpec(root)
		if err != nil {
			return nil, err
		}
		if len(spec.Secrets.Recipients) > 0 {
			return spec.Secrets.Recipients, nil
		}
	}
	return nil, errors.New("no recipients: add secrets.recipients to .minato.yml at the repo root or pass --recipient")
}

// open opens a local secret file, or creates an empty one when missing.
func (o *secretsOpts) open(file string, create bool) (*secrets.Secrets, error) {
	e, err := secrets.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) && create {
		rs, err := o.recipientsFor(file)
		if err != nil {
			return nil, err
		}
		return secrets.New(rs)
	}
	if err != nil {
		return nil, err
	}
	ids, err := o.identities()
	if err != nil {
		return nil, err
	}
	return secrets.Open(e, ids)
}

func save(file string, s *secrets.Secrets) error {
	data, err := s.Marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0o644)
}

// remoteValues reads every value of a stack through the API (admin role).
func (o *secretsOpts) remoteValues(stack string) (map[string]string, error) {
	c, err := o.api()
	if err != nil {
		return nil, err
	}
	info, err := c.Secrets(stack, true)
	if err != nil {
		return nil, err
	}
	if info.Values == nil {
		info.Values = map[string]string{}
	}
	return info.Values, nil
}

func (o *secretsOpts) remoteChange(stack string, ch daemon.SecretsChange) error {
	c, err := o.api()
	if err != nil {
		return err
	}
	info, err := c.ChangeSecrets(stack, ch)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "✔ committed %s on the server, deploying\n", short(info.Rev))
	return nil
}

func (o *secretsOpts) keygen() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate a personal age key pair and print the public key",
		RunE: func(cmd *cobra.Command, args []string) error {
			if out == "" {
				out = defaultUserKey()
			}
			pub, err := secrets.GenerateKey(out)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "private key written to %s (keep it safe, never commit it)\n", out)
			fmt.Println(pub)
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "key file (default ~/.config/minato/age.key)")
	return cmd
}

func defaultUserKey() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "minato", "age.key")
}

func (o *secretsOpts) pubkey() *cobra.Command {
	return &cobra.Command{
		Use:   "pubkey",
		Short: "Print the public keys of the available identities",
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := o.identities()
			if err != nil {
				return err
			}
			for _, p := range secrets.PublicKeys(ids) {
				fmt.Println(p)
			}
			return nil
		},
	}
}

func (o *secretsOpts) set() *cobra.Command {
	var random int
	cmd := &cobra.Command{
		Use:   "set TARGET KEY [VALUE]",
		Short: "Set a secret (value from argument, stdin or --random)",
		Example: `  minato secrets set blog DB_PASSWORD --random 32
  cat cert.pem | minato secrets set blog TLS_CERT
  minato secrets set stacks/blog/secrets.enc.yml SMTP_USER mailer`,
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, key := args[0], args[1]
			var value string
			switch {
			case len(args) == 3:
				value = args[2]
			case random > 0:
			default:
				v, err := readValue(key)
				if err != nil {
					return err
				}
				value = v
			}
			if !isFile(target) {
				ch := daemon.SecretsChange{Set: map[string]string{key: value}}
				if random > 0 {
					ch = daemon.SecretsChange{Random: map[string]int{key: random}}
				}
				return o.remoteChange(target, ch)
			}
			if random > 0 {
				value = secrets.RandomValue(random)
			}
			s, err := o.open(target, true)
			if err != nil {
				return err
			}
			if err := s.Set(key, value); err != nil {
				return err
			}
			if err := save(target, s); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "✔ %s set in %s\n", key, target)
			return nil
		},
	}
	cmd.Flags().IntVar(&random, "random", 0, "generate a random value of this length")
	return cmd
}

func readValue(key string) (string, error) {
	info, _ := os.Stdin.Stat()
	if info != nil && info.Mode()&os.ModeCharDevice != 0 {
		fmt.Fprintf(os.Stderr, "value for %s (end with Enter): ", key)
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	data, err := io.ReadAll(os.Stdin)
	return string(data), err
}

// values returns every clear value of a target.
func (o *secretsOpts) values(target string) (map[string]string, error) {
	if !isFile(target) {
		return o.remoteValues(target)
	}
	s, err := o.open(target, false)
	if err != nil {
		return nil, err
	}
	return s.Values(), nil
}

func (o *secretsOpts) get() *cobra.Command {
	return &cobra.Command{
		Use:   "get TARGET KEY",
		Short: "Print a decrypted secret (admin role when remote)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vals, err := o.values(args[0])
			if err != nil {
				return err
			}
			v, ok := vals[args[1]]
			if !ok {
				return fmt.Errorf("%s not found", args[1])
			}
			fmt.Print(v)
			if !strings.HasSuffix(v, "\n") {
				fmt.Println()
			}
			return nil
		},
	}
}

func (o *secretsOpts) unset() *cobra.Command {
	return &cobra.Command{
		Use:   "unset TARGET KEY...",
		Short: "Remove secrets",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isFile(args[0]) {
				return o.remoteChange(args[0], daemon.SecretsChange{Unset: args[1:]})
			}
			s, err := o.open(args[0], false)
			if err != nil {
				return err
			}
			for _, k := range args[1:] {
				if !s.Delete(k) {
					return fmt.Errorf("%s not found", k)
				}
			}
			return save(args[0], s)
		},
	}
}

func (o *secretsOpts) list() *cobra.Command {
	return &cobra.Command{
		Use:   "ls TARGET",
		Short: "List secret names and recipients (no key needed)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var keys, recipients []string
			if isFile(args[0]) {
				e, err := secrets.ReadFile(args[0])
				if err != nil {
					return err
				}
				keys, recipients = e.Keys(), e.Meta.Recipients
			} else {
				c, err := o.api()
				if err != nil {
					return err
				}
				info, err := c.Secrets(args[0], false)
				if err != nil {
					return err
				}
				keys, recipients = info.Keys, info.Recipients
			}
			for _, k := range keys {
				fmt.Println(k)
			}
			if len(recipients) > 0 {
				fmt.Fprintf(os.Stderr, "\nrecipients:\n  %s\n", strings.Join(recipients, "\n  "))
			}
			return nil
		},
	}
}

func (o *secretsOpts) show() *cobra.Command {
	return &cobra.Command{
		Use:   "show TARGET",
		Short: "Print every decrypted secret as YAML (admin role when remote)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vals, err := o.values(args[0])
			if err != nil {
				return err
			}
			data, err := yaml.Marshal(vals)
			if err != nil {
				return err
			}
			fmt.Print(string(data))
			return nil
		},
	}
}

func (o *secretsOpts) edit() *cobra.Command {
	return &cobra.Command{
		Use:   "edit TARGET",
		Short: "Edit secrets in $EDITOR (decrypted in a private temp file)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			var current map[string]string
			var local *secrets.Secrets
			var err error
			if isFile(target) {
				if local, err = o.open(target, true); err != nil {
					return err
				}
				current = local.Values()
			} else if current, err = o.remoteValues(target); err != nil {
				return err
			}

			edited, changed, err := editValues(current)
			if err != nil || !changed {
				return err
			}
			if local != nil {
				if err := local.Replace(edited); err != nil {
					return err
				}
				if err := save(target, local); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "✔ %s saved\n", target)
				return nil
			}
			ch := daemon.SecretsChange{Set: map[string]string{}}
			for k, v := range edited {
				if old, ok := current[k]; !ok || old != v {
					ch.Set[k] = v
				}
			}
			for k := range current {
				if _, ok := edited[k]; !ok {
					ch.Unset = append(ch.Unset, k)
				}
			}
			sort.Strings(ch.Unset)
			if len(ch.Set) == 0 && len(ch.Unset) == 0 {
				fmt.Fprintln(os.Stderr, "no change")
				return nil
			}
			return o.remoteChange(target, ch)
		},
	}
}

// editValues opens the values in $EDITOR until the content is valid.
func editValues(current map[string]string) (map[string]string, bool, error) {
	plain := []byte("# KEY: value, one per line. Use | for multiline values.\n")
	if len(current) > 0 {
		data, err := yaml.Marshal(current)
		if err != nil {
			return nil, false, err
		}
		plain = append(plain, data...)
	}
	tmpDir := ""
	if st, err := os.Stat("/dev/shm"); err == nil && st.IsDir() {
		tmpDir = "/dev/shm"
	}
	tmp, err := os.CreateTemp(tmpDir, "minato-secrets-*.yml")
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(plain); err != nil {
		_ = tmp.Close()
		return nil, false, err
	}
	if err := tmp.Close(); err != nil {
		return nil, false, err
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	for {
		c := exec.Command("sh", "-c", editor+` "$1"`, "editor", tmp.Name())
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			return nil, false, fmt.Errorf("editor: %w", err)
		}
		edited, err := os.ReadFile(tmp.Name())
		if err != nil {
			return nil, false, err
		}
		if bytes.Equal(edited, plain) {
			fmt.Fprintln(os.Stderr, "no change")
			return nil, false, nil
		}
		values := map[string]string{}
		if err := yaml.Unmarshal(edited, &values); err != nil {
			fmt.Fprintf(os.Stderr, "invalid content: %v\nPress Enter to edit again or Ctrl+C to abort.", err)
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
			continue
		}
		return values, true, nil
	}
}

func (o *secretsOpts) encrypt() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:     "encrypt PLAIN_FILE",
		Short:   "Encrypt a plain KEY: value YAML file (local)",
		Example: `  minato secrets encrypt secrets.yml -o stacks/blog/secrets.enc.yml && shred -u secrets.yml`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			if secrets.IsEncryptedFile(data) {
				return errors.New("file is already encrypted")
			}
			values := map[string]string{}
			if err := yaml.Unmarshal(data, &values); err != nil {
				return fmt.Errorf("expected a flat KEY: value YAML file: %w", err)
			}
			if out == "" {
				out = strings.TrimSuffix(args[0], filepath.Ext(args[0])) + ".enc.yml"
			}
			rs, err := o.recipientsFor(out)
			if err != nil {
				return err
			}
			s, err := secrets.New(rs)
			if err != nil {
				return err
			}
			if err := s.Replace(values); err != nil {
				return err
			}
			if err := save(out, s); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "✔ %d secrets encrypted to %s\n", len(values), out)
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "output file (default <name>.enc.yml)")
	return cmd
}

func (o *secretsOpts) rotate() *cobra.Command {
	var values []string
	var length int
	cmd := &cobra.Command{
		Use:     "rotate TARGET...",
		Aliases: []string{"updatekeys"},
		Short:   "Re-encrypt with a new data key for the current recipients, or regenerate values",
		Long: `Without --value: generates a new data key wrapped for the recipients of
.minato.yml (plus the server key when remote). Run it after adding or
removing a recipient.

With --value KEY: regenerates the value of KEY with a random string.`,
		Example: `  minato secrets rotate blog
  minato secrets rotate blog --value DB_PASSWORD --value HOOK_TOKEN
  minato secrets rotate stacks/*/secrets.enc.yml`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, target := range args {
				if !isFile(target) {
					ch := daemon.SecretsChange{Rotate: len(values) == 0}
					if len(values) > 0 {
						ch.Random = map[string]int{}
						for _, k := range values {
							ch.Random[k] = length
						}
					}
					if err := o.remoteChange(target, ch); err != nil {
						return err
					}
					continue
				}
				s, err := o.open(target, false)
				if err != nil {
					return err
				}
				if len(values) > 0 {
					for _, k := range values {
						if _, ok := s.Get(k); !ok {
							return fmt.Errorf("%s not found in %s", k, target)
						}
						_ = s.Set(k, secrets.RandomValue(length))
					}
				} else {
					rs, err := o.recipientsFor(target)
					if err != nil {
						return err
					}
					if err := s.Rotate(rs); err != nil {
						return err
					}
				}
				if err := save(target, s); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "✔ %s rotated\n", target)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&values, "value", nil, "regenerate this secret value (repeatable)")
	cmd.Flags().IntVar(&length, "length", 32, "length of regenerated values")
	return cmd
}
