// Package gitsync keeps a local checkout in sync with a remote branch.
package gitsync

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Repo is a git repository mirrored locally.
type Repo struct {
	URL    string
	Branch string
	Dir    string
	SSHKey string
}

// Sync clones or hard resets the checkout to the remote branch and returns
// the current commit. Without URL, Dir is used as is.
func (r *Repo) Sync(ctx context.Context) (string, error) {
	if r.URL == "" {
		rev, err := r.git(ctx, r.Dir, "rev-parse", "HEAD")
		if err != nil {
			return "local", nil
		}
		return rev, nil
	}
	if _, err := os.Stat(filepath.Join(r.Dir, ".git")); err != nil {
		if err := os.MkdirAll(filepath.Dir(r.Dir), 0o700); err != nil {
			return "", err
		}
		_ = os.RemoveAll(r.Dir)
		if _, err := r.git(ctx, "", "clone", "--depth", "1", "--branch", r.Branch, "--single-branch", r.URL, r.Dir); err != nil {
			return "", err
		}
	} else {
		if _, err := r.git(ctx, r.Dir, "remote", "set-url", "origin", r.URL); err != nil {
			return "", err
		}
		if _, err := r.git(ctx, r.Dir, "fetch", "--depth", "1", "--prune", "origin", r.Branch); err != nil {
			return "", err
		}
		if _, err := r.git(ctx, r.Dir, "reset", "--hard", "FETCH_HEAD"); err != nil {
			return "", err
		}
		if _, err := r.git(ctx, r.Dir, "clean", "-fdx"); err != nil {
			return "", err
		}
	}
	return r.git(ctx, r.Dir, "rev-parse", "HEAD")
}

var credentials = regexp.MustCompile(`://[^/@\s]+@`)

func (r *Repo) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if r.SSHKey != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf(
			"GIT_SSH_COMMAND=ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new", r.SSHKey))
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := credentials.ReplaceAllString(strings.TrimSpace(stderr.String()), "://***@")
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// CommitAndPush commits path (relative to the checkout) and pushes it to the
// tracked branch. It returns the new HEAD. Without a git checkout the file is
// only written.
func (r *Repo) CommitAndPush(ctx context.Context, path, message, authorName, authorEmail string) (string, error) {
	if _, err := os.Stat(filepath.Join(r.Dir, ".git")); err != nil {
		return "local", nil
	}
	if _, err := r.git(ctx, r.Dir, "add", "--", path); err != nil {
		return "", err
	}
	if _, err := r.git(ctx, r.Dir, "diff", "--cached", "--quiet"); err == nil {
		return r.git(ctx, r.Dir, "rev-parse", "HEAD")
	}
	host, _ := os.Hostname()
	if _, err := r.git(ctx, r.Dir, "-c", "user.name=minato", "-c", "user.email=minato@"+host,
		"commit", "--author", fmt.Sprintf("%s <%s>", authorName, authorEmail), "-m", message); err != nil {
		return "", err
	}
	if r.URL != "" {
		if _, err := r.git(ctx, r.Dir, "push", "origin", "HEAD:refs/heads/"+r.Branch); err != nil {
			return "", fmt.Errorf("%w (the deploy key needs write access to the repository)", err)
		}
	}
	return r.git(ctx, r.Dir, "rev-parse", "HEAD")
}
