// Package render turns a stack from the git repository into a deployable
// folder: secrets are decrypted, variables interpolated, templates rendered,
// and the result is synced to the deploy directory.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"filippo.io/age"

	"github.com/ermos/minato/internal/interp"
	"github.com/ermos/minato/internal/secrets"
	"github.com/ermos/minato/internal/stack"
)

const (
	// MetaDir holds minato generated content inside a deploy folder.
	MetaDir      = ".minato"
	secretsDir   = MetaDir + "/secrets"
	manifestFile = MetaDir + "/manifest"
	tmplSuffix   = ".tmpl"
)

// Options configures a render.
type Options struct {
	// OutDir is the absolute deploy folder. It is exposed as ${stack.dir}.
	OutDir     string
	Identities []age.Identity
	// HostEnv is exposed as ${env.*}.
	HostEnv map[string]string
	// Redact replaces secrets with placeholders and does not need any key.
	// Used for validation and previews.
	Redact bool
	// SharedSecrets is the shared secret file of the repository, empty when
	// there is none. Its values are used unless the stack file has the key.
	SharedSecrets string
}

// File is a rendered file.
type File struct {
	Data []byte
	Mode fs.FileMode
}

// Bundle is a rendered stack, ready to be written and deployed.
type Bundle struct {
	Stack  *stack.Stack
	OutDir string
	// Files maps slash separated relative paths to their content.
	Files map[string]File
	// ComposePath is the relative path of the compose file in Files.
	ComposePath string
	// Env is passed to docker compose / docker stack as process environment.
	Env      map[string]string
	Webhooks map[string]stack.Webhook
	Compose  *Compose
	Hash     string
}

// Build renders st in memory.
func Build(st *stack.Stack, opts Options) (*Bundle, error) {
	if opts.OutDir == "" {
		return nil, fmt.Errorf("render %s: empty output dir", st.Name)
	}
	secretValues, err := loadSecrets(st, opts)
	if err != nil {
		return nil, err
	}

	usedSecretFiles := map[string]bool{}
	ctx := interp.Context{
		"secrets": interp.Map(secretValues),
		"env":     interp.Map(opts.HostEnv),
		"stack": interp.Map(map[string]string{
			"name":    st.Name,
			"project": st.Project(),
			"mode":    st.Spec.Mode,
			"dir":     opts.OutDir,
		}),
		"secretfile": func(key string) (string, bool) {
			if _, ok := secretValues[key]; !ok {
				return "", false
			}
			usedSecretFiles[key] = true
			return filepath.Join(opts.OutDir, filepath.FromSlash(secretsDir), key), true
		},
	}

	var problems []string
	collect := func(where string, err error) {
		if err == nil {
			return
		}
		if ie, ok := err.(*interp.Error); ok {
			for _, p := range ie.Problems {
				problems = append(problems, where+": "+p)
			}
			return
		}
		problems = append(problems, where+": "+err.Error())
	}

	vars, err := ctx.StringMap(st.Spec.Vars, interp.Options{})
	collect(stack.SpecFile+" vars", err)
	ctx["vars"] = interp.Map(vars)
	env, err := ctx.StringMap(st.Spec.Env, interp.Options{})
	collect(stack.SpecFile+" env", err)

	hooks := map[string]stack.Webhook{}
	for name, h := range st.Spec.Webhooks {
		var e1, e2 error
		h.Token, e1 = ctx.String(h.Token, interp.Options{})
		h.Secret, e2 = ctx.String(h.Secret, interp.Options{})
		collect("webhook "+name, e1)
		collect("webhook "+name, e2)
		hooks[name] = h
	}

	b := &Bundle{
		Stack:       st,
		OutDir:      opts.OutDir,
		Files:       map[string]File{},
		ComposePath: filepath.ToSlash(st.Spec.Compose),
		Env:         env,
		Webhooks:    hooks,
	}

	secretsRel := filepath.ToSlash(st.Spec.Secrets)
	err = filepath.WalkDir(st.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(st.Dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (d.Name() == ".git" || rel == MetaDir) {
				return filepath.SkipDir
			}
			return nil
		}
		// Symlinks are skipped on purpose: they could point outside the repo.
		if !d.Type().IsRegular() || rel == stack.SpecFile || rel == secretsRel {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		switch {
		case rel == b.ComposePath:
			// Substituted values are escaped so compose does not interpolate
			// them a second time.
			out, err := ctx.String(string(data), interp.Options{EscapeDollar: true})
			collect(rel, err)
			data = []byte(out)
		case st.Templates() && strings.HasSuffix(rel, tmplSuffix):
			out, err := ctx.String(string(data), interp.Options{})
			collect(rel, err)
			data = []byte(out)
			rel = strings.TrimSuffix(rel, tmplSuffix)
		}
		b.Files[rel] = File{Data: data, Mode: mode}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", st.Name, err)
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("render %s:\n  %s", st.Name, strings.Join(problems, "\n  "))
	}

	for key := range usedSecretFiles {
		b.Files[secretsDir+"/"+key] = File{Data: []byte(secretValues[key]), Mode: 0o444}
	}

	compose, err := ParseCompose(b.Files[b.ComposePath].Data, env)
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", st.Name, err)
	}
	b.Compose = compose
	for svc := range st.Spec.Deploy.Rollout {
		if err := compose.CheckRollout(svc); err != nil {
			return nil, fmt.Errorf("render %s: %w", st.Name, err)
		}
	}
	if st.Spec.Mode == stack.ModeSwarm {
		changed, err := compose.VersionSwarmObjects(st.Project(), b.readFile)
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", st.Name, err)
		}
		if changed {
			data, err := compose.Marshal()
			if err != nil {
				return nil, err
			}
			b.Files[b.ComposePath] = File{Data: data, Mode: 0o644}
		}
	}
	b.Hash = b.computeHash()
	return b, nil
}

// loadSecrets returns the shared secrets of the repository overlaid with the
// secrets of the stack.
func loadSecrets(st *stack.Stack, opts Options) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range []string{opts.SharedSecrets, st.SecretsPath()} {
		if p == "" {
			continue
		}
		values, err := readSecretFile(st, p, opts)
		if err != nil {
			return nil, err
		}
		for k, v := range values {
			out[k] = v
		}
	}
	return out, nil
}

func readSecretFile(st *stack.Stack, p string, opts Options) (map[string]string, error) {
	if opts.Redact {
		e, err := secrets.ReadFile(p)
		if err != nil {
			return nil, err
		}
		out := map[string]string{}
		for _, k := range e.Keys() {
			out[k] = "<secret:" + k + ">"
		}
		return out, nil
	}
	if len(opts.Identities) == 0 {
		return nil, fmt.Errorf("stack %s has secrets but no age identity is available", st.Name)
	}
	values, err := secrets.DecryptFile(p, opts.Identities)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
	}
	return values, nil
}

// readFile returns the content of a file referenced by the compose file,
// looking in the bundle first.
func (b *Bundle) readFile(p string) ([]byte, error) {
	if rel, ok := b.relPath(p); ok {
		if f, ok := b.Files[rel]; ok {
			return f.Data, nil
		}
	}
	if !filepath.IsAbs(p) {
		return nil, fmt.Errorf("file %s not found in stack", p)
	}
	return os.ReadFile(p)
}

// relPath converts a compose path (relative to the compose file or absolute
// inside OutDir) to a bundle path.
func (b *Bundle) relPath(p string) (string, bool) {
	if filepath.IsAbs(p) {
		rel, err := filepath.Rel(b.OutDir, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", false
		}
		return filepath.ToSlash(rel), true
	}
	rel := path.Clean(path.Join(path.Dir(b.ComposePath), filepath.ToSlash(p)))
	if strings.HasPrefix(rel, "..") {
		return "", false
	}
	return rel, true
}

func (b *Bundle) computeHash() string {
	h := sha256.New()
	fmt.Fprintf(h, "mode=%s\nproject=%s\n", b.Stack.Spec.Mode, b.Stack.Project())
	for _, k := range sortedKeys(b.Env) {
		fmt.Fprintf(h, "env %s=%s\n", k, b.Env[k])
	}
	paths := make([]string, 0, len(b.Files))
	for p := range b.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		f := b.Files[p]
		sum := sha256.Sum256(f.Data)
		fmt.Fprintf(h, "file %s %o %x\n", p, f.Mode, sum)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ServicesUsing returns the services that mount or reference one of the
// given bundle paths.
func (b *Bundle) ServicesUsing(paths []string) []string {
	set := map[string]bool{}
	for svc, sources := range b.Compose.Mounts {
		for _, src := range sources {
			rel, ok := b.relPath(src)
			if !ok {
				continue
			}
			for _, p := range paths {
				if rel == "." || p == rel || strings.HasPrefix(p, rel+"/") {
					set[svc] = true
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
