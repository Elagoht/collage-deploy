package deploy_test

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	deploy "github.com/Elagoht/collage-deploy"
	"github.com/Elagoht/collage/pkg/collage"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata/golden")

const (
	html     = "text/html; charset=utf-8"
	csp      = "default-src 'self'"
	noCache  = "no-cache"
	referrer = "no-referrer"
)

// fixture is one exported site: pages and assets sharing the baseline headers,
// a fingerprinted asset directory, a feed with its own Content-Type and two
// Link values, an about page framed differently, the 404 page the build wrote
// without asking for it, and literal, patterned, catch-all and gone redirects.
func fixture(dir string) *collage.BuildFinishedEvent {
	// A page is registered as collage registers one by default, with no
	// trailing slash, and written to its directory's index.html.
	page := func(path, frame string) collage.BuiltFile {
		f := ok(path,
			"X-Content-Type-Options", nosniff, "Referrer-Policy", referrer,
			"Content-Type", html, "Cache-Control", noCache, "X-Frame-Options", frame, "Content-Security-Policy", csp)
		f.Kind = "page"
		f.File = filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(path, "/")), "index.html")
		return f
	}
	asset := func(path, ctype string) collage.BuiltFile {
		return ok(path, "X-Content-Type-Options", nosniff, "Referrer-Policy", referrer, "Content-Type", ctype, "Cache-Control", immutable)
	}
	return &collage.BuildFinishedEvent{
		OutDir: dir,
		Files: []collage.BuiltFile{
			page("/", "DENY"),
			page("/blog", "DENY"),
			page("/about", "SAMEORIGIN"),
			asset("/static/app.3f2a9c.css", "text/css; charset=utf-8"),
			asset("/static/print.77d01b.css", "text/css; charset=utf-8"),
			ok("/feed.xml", "X-Content-Type-Options", nosniff, "Referrer-Policy", referrer,
				"Content-Type", "application/rss+xml", "Cache-Control", noCache,
				"Link", "</>; rel=alternate", "Link", "</feed.xml>; rel=self"),
			{Kind: "page", Path: "/404.html"},
		},
		Redirects: []collage.BuiltRedirect{
			{From: "/old", To: "/new", Status: 301, Source: "page:new"},
			{From: "/blog/{slug}/feed", To: "/posts/{slug}/feed.xml", Status: 308, Source: "page:posts"},
			{From: "/docs/{rest...}", To: "/manual/{rest}", Status: 301, Source: "page:manual"},
			{From: "/gone", Status: 410, Source: "elagoht/redirects"},
			{From: "/blog/{slug}", To: "/posts/{slug}", Status: 308, Source: "page:posts"},
		},
	}
}

func run(t *testing.T, target string, ev *collage.BuildFinishedEvent) {
	t.Helper()
	if err := deploy.NewWith(deploy.Config{Target: target}).OnBuildFinished(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
}

// tree is every file under dir, by slash path, with its content.
func tree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = data
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// golden compares what the target wrote under out with testdata/golden/<target>,
// byte for byte, and rewrites the golden files under -update.
func golden(t *testing.T, target, out string) {
	t.Helper()
	got := tree(t, out)
	dir := filepath.Join("testdata", "golden", target)
	if *update {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		for name, data := range got {
			p := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	want := tree(t, dir)
	for name, data := range want {
		if g, ok := got[name]; !ok {
			t.Errorf("%s: %s was not written", target, name)
		} else if !bytes.Equal(g, data) {
			t.Errorf("%s: %s differs\n--- got\n%s\n--- want\n%s", target, name, g, data)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("%s: %s was written, and is not in the golden files", target, name)
		}
	}
}

func findings(ev *collage.BuildFinishedEvent, rule string) []collage.Finding {
	var out []collage.Finding
	for _, f := range ev.Findings {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

func noErrors(t *testing.T, ev *collage.BuildFinishedEvent) {
	t.Helper()
	for _, f := range ev.Findings {
		if f.Level == collage.FindingError {
			t.Errorf("error finding: %+v", f)
		}
	}
}

func TestGolden(t *testing.T) {
	for _, target := range deploy.Targets {
		t.Run(target, func(t *testing.T) {
			dir := t.TempDir()
			ev := fixture(dir)
			run(t, target, ev)
			noErrors(t, ev)
			golden(t, target, dir)
			for name, data := range tree(t, dir) {
				if bytes.ContainsAny(data, "{}") && name != "vercel.json" {
					t.Errorf("%s: %s holds a brace from a redirect:\n%s", target, name, data)
				}
				if name == "vercel.json" && (bytes.Contains(data, []byte("{slug")) || bytes.Contains(data, []byte("{rest"))) {
					t.Errorf("vercel.json holds an untranslated placeholder:\n%s", data)
				}
			}
		})
	}
}

func TestDeterministic(t *testing.T) {
	for _, target := range deploy.Targets {
		first := t.TempDir()
		run(t, target, fixture(first))
		want := tree(t, first)
		for range 5 {
			dir := t.TempDir()
			ev := fixture(dir)
			slices.Reverse(ev.Files)
			run(t, target, ev)
			got := tree(t, dir)
			for name, data := range want {
				if !bytes.Equal(got[name], data) {
					t.Fatalf("%s: %s differs between runs", target, name)
				}
			}
		}
	}
}

func TestNetlifyWarnings(t *testing.T) {
	ev := fixture(t.TempDir())
	run(t, "netlify", ev)
	gone := findings(ev, "deploy-gone")
	if len(gone) != 1 || !strings.Contains(gone[0].Message, "/gone (elagoht/redirects)") {
		t.Errorf("gone = %+v", gone)
	}
	status := findings(ev, "deploy-status")
	if len(status) != 1 || !strings.Contains(status[0].Message, "/blog/{slug} (page:posts) 308 as 301") {
		t.Errorf("status = %+v", status)
	}
	if len(ev.Findings) != 2 {
		t.Errorf("findings = %+v", ev.Findings)
	}
}

func TestCloudflareWarnings(t *testing.T) {
	ev := fixture(t.TempDir())
	run(t, "cloudflare", ev)
	if gone := findings(ev, "deploy-gone"); len(gone) != 1 {
		t.Errorf("gone = %+v", gone)
	}
	if len(ev.Findings) != 1 {
		t.Errorf("findings = %+v", ev.Findings)
	}
}

func TestVercelWarnings(t *testing.T) {
	ev := fixture(t.TempDir())
	run(t, "vercel", ev)
	if gone := findings(ev, "deploy-gone"); len(gone) != 1 || !strings.Contains(gone[0].Message, "/gone") {
		t.Errorf("gone = %+v", gone)
	}
	if len(ev.Findings) != 1 {
		t.Errorf("findings = %+v", ev.Findings)
	}
}

func TestGitHubPagesWarnings(t *testing.T) {
	ev := fixture(t.TempDir())
	run(t, "github-pages", ev)
	lost := findings(ev, "deploy-headers-lost")
	// Six captured files; the header names among them: X-Content-Type-Options,
	// Referrer-Policy, Content-Type, Cache-Control, X-Frame-Options,
	// Content-Security-Policy and Link.
	if len(lost) != 1 || lost[0].Message != "7 header names on 6 paths cannot be set on GitHub Pages" {
		t.Errorf("headers = %+v", lost)
	}
	unsupported := findings(ev, "deploy-unsupported-redirect")
	if len(unsupported) != 1 || !strings.Contains(unsupported[0].Message, "/blog/{slug}") || !strings.Contains(unsupported[0].Message, "/docs/{rest...}") {
		t.Errorf("patterned = %+v", unsupported)
	}
	if gone := findings(ev, "deploy-gone"); len(gone) != 1 {
		t.Errorf("gone = %+v", gone)
	}
	if refresh := findings(ev, "deploy-meta-refresh"); len(refresh) != 1 || !strings.Contains(refresh[0].Message, "/old 301") {
		t.Errorf("meta-refresh = %+v", refresh)
	}
	if len(ev.Findings) != 4 {
		t.Errorf("findings = %+v", ev.Findings)
	}
}

func TestExistingFilesAreRefused(t *testing.T) {
	cases := []struct {
		target, name string
		built        bool
	}{
		{"netlify", "_headers", false},
		{"netlify", "_redirects", true},
		{"cloudflare", "_redirects", false},
		{"cloudflare", "_headers", true},
		{"vercel", "vercel.json", false},
		{"vercel", "vercel.json", true},
		{"github-pages", "old/index.html", false},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s/%s/built=%v", c.target, c.name, c.built), func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, filepath.FromSlash(c.name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			mine := []byte("the user's own\n")
			if err := os.WriteFile(p, mine, 0o644); err != nil {
				t.Fatal(err)
			}
			ev := fixture(dir)
			if c.built {
				ev.Files = append(ev.Files, collage.BuiltFile{Kind: "document", Name: "hosts", Path: "/" + c.name})
			}
			run(t, c.target, ev)
			errs := findings(ev, "deploy-existing-file")
			if len(errs) != 1 || errs[0].Level != collage.FindingError || !strings.Contains(errs[0].Message, c.name) {
				t.Fatalf("findings = %+v", ev.Findings)
			}
			if c.built && !strings.Contains(errs[0].Message, `document "hosts"`) {
				t.Errorf("the error does not say the build wrote it: %s", errs[0].Message)
			}
			if data, _ := os.ReadFile(p); !bytes.Equal(data, mine) {
				t.Errorf("%s was changed: %q", c.name, data)
			}
			if c.target != "github-pages" {
				// Nothing else is written either: a refused _redirects leaves
				// no fresh _headers behind.
				if got := tree(t, dir); len(got) != 1 {
					t.Errorf("wrote %v", slices.Sorted(maps.Keys(got)))
				}
			}
		})
	}
}

func TestCloudflareHeaderRuleLimit(t *testing.T) {
	dir := t.TempDir()
	ev := &collage.BuildFinishedEvent{OutDir: dir}
	for i := range 101 {
		ev.Files = append(ev.Files, ok(fmt.Sprintf("/p%03d/", i), "X-Page", fmt.Sprint(i)))
	}
	run(t, "cloudflare", ev)
	limit := findings(ev, "deploy-limit")
	if len(limit) != 1 || !strings.Contains(limit[0].Message, "1 are left out: /p100/") {
		t.Fatalf("findings = %+v", ev.Findings)
	}
	data, err := os.ReadFile(filepath.Join(dir, "_headers"))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, "/") {
			paths = append(paths, l)
		}
	}
	if len(paths) != 100 || paths[0] != "/p000/" || paths[99] != "/p099/" {
		t.Errorf("paths = %d, first %q last %q", len(paths), paths[0], paths[len(paths)-1])
	}
}

func TestCloudflareRedirectLimits(t *testing.T) {
	dir := t.TempDir()
	ev := &collage.BuildFinishedEvent{OutDir: dir}
	for i := range 51 {
		ev.Redirects = append(ev.Redirects, collage.BuiltRedirect{From: fmt.Sprintf("/d%02d/{x}", i), To: "/x/{x}", Status: 301, Source: "test"})
	}
	run(t, "cloudflare", ev)
	data, err := os.ReadFile(filepath.Join(dir, "_redirects"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 100 {
		t.Fatalf("%d lines", len(lines))
	}
	// Every rule's own spelling is kept before any trailing-slash twin.
	for i := range 51 {
		if !strings.Contains(string(data), fmt.Sprintf("/d%02d/:x /x/:x 301\n", i)) {
			t.Errorf("rule %d left out", i)
		}
	}
	limit := findings(ev, "deploy-limit")
	if len(limit) != 1 || !strings.Contains(limit[0].Message, "/d50/:x/ for /d50/{x}") {
		t.Errorf("findings = %+v", ev.Findings)
	}
}

func TestCloudflareCatchAllOverAPageIsLeftOut(t *testing.T) {
	dir := t.TempDir()
	ev := &collage.BuildFinishedEvent{OutDir: dir,
		Files:     []collage.BuiltFile{ok("/docs/", "A", "1")},
		Redirects: []collage.BuiltRedirect{{From: "/docs/{rest...}", To: "/manual/{rest}", Status: 301, Source: "test"}},
	}
	run(t, "cloudflare", ev)
	if w := findings(ev, "deploy-unsupported-redirect"); len(w) != 1 || !strings.Contains(w[0].Message, "/docs/ is a page") {
		t.Fatalf("findings = %+v", ev.Findings)
	}
	if _, err := os.Stat(filepath.Join(dir, "_redirects")); err == nil {
		t.Error("_redirects written")
	}
	// Netlify serves the file over a rule that is not forced: the rule is safe.
	dir = t.TempDir()
	ev.OutDir, ev.Findings = dir, nil
	run(t, "netlify", ev)
	if data, _ := os.ReadFile(filepath.Join(dir, "_redirects")); string(data) != "/docs/* /manual/:splat 301\n" {
		t.Errorf("netlify _redirects = %q", data)
	}
}

func TestPatternTranslation(t *testing.T) {
	cases := []struct {
		from, to                    string
		netlify, cloudflare, vercel string // "" = left out with a warning
	}{
		{"/a/{post-id}", "/b/{post-id}", "/a/:p1 /b/:p1 301\n", "/a/:p1 /b/:p1 301\n/a/:p1/ /b/:p1 301\n", `"/a/:p1" -> "/b/:p1"`},
		{"/a/{splat}/{rest...}", "/b/{splat}/{rest}", "/a/:p1/* /b/:p1/:splat 301\n", "/a/:p1/* /b/:p1/:splat 301\n", `"/a/:p1/:p2+" -> "/b/:p1/:p2+"`},
		{"/a/{slug}", "/b/{slug}_old", "", "", ""},
		{"/{slug}.md", "/posts/{slug}", "", "", `"/:slug.md" -> "/posts/:slug"`},
		{"/post-{id}", "/p/{id}", "", "", `"/post-:id" -> "/p/:id"`},
		{"/{id}abc", "/p/{id}", "", "", ""},
		{"/a b", "/c", "", "", `"/a b" -> "/c"`},
		{"/a(1)", "/c", "/a(1) /c 301\n", "/a(1) /c 301\n/a(1)/ /c 301\n", `"/a\\(1\\)" -> "/c"`},
		{"/a/{x}", "/b?q={x}", "/a/:x /b?q=:x 301\n", "/a/:x /b?q=:x 301\n/a/:x/ /b?q=:x 301\n", `"/a/:x" -> "/b?q=:x"`},
		{"/ext", "https://example.com/x", "/ext https://example.com/x 301\n", "/ext https://example.com/x 301\n/ext/ https://example.com/x 301\n", `"/ext" -> "https://example.com/x"`},
		{"/old/", "/new", "/old/ /new 301\n", "/old/ /new 301\n/old /new 301\n", `"/old/" -> "/new"`},
	}
	for _, c := range cases {
		for target, want := range map[string]string{"netlify": c.netlify, "cloudflare": c.cloudflare, "vercel": c.vercel} {
			dir := t.TempDir()
			ev := &collage.BuildFinishedEvent{OutDir: dir, Redirects: []collage.BuiltRedirect{{From: c.from, To: c.to, Status: 301, Source: "test"}}}
			run(t, target, ev)
			name := "_redirects"
			if target == "vercel" {
				name = "vercel.json"
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if want == "" {
				if err == nil {
					t.Errorf("%s %s: written although it cannot be:\n%s", target, c.from, data)
				}
				if w := findings(ev, "deploy-unsupported-redirect"); len(w) != 1 {
					t.Errorf("%s %s: findings = %+v", target, c.from, ev.Findings)
				}
				continue
			}
			if err != nil {
				t.Errorf("%s %s: %v; findings %+v", target, c.from, err, ev.Findings)
				continue
			}
			got := string(data)
			if target == "vercel" {
				var b strings.Builder
				for _, l := range strings.Split(got, "\n") {
					l = strings.TrimSpace(l)
					if v, ok := strings.CutPrefix(l, `"source": `); ok {
						b.WriteString(strings.TrimSuffix(v, ","))
					}
					if v, ok := strings.CutPrefix(l, `"destination": `); ok {
						b.WriteString(" -> " + strings.TrimSuffix(v, ","))
						break
					}
				}
				got = b.String()
			}
			if got != want {
				t.Errorf("%s %s -> %s:\n got %q\nwant %q", target, c.from, c.to, got, want)
			}
		}
	}
}

func TestRedirectsMostSpecificFirst(t *testing.T) {
	dir := t.TempDir()
	ev := &collage.BuildFinishedEvent{OutDir: dir, Redirects: []collage.BuiltRedirect{
		{From: "/docs/{rest...}", To: "/manual/{rest}", Status: 301},
		{From: "/docs/{page}", To: "/m/{page}", Status: 301},
		{From: "/docs/intro", To: "/start", Status: 301},
	}}
	run(t, "netlify", ev)
	data, _ := os.ReadFile(filepath.Join(dir, "_redirects"))
	want := "/docs/intro /start 301\n/docs/:page /m/:page 301\n/docs/* /manual/:splat 301\n"
	if string(data) != want {
		t.Errorf("_redirects =\n%s\nwant\n%s", data, want)
	}
}

func TestUnwritableHeaderPathIsLeftOut(t *testing.T) {
	dir := t.TempDir()
	ev := &collage.BuildFinishedEvent{OutDir: dir, Files: []collage.BuiltFile{
		ok("/a/", "X", "1", "Y", "a"),
		ok("/b/:c.txt", "X", "1", "Y", "b"),
	}}
	run(t, "netlify", ev)
	if w := findings(ev, "deploy-unsupported-header"); len(w) != 1 || !strings.Contains(w[0].Message, "/b/:c.txt") {
		t.Fatalf("findings = %+v", ev.Findings)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "_headers"))
	if want := "/*\n  X: 1\n\n/a/\n  Y: a\n"; string(data) != want {
		t.Errorf("_headers = %q", data)
	}
}

func TestGitHubPagesRefreshFiles(t *testing.T) {
	dir := t.TempDir()
	ev := &collage.BuildFinishedEvent{OutDir: dir, Redirects: []collage.BuiltRedirect{
		{From: "/a.html", To: "/b?x=1&y=\"2\"", Status: 302},
		{From: "/c/", To: "https://example.com/", Status: 301},
		{From: "/feed.rss", To: "/feed.xml", Status: 301},
	}}
	run(t, "github-pages", ev)
	got := tree(t, dir)
	if _, ok := got["a.html"]; !ok {
		t.Errorf("a.html not written: %v", got)
	}
	if !bytes.Contains(got["a.html"], []byte(`content="0; url=/b?x=1&amp;y=&#34;2&#34;"`)) {
		t.Errorf("a.html = %s", got["a.html"])
	}
	if _, ok := got["c/index.html"]; !ok {
		t.Errorf("c/index.html not written")
	}
	if _, ok := got["feed.rss"]; ok {
		t.Error("a meta-refresh page was written as feed.rss")
	}
	if w := findings(ev, "deploy-unsupported-redirect"); len(w) != 1 || !strings.Contains(w[0].Message, "/feed.rss") {
		t.Errorf("findings = %+v", ev.Findings)
	}
}

func TestGitHubPagesKeepsAnExistingNoJekyll(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".nojekyll"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := &collage.BuildFinishedEvent{OutDir: dir}
	run(t, "github-pages", ev)
	if data, _ := os.ReadFile(filepath.Join(dir, ".nojekyll")); string(data) != "mine" {
		t.Errorf(".nojekyll = %q", data)
	}
	if len(ev.Findings) != 0 {
		t.Errorf("findings = %+v", ev.Findings)
	}
}

func TestGitHubPagesNeverWritesOutsideTheOutput(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "out")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ev := &collage.BuildFinishedEvent{OutDir: dir, Redirects: []collage.BuiltRedirect{
		{From: "/../escape", To: "/x", Status: 301},
		{From: "/a/../../escape2.html", To: "/x", Status: 301},
		{From: "/./b", To: "/x", Status: 301},
		{From: `/c\..\..\escape3`, To: "/x", Status: 301},
	}}
	run(t, "github-pages", ev)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("written beside the output: %v", entries)
	}
	if got := tree(t, dir); len(got) != 1 {
		t.Errorf("written: %v", slices.Sorted(maps.Keys(got)))
	}
	if w := findings(ev, "deploy-unsupported-redirect"); len(w) != 1 || !strings.Contains(w[0].Message, "/../escape") {
		t.Errorf("findings = %+v", ev.Findings)
	}
}

func TestLongerAffixRanksFirst(t *testing.T) {
	dir := t.TempDir()
	ev := &collage.BuildFinishedEvent{OutDir: dir, Redirects: []collage.BuiltRedirect{
		{From: "/{slug}", To: "/s/{slug}", Status: 301},
		{From: "/post-{id}", To: "/p/{id}", Status: 301},
		{From: "/post-{id}.md", To: "/p/{id}/md", Status: 301},
		{From: "/about", To: "/a", Status: 301},
	}}
	run(t, "vercel", ev)
	data, err := os.ReadFile(filepath.Join(dir, "vercel.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sources []string
	for _, l := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), `"source": `); ok {
			sources = append(sources, strings.Trim(strings.TrimSuffix(v, ","), `"`))
		}
	}
	want := []string{"/about", "/about/", "/post-:id.md", "/post-:id.md/", "/post-:id", "/post-:id/", "/:slug", "/:slug/"}
	if !slices.Equal(sources, want) {
		t.Errorf("sources = %v\nwant      %v", sources, want)
	}
}

func TestVercelAllowsAPortInAnAbsoluteDestination(t *testing.T) {
	dir := t.TempDir()
	ev := &collage.BuildFinishedEvent{OutDir: dir, Redirects: []collage.BuiltRedirect{
		{From: "/a/{x}", To: "http://h:8080/x/{x}", Status: 301},
		{From: "/b", To: "/c/:d", Status: 301},
	}}
	run(t, "vercel", ev)
	data, _ := os.ReadFile(filepath.Join(dir, "vercel.json"))
	if !bytes.Contains(data, []byte(`"destination": "http://h:8080/x/:x"`)) {
		t.Errorf("vercel.json = %s", data)
	}
	if w := findings(ev, "deploy-unsupported-redirect"); len(w) != 1 || !strings.Contains(w[0].Message, "/b") || strings.Contains(w[0].Message, "/a/{x}") {
		t.Errorf("findings = %+v", ev.Findings)
	}
	// _redirects stays cautious.
	ev = &collage.BuildFinishedEvent{OutDir: t.TempDir(), Redirects: ev.Redirects}
	run(t, "netlify", ev)
	if w := findings(ev, "deploy-unsupported-redirect"); len(w) != 1 || !strings.Contains(w[0].Message, "/a/{x}") {
		t.Errorf("netlify findings = %+v", ev.Findings)
	}
}

func TestVercelRouteLimitWording(t *testing.T) {
	ev := &collage.BuildFinishedEvent{OutDir: t.TempDir()}
	for i := range 1025 {
		ev.Redirects = append(ev.Redirects, collage.BuiltRedirect{From: fmt.Sprintf("/r%d", i), To: "/x", Status: 301})
	}
	run(t, "vercel", ev)
	w := findings(ev, "deploy-limit")
	if len(w) != 1 || strings.Contains(w[0].Message, "refuse") || !strings.Contains(w[0].Message, "up to 2048 routes") {
		t.Errorf("findings = %+v", ev.Findings)
	}
}

func TestPlaceholderInDestinationAuthorityIsAnError(t *testing.T) {
	for _, target := range deploy.Targets {
		for _, to := range []string{"http://{x}.example.com/p", "http://h:{x}/p"} {
			dir := t.TempDir()
			from := "/a/{x}"
			if target == "github-pages" {
				from = "/a"
			}
			ev := &collage.BuildFinishedEvent{OutDir: dir, Redirects: []collage.BuiltRedirect{
				{From: from, To: to, Status: 301, Source: "elagoht/hosts"},
				{From: "/ok", To: "/fine", Status: 301, Source: "page:ok"},
			}}
			run(t, target, ev)
			errs := findings(ev, "deploy-unsafe-redirect")
			if len(errs) != 1 || errs[0].Level != collage.FindingError || !strings.Contains(errs[0].Message, "elagoht/hosts") || !strings.Contains(errs[0].Message, to) {
				t.Errorf("%s %s: findings = %+v", target, to, ev.Findings)
			}
			for name, data := range tree(t, dir) {
				if bytes.Contains(data, []byte("example.com")) || bytes.Contains(data, []byte("h:")) && !bytes.Contains(data, []byte("http-equiv")) {
					t.Errorf("%s %s: %s holds the rule:\n%s", target, to, name, data)
				}
			}
		}
	}
}

// TestGitHubPagesTwoRedirectsOnOneFile: "/old" and "/old/index.html" are two
// redirects to the router and one file, old/index.html, to GitHub Pages. The
// second is an error naming both, not one saying the file was already there.
func TestGitHubPagesTwoRedirectsOnOneFile(t *testing.T) {
	dir := t.TempDir()
	ev := &collage.BuildFinishedEvent{OutDir: dir, Redirects: []collage.BuiltRedirect{
		{From: "/old", To: "/a", Status: 301, Source: "page:a"},
		{From: "/old/index.html", To: "/b", Status: 301, Source: "elagoht/redirects"},
	}}
	run(t, "github-pages", ev)
	errs := findings(ev, "deploy-existing-file")
	if len(errs) != 1 || errs[0].Level != collage.FindingError {
		t.Fatalf("findings = %+v", ev.Findings)
	}
	msg := errs[0].Message
	for _, want := range []string{"/old (page:a)", "/old/index.html (elagoht/redirects)", "old/index.html"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not name %q", msg, want)
		}
	}
	if strings.Contains(msg, "already in the output") {
		t.Errorf("message %q blames the output for the plugin's own page", msg)
	}
	if got := read(t, dir, "old/index.html"); !strings.Contains(got, `url=/a"`) {
		t.Errorf("old/index.html = %s, want the first redirect's page", got)
	}
}
