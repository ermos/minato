// Package docker drives docker compose and docker swarm through the docker CLI.
package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ermos/minato/internal/render"
	"github.com/ermos/minato/internal/stack"
)

// Action is an operation on a deployed stack.
type Action string

const (
	ActionDeploy   Action = "deploy"
	ActionPull     Action = "pull"
	ActionRestart  Action = "restart"
	ActionRecreate Action = "recreate"
	ActionDown     Action = "down"
)

// ParseAction validates an action name.
func ParseAction(s string) (Action, error) {
	switch a := Action(s); a {
	case ActionDeploy, ActionPull, ActionRestart, ActionRecreate, ActionDown:
		return a, nil
	}
	return "", fmt.Errorf("unknown action %q (deploy, pull, restart, recreate, down)", s)
}

// Client runs docker commands.
type Client struct {
	Bin string
}

// Run executes an action on a rendered bundle. Output is written to out.
func (c *Client) Run(ctx context.Context, b *render.Bundle, action Action, services []string, out io.Writer) error {
	if err := checkServices(b, services); err != nil {
		return err
	}
	if b.Stack.Spec.Mode == stack.ModeSwarm {
		return c.runSwarm(ctx, b, action, services, out)
	}
	return c.runCompose(ctx, b, action, services, out)
}

func checkServices(b *render.Bundle, services []string) error {
	known := map[string]bool{}
	for _, s := range b.Compose.Services() {
		known[s] = true
	}
	for _, s := range services {
		if !known[s] {
			return fmt.Errorf("unknown service %q in stack %s", s, b.Stack.Name)
		}
	}
	return nil
}

func (c *Client) runCompose(ctx context.Context, b *render.Bundle, action Action, services []string, out io.Writer) error {
	base := []string{"compose", "-p", b.Stack.Project(), "-f", filepath.Join(b.OutDir, b.ComposePath)}
	var args []string
	switch action {
	case ActionDeploy:
		if err := c.ensureNetworks(ctx, b, out); err != nil {
			return err
		}
		args = append(base, "up", "-d", "--remove-orphans", "--pull", b.Stack.Spec.Deploy.Pull)
		if b.Stack.Spec.Deploy.Wait {
			args = append(args, "--wait")
		}
	case ActionPull:
		args = append(base, "up", "-d", "--pull", "always")
		if b.Stack.Spec.Deploy.Wait {
			args = append(args, "--wait")
		}
	case ActionRestart:
		args = append(base, "restart")
	case ActionRecreate:
		args = append(base, "up", "-d", "--force-recreate")
	case ActionDown:
		args = append(base, "down", "--remove-orphans")
	}
	if action != ActionDown {
		args = append(args, services...)
	}
	return c.exec(ctx, b, out, args...)
}

func (c *Client) runSwarm(ctx context.Context, b *render.Bundle, action Action, services []string, out io.Writer) error {
	project := b.Stack.Project()
	deploy := func(resolve string) error {
		if err := c.ensureNetworks(ctx, b, out); err != nil {
			return err
		}
		args := []string{"stack", "deploy", "--prune", "--with-registry-auth", "--resolve-image", resolve,
			"-c", filepath.Join(b.OutDir, b.ComposePath)}
		if b.Stack.Spec.Deploy.Wait {
			args = append(args, "--detach=false")
		} else {
			args = append(args, "--detach=true")
		}
		return c.exec(ctx, b, out, append(args, project)...)
	}
	whole := len(services) == 0
	if whole {
		services = b.Compose.Services()
	}
	switch action {
	case ActionDeploy:
		resolve := map[string]string{"always": "always", "missing": "changed", "never": "never"}[b.Stack.Spec.Deploy.Pull]
		if err := deploy(resolve); err != nil {
			return err
		}
		c.pruneSwarmObjects(ctx, b, out)
		return nil
	case ActionPull:
		if whole {
			return deploy("always")
		}
		for _, svc := range services {
			img := b.Compose.Images[svc]
			if img == "" {
				continue
			}
			if err := c.exec(ctx, b, out, "service", "update", "--with-registry-auth", "--detach=true",
				"--image", img, project+"_"+svc); err != nil {
				return err
			}
		}
		return nil
	case ActionRestart, ActionRecreate:
		for _, svc := range services {
			if err := c.exec(ctx, b, out, "service", "update", "--force", "--detach=true", project+"_"+svc); err != nil {
				return err
			}
		}
		return nil
	case ActionDown:
		return c.exec(ctx, b, out, "stack", "rm", project)
	}
	return nil
}

// ensureNetworks creates external networks referenced by the stack.
func (c *Client) ensureNetworks(ctx context.Context, b *render.Bundle, out io.Writer) error {
	for _, n := range b.Compose.ExternalNetworks {
		if err := c.exec(ctx, nil, io.Discard, "network", "inspect", n); err == nil {
			continue
		}
		args := []string{"network", "create"}
		if b.Stack.Spec.Mode == stack.ModeSwarm {
			args = append(args, "--driver", "overlay", "--attachable")
		}
		fmt.Fprintf(out, "creating network %s\n", n)
		if err := c.exec(ctx, nil, out, append(args, n)...); err != nil {
			return err
		}
	}
	return nil
}

// pruneSwarmObjects removes old versions of configs and secrets. Objects still
// in use are refused by docker and kept.
func (c *Client) pruneSwarmObjects(ctx context.Context, b *render.Bundle, out io.Writer) {
	label := "label=com.docker.stack.namespace=" + b.Stack.Project()
	current := string(b.Files[b.ComposePath].Data)
	for _, kind := range []string{"config", "secret"} {
		var buf bytes.Buffer
		if err := c.exec(ctx, nil, &buf, kind, "ls", "--filter", label, "--format", "{{.Name}}"); err != nil {
			continue
		}
		for _, name := range strings.Fields(buf.String()) {
			if strings.Contains(current, name) {
				continue
			}
			_ = c.exec(ctx, nil, io.Discard, kind, "rm", name)
		}
	}
}

// Exec runs an arbitrary docker command for a stack with its environment.
func (c *Client) Exec(ctx context.Context, b *render.Bundle, out io.Writer, args ...string) error {
	return c.exec(ctx, b, out, args...)
}

func (c *Client) exec(ctx context.Context, b *render.Bundle, out io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.Env = os.Environ()
	if b != nil {
		cmd.Dir = b.OutDir
		cmd.Env = append(cmd.Env, envList(b.Env)...)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w", strings.Join(args[:min(3, len(args))], " "), err)
	}
	return nil
}

func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
