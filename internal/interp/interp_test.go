package interp

import (
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	ctx := Context{
		"secrets": Map(map[string]string{"PASS": "p$ss", "EMPTY": ""}),
		"vars":    Map(map[string]string{"DOMAIN": "example.com"}),
	}
	cases := []struct {
		name, in, want string
		opts           Options
	}{
		{"value", "https://${vars.DOMAIN}/x", "https://example.com/x", Options{}},
		{"default on missing", "${vars.PORT:-8080}", "8080", Options{}},
		{"default on empty", "${secrets.EMPTY:-fallback}", "fallback", Options{}},
		{"empty kept without op", "[${secrets.EMPTY}]", "[]", Options{}},
		{"escaped", "$${vars.DOMAIN}", "${vars.DOMAIN}", Options{}},
		{"unknown namespace untouched", "${TAG} ${foo.bar} $host", "${TAG} ${foo.bar} $host", Options{}},
		{"escape dollar for compose", "pw=${secrets.PASS}", "pw=p$$ss", Options{EscapeDollar: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ctx.String(c.in, c.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestStringErrors(t *testing.T) {
	ctx := Context{"secrets": Map(map[string]string{})}
	_, err := ctx.String("${secrets.A} ${secrets.B:?B is mandatory} ${secrets.A}", Options{})
	if err == nil {
		t.Fatal("expected error")
	}
	ie := err.(*Error)
	if len(ie.Problems) != 2 {
		t.Fatalf("expected 2 deduped problems, got %v", ie.Problems)
	}
	if !strings.Contains(err.Error(), "B is mandatory") {
		t.Fatalf("custom message missing: %v", err)
	}
}
