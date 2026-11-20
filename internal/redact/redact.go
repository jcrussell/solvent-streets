// Package redact hides credentials carried in URLs before pvmt shows or
// stores them. An arcgis_url may carry an access token in its query string
// (?token=...); every progress line, error, ingest_attempts row and cache
// .meta file that names a URL goes through URL, Error or String first.
package redact

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Placeholder replaces each redacted value.
const Placeholder = "REDACTED"

// secretKey matches a query parameter name that carries a credential: one of
// the credential words as the whole name or as a part of it set off by _, -
// or . ("token", "API_KEY", "X-Amz-Signature", "client_secret"), but not a
// word that merely contains one ("keyword", "design").
var secretKey = regexp.MustCompile(`(?i)(^|[_.-])(token|key|secret|password|passwd|pwd|sig|signature|apikey|accesstoken|authtoken|auth|code)([_.-]|$)`)

// queryParam matches one name=value pair inside free text: the name after a
// ?, &, ; or the start of a word (a bare token=... in an echoed body), the
// value up to the next separator.
var queryParam = regexp.MustCompile(`(^|[?&;\s,({\[])([^=&;?#\s"'(){}\[\],]+)=([^&;#\s"'(),}\]]*)`)

// jsonField matches a "name": "value" string member of echoed JSON.
var jsonField = regexp.MustCompile(`"([^"\\]+)"(\s*:\s*)"((?:[^"\\]|\\.)*)"`)

// fragment matches the fragment of a URL in free text, which can carry a
// token (an OAuth implicit-flow #access_token=...).
var fragment = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'#]*)#[^\s"'()]+`)

// userinfo matches the password of a scheme://user:password@ URL in free text,
// up to the last @ before the path, as url.Parse splits it, so a raw @ in the
// password is covered.
var userinfo = regexp.MustCompile(`(://[^/:@\s"']*):[^/\s"']*@`)

// URL returns raw with the value of every query parameter named as a
// credential (secretKey: token, key, secret, password, sig and kin, any case)
// replaced by REDACTED, and any userinfo password and fragment too. A URL
// that does not parse is redacted as free text (String).
func URL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return String(raw)
	}
	return redactParsed(u).String()
}

func redactParsed(u *url.URL) *url.URL {
	out := *u
	if out.User != nil {
		if _, ok := out.User.Password(); ok {
			out.User = url.UserPassword(out.User.Username(), Placeholder)
		}
	}
	if out.RawQuery != "" {
		out.RawQuery = redactQuery(out.RawQuery)
	}
	if out.Fragment != "" || out.RawFragment != "" {
		out.Fragment, out.RawFragment = Placeholder, ""
	}
	return &out
}

// redactQuery rewrites a raw query string pair by pair (pairs split on & or
// ;), keeping the order and encoding of every pair it does not redact.
func redactQuery(raw string) string {
	var pairs, seps []string
	start := 0
	for i := range len(raw) {
		if raw[i] == '&' || raw[i] == ';' {
			pairs, seps = append(pairs, raw[start:i]), append(seps, raw[i:i+1])
			start = i + 1
		}
	}
	pairs = append(pairs, raw[start:])
	for i, p := range pairs {
		name, _, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		if dec, err := url.QueryUnescape(name); err == nil {
			name = dec
		}
		if secretKey.MatchString(name) {
			k, _, _ := strings.Cut(p, "=")
			pairs[i] = k + "=" + Placeholder
		}
	}
	var b strings.Builder
	for i, p := range pairs {
		b.WriteString(p)
		if i < len(seps) {
			b.WriteString(seps[i])
		}
	}
	return b.String()
}

// Secrets returns the credential values URL would replace in raw: the
// userinfo password, the fragment and the value of every query parameter
// named as a credential, decoded. A raw that does not parse has none it can
// name.
func Secrets(raw string) []string {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	var out []string
	if u.User != nil {
		if pw, ok := u.User.Password(); ok && pw != "" {
			out = append(out, pw)
		}
	}
	if u.Fragment != "" {
		out = append(out, u.Fragment)
	}
	for _, p := range strings.FieldsFunc(u.RawQuery, func(r rune) bool { return r == '&' || r == ';' }) {
		name, value, ok := strings.Cut(p, "=")
		if !ok || value == "" {
			continue
		}
		if dec, err := url.QueryUnescape(name); err == nil {
			name = dec
		}
		if !secretKey.MatchString(name) {
			continue
		}
		if dec, err := url.QueryUnescape(value); err == nil {
			value = dec
		}
		out = append(out, value)
	}
	return out
}

// String redacts every credential found in free text, such as an error
// message that quotes a URL or echoes a response body: secret name=value
// pairs (in a query string or bare), secret "name": "value" JSON members,
// URL fragments and userinfo passwords.
func String(s string) string {
	s = fragment.ReplaceAllString(s, "$1#"+Placeholder)
	s = jsonField.ReplaceAllStringFunc(s, func(m string) string {
		sub := jsonField.FindStringSubmatch(m)
		if !secretKey.MatchString(sub[1]) {
			return m
		}
		return `"` + sub[1] + `"` + sub[2] + `"` + Placeholder + `"`
	})
	s = queryParam.ReplaceAllStringFunc(s, func(m string) string {
		sub := queryParam.FindStringSubmatch(m)
		name := sub[2]
		if dec, err := url.QueryUnescape(name); err == nil {
			name = dec
		}
		if !secretKey.MatchString(name) {
			return m
		}
		return sub[1] + sub[2] + "=" + Placeholder
	})
	return userinfo.ReplaceAllString(s, "$1:"+Placeholder+"@")
}

// Error returns err with the URL of the *url.Error in its chain (the HTTP
// client's "Get \"https://...?token=...\": ..." error) rewritten by URL, and
// with its whole message redacted by String, so neither its text nor a caller
// that unwraps it to the *url.Error sees a credential. errors.Is and errors.As
// still reach every cause. Apply it before wrapping err with fmt.Errorf, which
// copies the message as it stands. A nil err stays nil.
func Error(err error) error {
	if err == nil {
		return nil
	}
	if ue, ok := errors.AsType[*url.Error](err); ok {
		ue.URL = URL(ue.URL)
	}
	if msg := err.Error(); String(msg) != msg {
		return &redacted{err: err}
	}
	return err
}

// redacted wraps an error whose message still quotes a credential. Unwrap
// returns the original, so errors.Is and errors.As still see its causes.
type redacted struct{ err error }

func (r *redacted) Error() string { return String(r.err.Error()) }
func (r *redacted) Unwrap() error { return r.err }
