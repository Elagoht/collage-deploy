package deploy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Elagoht/collage/pkg/collage"
)

// vercelRoutes is the number of routes Vercel's documentation allows in one
// deployment ("up to 2048 routes"); headers and redirects are counted together.
const vercelRoutes = 2048

// vercelConfig is the part of vercel.json the plugin writes. The field order is
// the key order in the file.
type vercelConfig struct {
	Headers   []vercelHeaders  `json:"headers,omitempty"`
	Redirects []vercelRedirect `json:"redirects,omitempty"`
}

type vercelHeaders struct {
	Source  string        `json:"source"`
	Headers []vercelValue `json:"headers"`
}

type vercelValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type vercelRedirect struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	StatusCode  int    `json:"statusCode"`
}

// vercelSyntax is path-to-regexp's, as vercel.json's source reads it: ":name"
// for a placeholder, which may have text around it in a segment, and
// ":name+" for a catch-all (one or more segments, as collage's needs one).
var vercelSyntax = syntax{
	param:        func(name string) string { return ":" + name },
	catchAllFrom: func(name string) string { return ":" + name + "+" },
	catchAllTo:   func(name string) string { return ":" + name + "+" },
	affixed:      true,
	literal:      func(text string) (string, error) { return vercelLiteral(text), nil },
}

// vercelLiteral escapes the characters path-to-regexp reads as syntax.
func vercelLiteral(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		if strings.IndexByte(`\:()*+?{}[]`, text[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(text[i])
	}
	return b.String()
}

// vercelTo checks a destination's literal text. Vercel substitutes
// placeholders in the path and query only and never in an absolute URL's
// "scheme://authority", so a port there ("http://h:8080") is no placeholder. A
// piece of text that follows a placeholder cannot begin with a scheme: the
// word character after "}" is refused before this is asked.
func vercelTo(text string) error {
	if _, authority, ok := strings.Cut(text, "://"); ok && validScheme(text[:strings.Index(text, "://")]) {
		rest := ""
		if at := strings.IndexAny(authority, "/?#"); at >= 0 {
			rest = authority[at:]
		}
		text = rest
	}
	if placeholderLike(text) {
		return fmt.Errorf("to %q holds \":\" before a name, which Vercel reads as a placeholder", text)
	}
	return nil
}

// validScheme reports whether s is a URL scheme: a letter, then letters,
// digits, "+", "-" or ".".
func validScheme(s string) bool {
	if s == "" || !(s[0] >= 'a' && s[0] <= 'z' || s[0] >= 'A' && s[0] <= 'Z') {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !(wordByte(c) && c != '_' || c == '+' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// vercelSources are the sources a header rule is written at. Vercel matches a
// source exactly, trailing "/" included, and by default serves a directory's
// index.html at "/blog" and "/blog/" alike, so a directory's rule is written
// at both; "/dir/*" is "/dir/(.*)", and also "/dir" when the directory has an
// index page.
func vercelSources(path string, served map[string]bool) []string {
	if prefix, ok := strings.CutSuffix(path, "*"); ok {
		sources := []string{vercelLiteral(prefix) + "(.*)"}
		if prefix != "/" && served[prefix] {
			sources = append(sources, vercelLiteral(strings.TrimSuffix(prefix, "/")))
		}
		return sources
	}
	if path != "/" && strings.HasSuffix(path, "/") {
		return []string{vercelLiteral(strings.TrimSuffix(path, "/")), vercelLiteral(path)}
	}
	return []string{vercelLiteral(path)}
}

// writeVercel writes vercel.json.
func writeVercel(ev *collage.BuildFinishedEvent, out *output, rules []HeaderRule) error {
	if !claim(ev, "vercel.json") {
		return nil
	}
	served := map[string]bool{}
	for _, f := range ev.Files {
		served[ServedAt(ev.OutDir, f)] = true
	}
	var cfg vercelConfig
	for _, r := range rules {
		var values []vercelValue
		for _, name := range sortedNames(r.Headers) {
			if len(r.Headers[name]) > 0 {
				values = append(values, vercelValue{Key: name, Value: strings.Join(r.Headers[name], ", ")})
			}
		}
		if len(values) == 0 {
			continue
		}
		for _, source := range vercelSources(r.Path, served) {
			cfg.Headers = append(cfg.Headers, vercelHeaders{Source: source, Headers: values})
		}
	}

	var gone, unwritable []string
	for _, r := range readRedirects(ev) {
		label := fmt.Sprintf("%s (%s)", r.From, r.Source)
		switch r.Status {
		case 301, 302, 307, 308:
		case 410:
			gone = append(gone, label)
			continue
		default:
			unwritable = append(unwritable, fmt.Sprintf("%s: status %d", label, r.Status))
			continue
		}
		from, to, err := translate(r, vercelSyntax, vercelTo)
		if err != nil {
			if unsafeRedirect(ev, r.BuiltRedirect, err) {
				continue
			}
			unwritable = append(unwritable, fmt.Sprintf("%s: %v", label, err))
			continue
		}
		cfg.Redirects = append(cfg.Redirects, vercelRedirect{Source: from, Destination: to, StatusCode: r.Status})
		// Vercel matches the source exactly and, by default, serves a path
		// with or without its trailing "/"; collage redirects both.
		if twin := toggleSlash(from); twin != from {
			cfg.Redirects = append(cfg.Redirects, vercelRedirect{Source: twin, Destination: to, StatusCode: r.Status})
		}
	}
	if len(gone) > 0 {
		ev.Warn("", "deploy-gone", fmt.Sprintf("vercel.json's redirects cannot answer 410; these paths answer 404 instead: %s", left(gone)))
	}
	if len(unwritable) > 0 {
		ev.Warn("", "deploy-unsupported-redirect", fmt.Sprintf("%d redirects cannot be written in vercel.json and are left out: %s", len(unwritable), strings.Join(unwritable, "; ")))
	}
	if n := len(cfg.Headers) + len(cfg.Redirects); n > vercelRoutes {
		ev.Warn("", "deploy-limit", fmt.Sprintf("vercel.json holds %d header and redirect routes; Vercel's documentation allows up to %d routes per deployment", n, vercelRoutes))
	}
	if len(cfg.Headers) == 0 && len(cfg.Redirects) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("elagoht/deploy: vercel.json: %w", err)
	}
	return out.create("vercel.json", buf.Bytes())
}
