package deploy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	deploy "github.com/Elagoht/collage-deploy"
	redirects "github.com/Elagoht/collage-redirects"
	secure "github.com/Elagoht/collage-secure"
	"github.com/Elagoht/collage/pkg/collage"
)

// e2eSite builds a small site for target into its own directory: two pages, one
// with a redirect of its own, a feed document, a static mount with a
// fingerprinted file, collage-secure's headers with a nonce-bearing CSP, and
// collage-redirects' rules, one of them a prefix.
func e2eSite(t *testing.T, target string) (string, *collage.BuildReport) {
	t.Helper()
	templates := fstest.MapFS{
		"t/home.html":  {Data: []byte(`<html><body><h1>Home</h1><script nonce="{{cspNonce}}">1</script></body></html>`)},
		"t/about.html": {Data: []byte(`<html><body><h1>About</h1></body></html>`)},
	}
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: templates, Root: "t"},
		Logger:   slog.New(slog.NewTextHandler(new(bytes.Buffer), nil)),
		Plugins: []collage.Plugin{
			secure.New(secure.Options{
				CSP:            "default-src 'self'; script-src 'self' 'nonce-{nonce}'",
				ReferrerPolicy: "same-origin",
			}),
			redirects.New(redirects.Options{Rules: []redirects.Rule{
				{From: "/old-pricing", To: "/about", Status: 301},
				{From: "/blog/*", To: "/posts/:splat", Status: 301},
			}}),
			deploy.NewWith(deploy.Config{Target: target}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	pages := []*collage.Page{
		collage.NewPage("home").WithContent(collage.NewFragment("home", "home.html").Build()).WithPath("en", "/").Build(),
		collage.NewPage("about").WithContent(collage.NewFragment("about", "about.html").Build()).
			WithPath("en", "/about").WithPermanentRedirect("/about-us", "/about").Build(),
	}
	for _, p := range pages {
		if err := app.RegisterPage(p); err != nil {
			t.Fatal(err)
		}
	}
	feed := collage.NewDocument("feed", "application/rss+xml; charset=utf-8").
		AtRoot("/feed.xml").WithBody([]byte(`<rss version="2.0"></rss>`)).Build()
	if err := app.RegisterDocument(feed); err != nil {
		t.Fatal(err)
	}
	err = app.Mount("/static/", fstest.MapFS{"app.3f9a1c2e.css": {Data: []byte("body{color:red}")}},
		collage.WithCacheControl("public, max-age=31536000, immutable"))
	if err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	builder, err := collage.NewBuilder(app, collage.BuildOptions{OutDir: out})
	if err != nil {
		t.Fatal(err)
	}
	report, err := builder.Build(context.Background())
	if err != nil {
		t.Fatalf("%s: Build: %v", target, err)
	}
	return out, report
}

func read(t *testing.T, out, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return string(body)
}

// unstableCSP reports whether the build warned that the nonce CSP differs
// between responses.
func unstableCSP(report *collage.BuildReport) bool {
	return slices.ContainsFunc(report.Findings, func(f collage.Finding) bool {
		return f.Rule == "unstable-header" && f.Level == collage.FindingWarning &&
			strings.Contains(f.Message, "Content-Security-Policy")
	})
}

func reportFinding(report *collage.BuildReport, rule, text string) bool {
	return slices.ContainsFunc(report.Findings, func(f collage.Finding) bool {
		return f.Rule == rule && strings.Contains(f.Message, text)
	})
}

func assertLines(t *testing.T, name, body string, want ...string) {
	t.Helper()
	lines := strings.Split(body, "\n")
	for _, w := range want {
		if !slices.Contains(lines, w) {
			t.Errorf("%s has no line %q:\n%s", name, w, body)
		}
	}
}

func assertNoNonceCSP(t *testing.T, name, body string) {
	t.Helper()
	if strings.Contains(body, "Content-Security-Policy") || strings.Contains(body, "nonce-") {
		t.Errorf("%s carries the per-response CSP:\n%s", name, body)
	}
}

func TestEndToEnd_NetlifyAndCloudflare(t *testing.T) {
	for _, target := range []string{"netlify", "cloudflare"} {
		t.Run(target, func(t *testing.T) {
			out, report := e2eSite(t, target)
			if _, err := os.Stat(filepath.Join(out, "static", "app.3f9a1c2e.css")); err != nil {
				t.Errorf("the fingerprinted file was not written: %v", err)
			}
			headers := read(t, out, "_headers")
			assertLines(t, "_headers", headers,
				"  Referrer-Policy: same-origin",
				"  Content-Type: application/rss+xml; charset=utf-8",
				"  Cache-Control: public, max-age=31536000, immutable",
			)
			assertNoNonceCSP(t, "_headers", headers)
			redirectsFile := read(t, out, "_redirects")
			assertLines(t, "_redirects", redirectsFile,
				"/about-us /about 301",
				"/old-pricing /about 301",
				"/blog /posts/ 301",
				"/blog/* /posts/:splat 301",
			)
			if !unstableCSP(report) {
				t.Errorf("no unstable-header warning for the CSP: %+v", report.Findings)
			}
		})
	}
}

type vercelHeader struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type vercelConfig struct {
	Headers []struct {
		Source  string         `json:"source"`
		Headers []vercelHeader `json:"headers"`
	} `json:"headers"`
	Redirects []struct {
		Source      string `json:"source"`
		Destination string `json:"destination"`
		StatusCode  int    `json:"statusCode"`
	} `json:"redirects"`
}

func TestEndToEnd_Vercel(t *testing.T) {
	out, report := e2eSite(t, "vercel")
	body := read(t, out, "vercel.json")
	var cfg vercelConfig
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatalf("vercel.json: %v\n%s", err, body)
	}
	var all []vercelHeader
	for _, rule := range cfg.Headers {
		all = append(all, rule.Headers...)
	}
	for _, want := range []vercelHeader{
		{"Referrer-Policy", "same-origin"},
		{"Content-Type", "application/rss+xml; charset=utf-8"},
		{"Cache-Control", "public, max-age=31536000, immutable"},
	} {
		if !slices.Contains(all, want) {
			t.Errorf("vercel.json has no header %+v:\n%s", want, body)
		}
	}
	assertNoNonceCSP(t, "vercel.json", body)
	got := make([]string, 0, len(cfg.Redirects))
	for _, r := range cfg.Redirects {
		got = append(got, r.Source+" "+r.Destination+" "+jsonInt(r.StatusCode))
	}
	for _, want := range []string{
		"/about-us /about 301",
		"/old-pricing /about 301",
		"/blog /posts/ 301",
		"/blog/:rest+ /posts/:rest+ 301",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("vercel.json has no redirect %q: %v", want, got)
		}
	}
	if !unstableCSP(report) {
		t.Errorf("no unstable-header warning for the CSP: %+v", report.Findings)
	}
}

func jsonInt(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// GitHub Pages carries no headers and no patterned redirect: the literal rules
// become meta-refresh pages, and the rest are warnings.
func TestEndToEnd_GitHubPages(t *testing.T) {
	out, report := e2eSite(t, "github-pages")
	for from, to := range map[string]string{
		"about-us/index.html":    "/about",
		"old-pricing/index.html": "/about",
		"blog/index.html":        "/posts/",
	} {
		if page := read(t, out, from); !strings.Contains(page, `url=`+to+`"`) {
			t.Errorf("%s does not refresh to %s:\n%s", from, to, page)
		}
	}
	if _, err := os.Stat(filepath.Join(out, ".nojekyll")); err != nil {
		t.Errorf(".nojekyll: %v", err)
	}
	for _, name := range []string{"_headers", "_redirects", "vercel.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); err == nil {
			t.Errorf("%s was written for GitHub Pages", name)
		}
	}
	if !reportFinding(report, "deploy-unsupported-redirect", "/blog/{rest...}") {
		t.Errorf("no warning for the prefix rule's catch-all: %+v", report.Findings)
	}
	if !reportFinding(report, "deploy-headers-lost", "") {
		t.Errorf("no warning that the headers are lost: %+v", report.Findings)
	}
	if !unstableCSP(report) {
		t.Errorf("no unstable-header warning for the CSP: %+v", report.Findings)
	}
	if page := read(t, out, "index.html"); strings.Contains(page, "nonce") {
		t.Errorf("the home page kept a nonce attribute:\n%s", page)
	}
}
