package render

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"filippo.io/age"

	"github.com/ermos/minato/internal/secrets"
	"github.com/ermos/minato/internal/stack"
)

func writeStack(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "app")
	for p, c := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func encryptSecrets(t *testing.T, dir string, values map[string]string) *age.X25519Identity {
	t.Helper()
	id, _ := age.GenerateX25519Identity()
	s, _ := secrets.New([]string{id.Recipient().String()})
	for k, v := range values {
		_ = s.Set(k, v)
	}
	data, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, stack.DefaultSecretsFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return id
}

const composeYML = `services:
  web:
    image: nginx:${TAG:-1.27}
    environment:
      DB_PASSWORD: ${secrets.DB_PASSWORD}
    volumes:
      - ./files/nginx.conf:/etc/nginx/nginx.conf:ro
      - ./data:/data
    secrets: [db]
  worker:
    image: busybox
secrets:
  db:
    file: ${secretfile.DB_PASSWORD}
`

func TestBuildAndWrite(t *testing.T) {
	dir := writeStack(t, map[string]string{
		"compose.yml":           composeYML,
		"minato.yml":            "vars:\n  HOST: ${env.HOSTNAME}.example.com\nenv:\n  TAG: \"1.27-${vars.HOST}\"\n",
		"files/nginx.conf.tmpl": "server_name ${vars.HOST}; # $host stays\n",
		"files/static.txt":      "hello",
	})
	id := encryptSecrets(t, dir, map[string]string{"DB_PASSWORD": "pa$$word"})
	st, err := stack.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "deploy")
	opts := Options{OutDir: out, Identities: []age.Identity{id}, HostEnv: map[string]string{"HOSTNAME": "vps1"}}
	b, err := Build(st, opts)
	if err != nil {
		t.Fatal(err)
	}

	compose := string(b.Files["compose.yml"].Data)
	if !strings.Contains(compose, "DB_PASSWORD: pa$$$$word") {
		t.Fatalf("secret must be dollar escaped for compose:\n%s", compose)
	}
	if !strings.Contains(compose, filepath.Join(out, ".minato/secrets/DB_PASSWORD")) {
		t.Fatalf("secretfile path missing:\n%s", compose)
	}
	if got := string(b.Files["files/nginx.conf"].Data); got != "server_name vps1.example.com; # $host stays\n" {
		t.Fatalf("template not rendered: %q", got)
	}
	if string(b.Files[".minato/secrets/DB_PASSWORD"].Data) != "pa$$word" {
		t.Fatal("secret file content")
	}
	if _, ok := b.Files["minato.yml"]; ok {
		t.Fatal("minato.yml must not be deployed")
	}
	if b.Env["TAG"] != "1.27-vps1.example.com" {
		t.Fatalf("env: %v", b.Env)
	}
	if !strings.HasPrefix(b.Compose.Images["web"], "nginx:1.27-vps1") {
		t.Fatalf("image not expanded: %v", b.Compose.Images)
	}

	res, err := b.Write()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Fresh {
		t.Fatal("first write must be fresh")
	}
	// Runtime data written by a container must survive.
	_ = os.MkdirAll(filepath.Join(out, "data"), 0o755)
	_ = os.WriteFile(filepath.Join(out, "data", "db"), []byte("x"), 0o644)

	confPath := filepath.Join(out, "files", "nginx.conf")
	before := inode(t, confPath)

	_ = os.WriteFile(filepath.Join(dir, "files", "nginx.conf.tmpl"), []byte("changed\n"), 0o644)
	_ = os.Remove(filepath.Join(dir, "files", "static.txt"))
	b2, err := Build(st, opts)
	if err != nil {
		t.Fatal(err)
	}
	if b2.Hash == b.Hash {
		t.Fatal("hash must change")
	}
	res, err = b2.Write()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Changed, ",") != "files/nginx.conf,files/static.txt" {
		t.Fatalf("changed: %v", res.Changed)
	}
	if inode(t, confPath) != before {
		t.Fatal("file must be rewritten in place to keep bind mounts working")
	}
	if _, err := os.Stat(filepath.Join(out, "data", "db")); err != nil {
		t.Fatal("runtime data must not be removed")
	}
	if got := b2.ServicesUsing(res.Changed); strings.Join(got, ",") != "web" {
		t.Fatalf("services using: %v", got)
	}
	if got := b2.ServicesUsing([]string{".minato/secrets/DB_PASSWORD"}); strings.Join(got, ",") != "web" {
		t.Fatalf("services using secret: %v", got)
	}
}

func TestSwarmConfigsAreVersioned(t *testing.T) {
	dir := writeStack(t, map[string]string{
		"minato.yml":   "mode: swarm\n",
		"compose.yml":  "services:\n  web:\n    image: nginx\n    configs: [conf]\nconfigs:\n  conf:\n    file: ./files/a.conf\n",
		"files/a.conf": "v1",
	})
	st, _ := stack.Load(dir)
	out := t.TempDir()
	b, err := Build(st, Options{OutDir: out})
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "files", "a.conf"), []byte("v2"), 0o644)
	b2, _ := Build(st, Options{OutDir: out})
	n1, n2 := configName(t, b), configName(t, b2)
	if !strings.HasPrefix(n1, "app_conf_") || n1 == n2 {
		t.Fatalf("names must be content based: %s %s", n1, n2)
	}
}

func TestMissingReferencesAreReported(t *testing.T) {
	dir := writeStack(t, map[string]string{
		"compose.yml": "services:\n  web:\n    image: ${vars.IMAGE}\n",
	})
	st, _ := stack.Load(dir)
	_, err := Build(st, Options{OutDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "${vars.IMAGE}: not found") {
		t.Fatalf("expected missing var error, got %v", err)
	}
}

func configName(t *testing.T, b *Bundle) string {
	t.Helper()
	c, _ := ParseCompose(b.Files["compose.yml"].Data, nil)
	return asMap(asMap(c.raw["configs"])["conf"])["name"].(string)
}

func inode(t *testing.T, p string) uint64 {
	t.Helper()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Ino
}

func TestSharedSecrets(t *testing.T) {
	dir := writeStack(t, map[string]string{
		"compose.yml": "services:\n  web:\n    image: nginx\n    environment:\n      DB: ${secrets.DB_PASSWORD}\n      TOKEN: ${secrets.TOKEN}\n    secrets: [db]\nsecrets:\n  db:\n    file: ${secretfile.DB_PASSWORD}\n",
	})
	stackID := encryptSecrets(t, dir, map[string]string{"TOKEN": "from-stack"})
	sharedDir := t.TempDir()
	sharedID := encryptSecrets(t, sharedDir, map[string]string{"DB_PASSWORD": "shared-pg", "TOKEN": "from-shared"})
	shared := filepath.Join(sharedDir, stack.DefaultSecretsFile)

	st, _ := stack.Load(dir)
	out := t.TempDir()
	b, err := Build(st, Options{OutDir: out, Identities: []age.Identity{stackID, sharedID}, SharedSecrets: shared})
	if err != nil {
		t.Fatal(err)
	}
	compose := string(b.Files[b.ComposePath].Data)
	if !strings.Contains(compose, "DB: shared-pg") {
		t.Errorf("shared secret not used:\n%s", compose)
	}
	if !strings.Contains(compose, "TOKEN: from-stack") {
		t.Errorf("the stack value must win over the shared one:\n%s", compose)
	}
	if got := string(b.Files[".minato/secrets/DB_PASSWORD"].Data); got != "shared-pg" {
		t.Errorf("secretfile of a shared secret = %q", got)
	}

	// Validation needs no key and sees the shared keys.
	if _, err := Build(st, Options{OutDir: out, Redact: true, SharedSecrets: shared}); err != nil {
		t.Fatalf("redacted build: %v", err)
	}
}

func TestRolloutRejectsPinnedServices(t *testing.T) {
	for _, c := range []struct {
		name, service, want string
	}{
		{"free", "  api:\n    image: app\n    ports: [\"8787\"]\n", ""},
		{"container name", "  api:\n    image: app\n    container_name: api\n", "has container_name"},
		{"host port", "  api:\n    image: app\n    ports: [\"8080:8787\"]\n", "published host port"},
		{"long syntax", "  api:\n    image: app\n    ports:\n      - target: 8787\n        published: 8080\n", "published host port"},
		{"unknown", "  web:\n    image: app\n", "unknown service \"api\""},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := writeStack(t, map[string]string{
				"compose.yml": "services:\n" + c.service,
				"minato.yml":  "deploy:\n  rollout:\n    api: {}\n",
			})
			st, err := stack.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Build(st, Options{OutDir: t.TempDir()})
			if c.want == "" && err != nil {
				t.Fatal(err)
			}
			if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Fatalf("expected %q, got %v", c.want, err)
			}
		})
	}
}
