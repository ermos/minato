package docker

import (
	"strings"
	"testing"
	"time"

	"github.com/ermos/minato/internal/stack"
)

func TestReadiness(t *testing.T) {
	now := time.Now()
	running := func(started time.Duration, health string) container {
		var ct container
		ct.State.Running, ct.State.Status, ct.State.StartedAt = true, "running", now.Add(-started)
		if health != "" {
			ct.State.Health = &struct {
				Status string `json:"Status"`
			}{health}
		}
		ct.NetworkSettings.Networks = map[string]struct {
			IPAddress string `json:"IPAddress"`
		}{"web": {"172.18.0.5"}}
		return ct
	}
	var probed string
	probe := func(ok bool) func(string) bool {
		return func(url string) bool { probed = url; return ok }
	}
	withProbe := stack.Rollout{Probe: &stack.Probe{Port: 8787, Path: "/api/health"}}

	exited := running(time.Minute, "")
	exited.State.Running, exited.State.Status = false, "exited"

	for _, c := range []struct {
		name    string
		ct      container
		r       stack.Rollout
		probeOK bool
		ready   bool
		fatal   string
	}{
		{"probe answers", running(time.Second, ""), withProbe, true, true, ""},
		{"probe not yet", running(time.Second, ""), withProbe, false, false, ""},
		{"probe wins over healthcheck", running(time.Second, "starting"), withProbe, true, true, ""},
		{"healthy", running(time.Second, "healthy"), stack.Rollout{}, false, true, ""},
		{"health starting", running(time.Minute, "starting"), stack.Rollout{}, false, false, ""},
		{"unhealthy", running(time.Minute, "unhealthy"), stack.Rollout{}, false, false, "unhealthy"},
		{"no check, too recent", running(3*time.Second, ""), stack.Rollout{}, false, false, ""},
		{"no check, stable", running(stableFor, ""), stack.Rollout{}, false, true, ""},
		{"exited", exited, withProbe, true, false, "exited"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ok, _, err := readiness(c.ct, c.r, now, probe(c.probeOK))
			if c.fatal != "" {
				if err == nil || !strings.Contains(err.Error(), c.fatal) {
					t.Fatalf("expected %q, got %v", c.fatal, err)
				}
				return
			}
			if err != nil || ok != c.ready {
				t.Fatalf("ready = %v, %v, want %v", ok, err, c.ready)
			}
		})
	}
	if probed != "http://172.18.0.5:8787/api/health" {
		t.Errorf("probed %s", probed)
	}
}
