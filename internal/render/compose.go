package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Compose is the subset of a compose file minato needs to understand.
type Compose struct {
	raw map[string]any
	// Images maps service names to their image (compose variables expanded).
	Images map[string]string
	// Mounts maps service names to the host paths they use: bind mounts and,
	// in compose mode, file based configs and secrets.
	Mounts map[string][]string
	// ExternalNetworks must exist before deploying.
	ExternalNetworks []string
}

// ParseCompose parses a rendered compose file. env is used to expand compose
// ${VAR} references in the fields minato reads.
func ParseCompose(data []byte, env map[string]string) (*Compose, error) {
	c := &Compose{raw: map[string]any{}, Images: map[string]string{}, Mounts: map[string][]string{}}
	if err := yaml.Unmarshal(data, &c.raw); err != nil {
		return nil, fmt.Errorf("parse compose: %w", err)
	}
	expand := func(s string) string { return expandComposeVars(s, env) }

	topFiles := map[string]map[string]string{"configs": {}, "secrets": {}}
	for kind := range topFiles {
		for name, def := range asMap(c.raw[kind]) {
			if f, ok := asMap(def)["file"].(string); ok {
				topFiles[kind][name] = expand(f)
			}
		}
	}

	services := asMap(c.raw["services"])
	if len(services) == 0 {
		return nil, fmt.Errorf("compose file has no services")
	}
	for name, def := range services {
		svc := asMap(def)
		if img, ok := svc["image"].(string); ok {
			c.Images[name] = expand(img)
		}
		for _, v := range asSlice(svc["volumes"]) {
			var src string
			switch vol := v.(type) {
			case string:
				src = strings.SplitN(expand(vol), ":", 2)[0]
			case map[string]any:
				if t, _ := vol["type"].(string); t == "bind" {
					src, _ = vol["source"].(string)
					src = expand(src)
				}
			}
			if strings.HasPrefix(src, ".") || strings.HasPrefix(src, "/") {
				c.Mounts[name] = append(c.Mounts[name], src)
			}
		}
		for kind, files := range topFiles {
			for _, ref := range asSlice(svc[kind]) {
				var refName string
				switch r := ref.(type) {
				case string:
					refName = r
				case map[string]any:
					refName, _ = r["source"].(string)
				}
				if f, ok := files[refName]; ok {
					c.Mounts[name] = append(c.Mounts[name], f)
				}
			}
		}
	}

	for key, def := range asMap(c.raw["networks"]) {
		n := asMap(def)
		if ext, _ := n["external"].(bool); !ext {
			continue
		}
		name := key
		if v, ok := n["name"].(string); ok && v != "" {
			name = expand(v)
		}
		c.ExternalNetworks = append(c.ExternalNetworks, name)
	}
	sort.Strings(c.ExternalNetworks)
	return c, nil
}

// Services returns the sorted service names.
func (c *Compose) Services() []string {
	var out []string
	for name := range asMap(c.raw["services"]) {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// VersionSwarmObjects gives file based swarm configs and secrets a name
// derived from their content. Swarm configs are immutable, so a new name per
// content lets `docker stack deploy` roll the change to services.
func (c *Compose) VersionSwarmObjects(project string, read func(string) ([]byte, error)) (bool, error) {
	changed := false
	for _, kind := range []string{"configs", "secrets"} {
		for key, def := range asMap(c.raw[kind]) {
			m := asMap(def)
			file, ok := m["file"].(string)
			if !ok || m["name"] != nil {
				continue
			}
			data, err := read(file)
			if err != nil {
				return false, fmt.Errorf("%s %s: %w", kind, key, err)
			}
			sum := sha256.Sum256(data)
			m["name"] = fmt.Sprintf("%s_%s_%s", project, key, hex.EncodeToString(sum[:])[:10])
			changed = true
		}
	}
	return changed, nil
}

// Marshal encodes the compose file.
func (c *Compose) Marshal() ([]byte, error) { return yaml.Marshal(c.raw) }

var composeVar = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:?[-?])([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// expandComposeVars mimics compose interpolation for the few fields minato
// reads. Missing variables fall back to the process environment.
func expandComposeVars(s string, env map[string]string) string {
	lookup := func(k string) (string, bool) {
		if v, ok := env[k]; ok {
			return v, true
		}
		return os.LookupEnv(k)
	}
	return composeVar.ReplaceAllStringFunc(s, func(m string) string {
		if m == "$$" {
			return "$"
		}
		sub := composeVar.FindStringSubmatch(m)
		name, op, arg := sub[1], sub[2], sub[3]
		if name == "" {
			name = sub[4]
		}
		v, ok := lookup(name)
		if strings.HasPrefix(op, "-") && !ok || op == ":-" && v == "" {
			return arg
		}
		return v
	})
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
