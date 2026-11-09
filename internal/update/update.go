// Package update replaces the minato binary with a GitHub release.
package update

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is the GitHub repository publishing the releases.
const Repo = "ermos/minato"

// Updater checks and installs releases. The zero value talks to GitHub.
type Updater struct {
	// APIBase and DownloadBase are overridden in tests.
	APIBase      string
	DownloadBase string
	HTTP         *http.Client
}

func (u *Updater) client() *http.Client {
	if u.HTTP != nil {
		return u.HTTP
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

func (u *Updater) apiBase() string {
	if u.APIBase != "" {
		return strings.TrimSuffix(u.APIBase, "/")
	}
	return "https://api.github.com/repos/" + Repo
}

func (u *Updater) downloadBase() string {
	if u.DownloadBase != "" {
		return strings.TrimSuffix(u.DownloadBase, "/")
	}
	return "https://github.com/" + Repo + "/releases/download"
}

// AssetName is the release binary for this platform.
func AssetName() string { return fmt.Sprintf("minato-%s-%s", runtime.GOOS, runtime.GOARCH) }

// Latest returns the tag of the latest release.
func (u *Updater) Latest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.apiBase()+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := u.client().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return "", errors.New("no release published yet")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("latest release: %s", resp.Status)
	}
	var r struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	if _, err := ParseVersion(r.TagName); err != nil {
		return "", fmt.Errorf("latest release: %w", err)
	}
	return r.TagName, nil
}

func (u *Updater) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// Download fetches the binary of tag for this platform and checks it against
// the checksums.txt of the release.
func (u *Updater) Download(ctx context.Context, tag string) ([]byte, error) {
	base := u.downloadBase() + "/" + tag + "/"
	sums, err := u.get(ctx, base+"checksums.txt", 1<<20)
	if err != nil {
		return nil, err
	}
	want, err := checksum(sums, AssetName())
	if err != nil {
		return nil, err
	}
	bin, err := u.get(ctx, base+AssetName(), 512<<20)
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256(bin)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("checksum mismatch for %s %s", AssetName(), tag)
	}
	return bin, nil
}

func checksum(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("no checksum for %s in the release", name)
}

// Replace writes bin over the executable at path, atomically: a running
// process keeps its old binary until it restarts.
func Replace(path string, bin []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".minato-update-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("cannot write to %s, run with sudo", filepath.Dir(path))
		}
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(bin); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), info.Mode().Perm()|0o111); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Version is a parsed vMAJOR.MINOR.PATCH[-PRE] tag.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

// ParseVersion parses a release tag. Build metadata is ignored.
func ParseVersion(s string) (Version, error) {
	var v Version
	raw := strings.TrimPrefix(strings.TrimSpace(s), "v")
	raw, _, _ = strings.Cut(raw, "+")
	raw, v.Pre, _ = strings.Cut(raw, "-")
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("invalid version %q", s)
	}
	nums := []*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, fmt.Errorf("invalid version %q", s)
		}
		*nums[i] = n
	}
	return v, nil
}

// Less reports whether v is older than o. A pre-release is older than the
// release of the same number.
func (v Version) Less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	if v.Patch != o.Patch {
		return v.Patch < o.Patch
	}
	switch {
	case v.Pre == o.Pre:
		return false
	case v.Pre == "":
		return false
	case o.Pre == "":
		return true
	default:
		return v.Pre < o.Pre
	}
}

// SameMajor reports whether o has the major version of v. Automatic updates
// never cross a major version.
func (v Version) SameMajor(o Version) bool { return v.Major == o.Major }
