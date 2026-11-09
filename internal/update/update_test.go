package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionOrdering(t *testing.T) {
	for _, c := range []struct {
		a, b string
		less bool
	}{
		{"v0.1.0", "v0.2.0", true},
		{"v0.2.0", "v0.10.0", true},
		{"v1.2.3", "v1.2.3", false},
		{"v1.2.3", "v1.2.2", false},
		{"v1.0.0-rc.1", "v1.0.0", true},
		{"v1.0.0", "v1.0.0-rc.1", false},
		{"0.9.9", "v1.0.0", true},
	} {
		a, err := ParseVersion(c.a)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := ParseVersion(c.b)
		if got := a.Less(b); got != c.less {
			t.Errorf("%s < %s = %v, want %v", c.a, c.b, got, c.less)
		}
	}
	if _, err := ParseVersion("dev"); err == nil {
		t.Error("dev must not parse as a version")
	}
	a, _ := ParseVersion("v0.9.0")
	b, _ := ParseVersion("v1.0.0")
	if a.SameMajor(b) {
		t.Error("v0 to v1 crosses a major version")
	}
}

// fakeGitHub serves a release whose checksum file matches sum.
func fakeGitHub(t *testing.T, tag string, bin []byte, sum string) *Updater {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"tag_name": %q}`, tag)
	})
	mux.HandleFunc("/dl/"+tag+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  minato-other-arch\n%s  %s\n", strings.Repeat("0", 64), sum, AssetName())
	})
	mux.HandleFunc("/dl/"+tag+"/"+AssetName(), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bin)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &Updater{APIBase: srv.URL + "/api", DownloadBase: srv.URL + "/dl"}
}

func TestDownloadAndReplace(t *testing.T) {
	bin := []byte("#!/bin/sh\necho new minato\n")
	h := sha256.Sum256(bin)
	u := fakeGitHub(t, "v0.2.0", bin, hex.EncodeToString(h[:]))
	ctx := context.Background()

	tag, err := u.Latest(ctx)
	if err != nil || tag != "v0.2.0" {
		t.Fatalf("latest = %q, %v", tag, err)
	}
	got, err := u.Download(ctx, tag)
	if err != nil {
		t.Fatal(err)
	}

	exe := filepath.Join(t.TempDir(), "minato")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Replace(exe, got); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(exe)
	info, _ := os.Stat(exe)
	if string(data) != string(bin) || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("binary not replaced: %q, mode %v", data, info.Mode())
	}
	entries, _ := os.ReadDir(filepath.Dir(exe))
	if len(entries) != 1 {
		t.Errorf("temporary file left behind: %v", entries)
	}
}

func TestDownloadRejectsBadChecksum(t *testing.T) {
	u := fakeGitHub(t, "v0.2.0", []byte("tampered"), strings.Repeat("a", 64))
	if _, err := u.Download(context.Background(), "v0.2.0"); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected a checksum mismatch, got %v", err)
	}
}
