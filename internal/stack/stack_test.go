package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadSpec(t *testing.T, spec string) (*Stack, error) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "compose.yml"), []byte("services:\n  api:\n    image: app\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, SpecFile), []byte(spec), 0o644)
	return Load(dir)
}

func TestRolloutSettings(t *testing.T) {
	st, err := loadSpec(t, "deploy:\n  rollout:\n    api:\n      probe: {port: 8787}\n    worker: {timeout: 30s}\n")
	if err != nil {
		t.Fatal(err)
	}
	api, worker := st.Spec.Deploy.Rollout["api"], st.Spec.Deploy.Rollout["worker"]
	if api.TimeoutDuration() != 2*time.Minute || api.Probe.Path != "/" {
		t.Errorf("api defaults: timeout %s, path %q", api.TimeoutDuration(), api.Probe.Path)
	}
	if api.DrainDuration() != 5*time.Second {
		t.Errorf("api drain = %s", api.DrainDuration())
	}
	if worker.TimeoutDuration() != 30*time.Second {
		t.Errorf("worker timeout = %s", worker.TimeoutDuration())
	}

	for spec, want := range map[string]string{
		"mode: swarm\ndeploy:\n  rollout:\n    api: {}\n":                    "compose mode",
		"deploy:\n  rollout:\n    api: {timeout: soon}\n":                    "invalid duration",
		"deploy:\n  rollout:\n    api:\n      probe: {port: 0}\n":            "invalid port",
		"deploy:\n  rollout:\n    api:\n      probe: {port: 80, path: up}\n": "must start with /",
	} {
		if _, err := loadSpec(t, spec); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: expected %q, got %v", spec, want, err)
		}
	}
}
