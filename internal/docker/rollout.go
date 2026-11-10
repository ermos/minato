package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ermos/minato/internal/render"
	"github.com/ermos/minato/internal/stack"
)

// stableFor is how long a container without healthcheck nor probe must stay
// up to be considered ready.
const stableFor = 10 * time.Second

// splitRollout separates the targeted services updated with a rolling deploy
// from the others. Only actions creating containers roll out.
func splitRollout(b *render.Bundle, action Action, services []string) (roll, rest []string) {
	if len(b.Stack.Spec.Deploy.Rollout) == 0 || b.Stack.Spec.Mode != stack.ModeCompose {
		return nil, services
	}
	if action != ActionDeploy && action != ActionPull && action != ActionRecreate {
		return nil, services
	}
	if len(services) == 0 {
		services = b.Compose.Services()
	}
	for _, s := range services {
		if _, ok := b.Stack.Spec.Deploy.Rollout[s]; ok {
			roll = append(roll, s)
		} else {
			rest = append(rest, s)
		}
	}
	return roll, rest
}

// composeRollout deploys rest the usual way, then rolls out each service of
// roll.
func (c *Client) composeRollout(ctx context.Context, b *render.Bundle, action Action, roll, rest []string, out io.Writer) error {
	if action == ActionDeploy {
		if err := c.ensureNetworks(ctx, b, out); err != nil {
			return err
		}
	}
	if len(rest) > 0 {
		// --no-deps: a dependency in roll must not be recreated in place.
		args := append(append(composeBase(b), composeArgs(b, action)...), "--no-deps")
		if err := c.exec(ctx, b, out, append(args, rest...)...); err != nil {
			return err
		}
	}
	for _, svc := range roll {
		if err := c.rollout(ctx, b, action, svc, out); err != nil {
			return err
		}
	}
	return nil
}

// rollout starts new containers of svc next to the running ones, waits for
// them to be ready, then removes the old ones. When the new containers fail,
// they are removed and the old ones keep running.
func (c *Client) rollout(ctx context.Context, b *render.Bundle, action Action, svc string, out io.Writer) error {
	base := composeBase(b)
	pull := b.Stack.Spec.Deploy.Pull
	if action == ActionPull {
		pull = "always"
	}
	if pull == "always" {
		if err := c.exec(ctx, b, out, append(base, "pull", svc)...); err != nil {
			return err
		}
		pull = "never"
	}

	all, err := c.serviceContainers(ctx, b, svc)
	if err != nil {
		return err
	}
	var old, stopped []string
	for _, ct := range all {
		if ct.State.Running {
			old = append(old, ct.ID)
		} else {
			stopped = append(stopped, ct.ID)
		}
	}
	if len(old) == 0 {
		// Nothing serves yet: a plain deploy has no downtime to avoid.
		args := append(base, "up", "-d", "--no-deps", "--pull", pull)
		if action == ActionRecreate {
			args = append(args, "--force-recreate")
		}
		return c.exec(ctx, b, out, append(args, svc)...)
	}
	if action != ActionRecreate {
		fresh, err := c.upToDate(ctx, b, svc, all)
		if err != nil {
			return err
		}
		if fresh {
			return nil
		}
	}
	if len(stopped) > 0 {
		if err := c.exec(ctx, nil, out, append([]string{"rm", "-f"}, stopped...)...); err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "rolling out %s: starting %d new container(s)\n", svc, len(old))
	if err := c.exec(ctx, b, out, append(base, "up", "-d", "--no-deps", "--no-recreate", "--pull", pull,
		"--scale", svc+"="+strconv.Itoa(2*len(old)), svc)...); err != nil {
		return err
	}
	now, err := c.serviceContainers(ctx, b, svc)
	if err != nil {
		return err
	}
	var fresh []string
	for _, ct := range now {
		if !slices.Contains(old, ct.ID) {
			fresh = append(fresh, ct.ID)
		}
	}
	if len(fresh) == 0 {
		return fmt.Errorf("rollout of %s: no new container was started", svc)
	}

	if err := c.waitReady(ctx, fresh, b.Stack.Spec.Deploy.Rollout[svc], out); err != nil {
		_ = c.exec(ctx, nil, out, "logs", "--tail", "30", fresh[0])
		_ = c.exec(context.WithoutCancel(ctx), nil, out, append([]string{"rm", "-f"}, fresh...)...)
		return fmt.Errorf("rollout of %s failed, the previous version keeps running: %w", svc, err)
	}
	if d := b.Stack.Spec.Deploy.Rollout[svc].DrainDuration(); d > 0 {
		fmt.Fprintf(out, "%s ready, stopping the previous version in %s\n", svc, d)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
	if err := c.exec(ctx, nil, out, append([]string{"stop"}, old...)...); err != nil {
		return err
	}
	if err := c.exec(ctx, nil, out, append([]string{"rm"}, old...)...); err != nil {
		return err
	}
	fmt.Fprintf(out, "✔ %s rolled out\n", svc)
	return nil
}

// container is the part of docker inspect used by rollouts.
type container struct {
	ID     string `json:"Id"`
	Image  string `json:"Image"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status    string    `json:"Status"`
		Running   bool      `json:"Running"`
		StartedAt time.Time `json:"StartedAt"`
		Health    *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

func (c *Client) serviceContainers(ctx context.Context, b *render.Bundle, svc string) ([]container, error) {
	var buf bytes.Buffer
	if err := c.exec(ctx, b, &buf, append(composeBase(b), "ps", "-a", "-q", svc)...); err != nil {
		return nil, err
	}
	return c.inspect(ctx, strings.Fields(buf.String()))
}

func (c *Client) inspect(ctx context.Context, ids []string) ([]container, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var buf bytes.Buffer
	if err := c.exec(ctx, nil, &buf, append([]string{"inspect"}, ids...)...); err != nil {
		return nil, err
	}
	var cts []container
	if err := json.Unmarshal(buf.Bytes(), &cts); err != nil {
		return nil, fmt.Errorf("docker inspect: %w", err)
	}
	return cts, nil
}

// upToDate reports whether the containers of svc already run the current
// configuration and image, like docker compose decides to recreate them.
func (c *Client) upToDate(ctx context.Context, b *render.Bundle, svc string, cts []container) (bool, error) {
	var buf bytes.Buffer
	if err := c.exec(ctx, b, &buf, append(composeBase(b), "config", "--hash", svc)...); err != nil {
		return false, err
	}
	fields := strings.Fields(buf.String())
	if len(fields) != 2 {
		return false, fmt.Errorf("docker compose config --hash %s: unexpected output %q", svc, buf.String())
	}
	imageID := ""
	if img := b.Compose.Images[svc]; img != "" {
		var id bytes.Buffer
		if err := c.exec(ctx, nil, &id, "image", "inspect", "--format", "{{.Id}}", img); err != nil {
			return false, nil // image missing locally: it changes
		}
		imageID = strings.TrimSpace(id.String())
	}
	for _, ct := range cts {
		if ct.Config.Labels["com.docker.compose.config-hash"] != fields[1] {
			return false, nil
		}
		if imageID != "" && ct.Image != imageID {
			return false, nil
		}
	}
	return true, nil
}

func (c *Client) waitReady(ctx context.Context, ids []string, r stack.Rollout, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, r.TimeoutDuration())
	defer cancel()
	client := &http.Client{Timeout: 3 * time.Second}
	probe := func(url string) bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false
		}
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode >= 200 && resp.StatusCode < 300
	}
	var waiting string
	for {
		cts, err := c.inspect(ctx, ids)
		if err != nil && ctx.Err() == nil {
			return err
		}
		ready := len(cts) == len(ids)
		for _, ct := range cts {
			ok, why, err := readiness(ct, r, time.Now(), probe)
			if err != nil {
				return err
			}
			if !ok {
				ready, waiting = false, why
			}
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			if waiting == "" {
				waiting = ctx.Err().Error()
			}
			return fmt.Errorf("not ready after %s: %s", r.TimeoutDuration(), waiting)
		case <-time.After(2 * time.Second):
		}
	}
}

// readiness tells whether a new container is ready. err means it will never
// be. probe performs an HTTP GET and reports a 2xx answer.
func readiness(ct container, r stack.Rollout, now time.Time, probe func(url string) bool) (ok bool, why string, err error) {
	if !ct.State.Running {
		return false, "", fmt.Errorf("the new container is %s", ct.State.Status)
	}
	if r.Probe != nil {
		ip := containerIP(ct)
		if ip == "" {
			return false, "", errors.New("the new container has no IP address to probe")
		}
		url := fmt.Sprintf("http://%s:%d%s", ip, r.Probe.Port, r.Probe.Path)
		if probe(url) {
			return true, "", nil
		}
		return false, "GET " + url + " did not answer 2xx", nil
	}
	if h := ct.State.Health; h != nil {
		switch h.Status {
		case "healthy":
			return true, "", nil
		case "unhealthy":
			return false, "", errors.New("the new container is unhealthy")
		}
		return false, "the healthcheck is " + h.Status, nil
	}
	if now.Sub(ct.State.StartedAt) >= stableFor {
		return true, "", nil
	}
	return false, "waiting for the container to stay up", nil
}

func containerIP(ct container) string {
	names := make([]string, 0, len(ct.NetworkSettings.Networks))
	for n := range ct.NetworkSettings.Networks {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if ip := ct.NetworkSettings.Networks[n].IPAddress; ip != "" {
			return ip
		}
	}
	return ""
}
