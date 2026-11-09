package daemon

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"testing"
)

func TestAuthorized(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	goodSig := hex.EncodeToString(mac.Sum(nil))

	cases := []struct {
		name          string
		target        string
		headers       map[string]string
		token, secret string
		want          bool
	}{
		{"bearer", "/", map[string]string{"Authorization": "Bearer tok"}, "tok", "", true},
		{"header", "/", map[string]string{"X-Minato-Token": "tok"}, "tok", "", true},
		{"gitlab", "/", map[string]string{"X-Gitlab-Token": "tok"}, "tok", "", true},
		{"query", "/?token=tok", nil, "tok", "", true},
		{"wrong token", "/?token=nope", nil, "tok", "", false},
		{"github signature", "/", map[string]string{"X-Hub-Signature-256": "sha256=" + goodSig}, "", "s3cret", true},
		{"gitea signature", "/", map[string]string{"X-Gitea-Signature": goodSig}, "", "s3cret", true},
		{"bad signature", "/", map[string]string{"X-Hub-Signature-256": "sha256=00ff"}, "", "s3cret", false},
		{"nothing configured", "/?token=", nil, "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", c.target, nil)
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			if got := authorized(r, body, c.token, c.secret); got != c.want {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}
