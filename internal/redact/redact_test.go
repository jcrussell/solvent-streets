package redact

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://h.example/arcgis/query?token=abc123&f=json", "https://h.example/arcgis/query?token=REDACTED&f=json"},
		{"https://h.example/q?f=json&API_KEY=k&Signature=s&client_secret=x&password=p", "https://h.example/q?f=json&API_KEY=REDACTED&Signature=REDACTED&client_secret=REDACTED&password=REDACTED"},
		{"https://h.example/q?where=1%3D1&outFields=*", "https://h.example/q?where=1%3D1&outFields=*"},
		{"https://user:pw@h.example/q", "https://user:REDACTED@h.example/q"},
		{"https://h.example/plain", "https://h.example/plain"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := URL(tc.in); got != tc.want {
			t.Errorf("URL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestString(t *testing.T) {
	in := `arcgis https://h.example/query?f=json&token=abc123 returned 498: "https://u:pw@h.example/x?sig=zz"`
	got := String(in)
	for _, secret := range []string{"abc123", "pw@", "zz"} {
		if strings.Contains(got, secret) {
			t.Errorf("String left %q in %q", secret, got)
		}
	}
	if !strings.Contains(got, "f=json") || !strings.Contains(got, "returned 498") {
		t.Errorf("String dropped non-secret text: %q", got)
	}
}

func TestError(t *testing.T) {
	ue := &url.Error{Op: "Get", URL: "https://h.example/query?token=abc123", Err: context.DeadlineExceeded}
	err := Error(ue)
	if strings.Contains(err.Error(), "abc123") {
		t.Errorf("Error message %q leaks the token", err)
	}
	got, ok := errors.AsType[*url.Error](err)
	if !ok || strings.Contains(got.URL, "abc123") {
		t.Errorf("unwrapped *url.Error URL = %v, want it redacted", got)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("Error lost the cause")
	}
	plain := errors.New("no url here")
	if got := Error(plain); got.Error() != plain.Error() || errors.Unwrap(got) != nil {
		t.Error("Error wrapped an error with nothing to redact")
	}
	if Error(nil) != nil {
		t.Error("Error(nil) != nil")
	}
}

func TestSecretKeyNames(t *testing.T) {
	for name, secret := range map[string]bool{
		"token": true, "access_token": true, "API_KEY": true, "apikey": true, "X-Amz-Signature": true,
		"sig": true, "client_secret": true, "password": true,
		"keyword": false, "design": false, "outFields": false, "where": false, "f": false, "monkey": false,
	} {
		if got := secretKey.MatchString(name); got != secret {
			t.Errorf("secretKey(%q) = %v, want %v", name, got, secret)
		}
	}
}

func TestString_Edges(t *testing.T) {
	cases := map[string]string{
		`(see https://h.example/q?token=abc)`:          `(see https://h.example/q?token=REDACTED)`,
		`https://u:p@ss@h.example/q`:                   `https://u:REDACTED@h.example/q`,
		`https://h.example/q?keyword=parks&design=new`: `https://h.example/q?keyword=parks&design=new`,
	}
	for in, want := range cases {
		if got := String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
	if got := URL("https://u:p@ss@h.example/q"); strings.Contains(got, "p@ss") || strings.Contains(got, "ss@") {
		t.Errorf("URL with a raw @ in the password = %q", got)
	}
}

func TestRedact_ReviewEdges(t *testing.T) {
	cases := map[string]string{
		`https://h.example/q?f=json;token=abc`:                     `https://h.example/q?f=json;token=REDACTED`,
		`body: token=abc&f=json`:                                   `body: token=REDACTED&f=json`,
		`{"error":{"code":498,"token":"abc","message":"Invalid"}}`: `{"error":{"code":498,"token":"REDACTED","message":"Invalid"}}`,
		`see https://h.example/cb#access_token=abc`:                `see https://h.example/cb#REDACTED`,
		`https://h.example/q?auth=abc&code=xyz&where=1%3D1`:        `https://h.example/q?auth=REDACTED&code=REDACTED&where=1%3D1`,
	}
	for in, want := range cases {
		if got := String(in); got != want {
			t.Errorf("String(%q)\n got %q\nwant %q", in, got, want)
		}
	}
	if got := URL("https://h.example/q?f=json;sig=abc#frag"); got != "https://h.example/q?f=json;sig=REDACTED#REDACTED" {
		t.Errorf("URL = %q", got)
	}
}
