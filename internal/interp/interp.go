// Package interp implements the ${namespace.key} interpolation used in
// minato.yml, compose files and *.tmpl files.
//
// Supported forms:
//
//	${secrets.DB_PASSWORD}        value, error if missing
//	${vars.DOMAIN:-example.com}   default when missing or empty
//	${env.HOME:?HOME must be set}  custom error when missing or empty
//	$${secrets.X}                 escaped, rendered as the literal ${secrets.X}
//
// Only registered namespaces are touched, so plain compose variables such as
// ${TAG} or shell/nginx syntax like $host are left as is.
package interp

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var pattern = regexp.MustCompile(`\$(\$?)\{([a-z][a-z0-9_]*)\.([A-Za-z0-9_.\-]+)(?:(:-|:\?)([^}]*))?\}`)

// Resolver returns the value for key in a namespace. found reports whether
// the key exists.
type Resolver func(key string) (value string, found bool)

// Context maps namespaces to resolvers.
type Context map[string]Resolver

// Map builds a resolver backed by a plain map.
func Map(m map[string]string) Resolver {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

// Options tweak how values are substituted.
type Options struct {
	// EscapeDollar doubles every "$" of substituted values. Needed when the
	// output is read again by a tool doing its own interpolation (compose).
	EscapeDollar bool
}

// Error lists every reference that could not be resolved.
type Error struct {
	Problems []string
}

func (e *Error) Error() string {
	return "interpolation failed: " + strings.Join(e.Problems, "; ")
}

// String interpolates s.
func (c Context) String(s string, opts Options) (string, error) {
	var problems []string
	out := pattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := pattern.FindStringSubmatch(m)
		escaped, ns, key, op, arg := sub[1], sub[2], sub[3], sub[4], sub[5]
		if escaped != "" {
			return m[1:]
		}
		resolve, ok := c[ns]
		if !ok {
			return m
		}
		v, found := resolve(key)
		if !found || (v == "" && op != "") {
			switch op {
			case ":-":
				v = arg
			case ":?":
				msg := arg
				if msg == "" {
					msg = "is required"
				}
				problems = append(problems, fmt.Sprintf("${%s.%s}: %s", ns, key, msg))
				return m
			default:
				if !found {
					problems = append(problems, fmt.Sprintf("${%s.%s}: not found", ns, key))
					return m
				}
			}
		}
		if opts.EscapeDollar {
			v = strings.ReplaceAll(v, "$", "$$")
		}
		return v
	})
	if len(problems) > 0 {
		return out, &Error{Problems: dedupe(problems)}
	}
	return out, nil
}

// StringMap interpolates every value of m and returns a new map.
func (c Context) StringMap(m map[string]string, opts Options) (map[string]string, error) {
	out := make(map[string]string, len(m))
	var problems []string
	for k, v := range m {
		r, err := c.String(v, opts)
		if err != nil {
			if ie, ok := err.(*Error); ok {
				for _, p := range ie.Problems {
					problems = append(problems, k+": "+p)
				}
				continue
			}
			return nil, err
		}
		out[k] = r
	}
	if len(problems) > 0 {
		return out, &Error{Problems: dedupe(problems)}
	}
	return out, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
