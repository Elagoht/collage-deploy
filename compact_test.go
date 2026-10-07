package deploy_test

import (
	"fmt"
	"math/rand"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	deploy "github.com/Elagoht/collage-deploy"
	"github.com/Elagoht/collage/pkg/collage"
)

const (
	immutable = "public, max-age=31536000, immutable"
	nosniff   = "nosniff"
)

func file(path string, status int, h http.Header) collage.BuiltFile {
	return collage.BuiltFile{Path: path, Status: status, Headers: h}
}

func ok(path string, kv ...string) collage.BuiltFile {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return file(path, 200, h)
}

func find(rules []deploy.HeaderRule, path string) *deploy.HeaderRule {
	for i := range rules {
		if rules[i].Path == path {
			return &rules[i]
		}
	}
	return nil
}

func isCaptured(f collage.BuiltFile) bool {
	return f.Status >= 200 && f.Status <= 299 && f.Headers != nil
}

// assertExact holds Compact to its policy: a host applying the rules gives every
// captured 2xx file exactly its own headers, never sets one header name in two
// rules for one file, and gives a file that was not asked for (status 0) nothing
// but the "/*" headers.
func assertExact(t *testing.T, files []collage.BuiltFile) {
	t.Helper()
	assertExactIn(t, "", files)
}

// assertExactIn is assertExact for files written under out, whose served paths
// come from their File.
func assertExactIn(t *testing.T, out string, files []collage.BuiltFile) {
	t.Helper()
	rules := deploy.Compact(out, files)
	root := http.Header{}
	if r := find(rules, "/*"); r != nil {
		root = r.Headers
	}
	for _, r := range rules {
		if r.Path == "/*" || strings.HasSuffix(r.Path, "*") {
			for _, name := range []string{"Content-Type", "Content-Disposition", "Content-Language"} {
				if r.Headers.Get(name) != "" {
					t.Errorf("wildcard %s carries %s: %v", r.Path, name, r.Headers)
				}
			}
		}
	}
	for _, f := range files {
		served := deploy.ServedAt(out, f)
		switch {
		case isCaptured(f):
			if got := deploy.Expand(rules, served); !reflect.DeepEqual(got, f.Headers) {
				t.Errorf("Expand(%s) = %v, want %v\nrules: %v", f.Path, got, f.Headers, rules)
			}
			seen := map[string]string{}
			for _, r := range rules {
				if !strings.HasSuffix(r.Path, "*") && r.Path != served {
					continue
				}
				if strings.HasSuffix(r.Path, "*") && !strings.HasPrefix(served, strings.TrimSuffix(r.Path, "*")) {
					continue
				}
				for name := range r.Headers {
					if prev, dup := seen[name]; dup {
						t.Errorf("%s: header %s is set by both %s and %s", f.Path, name, prev, r.Path)
					}
					seen[name] = r.Path
				}
			}
		case f.Captured && f.Status == 0:
			if got := deploy.Expand(rules, served); len(got) != 0 {
				t.Errorf("%s, whose capture failed, gets %v from the rules", f.Path, got)
			}
		case !f.Captured && (f.Status == 0 || f.Headers == nil):
			if got := deploy.Expand(rules, served); !reflect.DeepEqual(got, root) {
				t.Errorf("uncaptured %s gets %v, want only the /* headers %v", f.Path, got, root)
			}
		}
	}
}

func TestServedPath(t *testing.T) {
	for in, want := range map[string]string{
		"/":                "/",
		"index.html":       "/",
		"/index.html":      "/",
		"blog/index.html":  "/blog/",
		"/blog/":           "/blog/",
		"/feed.xml":        "/feed.xml",
		"static/app.css":   "/static/app.css",
		"/a/b/index.html":  "/a/b/",
		"/a/notindex.html": "/a/notindex.html",
	} {
		if got := deploy.ServedPath(in); got != want {
			t.Errorf("ServedPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSharedHeaderGoesToRoot(t *testing.T) {
	files := []collage.BuiltFile{
		ok("/", "X-Content-Type-Options", nosniff, "Content-Type", "text/html"),
		ok("/about/", "X-Content-Type-Options", nosniff, "Content-Type", "text/html"),
		ok("/feed.xml", "X-Content-Type-Options", nosniff, "Content-Type", "application/xml"),
	}
	rules := deploy.Compact("", files)
	root := find(rules, "/*")
	if root == nil || root.Headers.Get("X-Content-Type-Options") != nosniff || len(root.Headers) != 1 {
		t.Fatalf("root rule = %v", root)
	}
	for _, r := range rules[1:] {
		if r.Headers.Get("X-Content-Type-Options") != "" {
			t.Errorf("rule %s repeats the shared header", r.Path)
		}
	}
	assertExact(t, files)
}

func TestDirectoryWildcard(t *testing.T) {
	files := []collage.BuiltFile{
		ok("/", "X-Content-Type-Options", nosniff),
		ok("/static/app.3f9a.css", "X-Content-Type-Options", nosniff, "Cache-Control", immutable),
		ok("/static/logo.1b2c.png", "X-Content-Type-Options", nosniff, "Cache-Control", immutable),
	}
	rules := deploy.Compact("", files)
	if len(rules) != 2 || rules[0].Path != "/*" || rules[1].Path != "/static/*" {
		t.Fatalf("rules = %v", rules)
	}
	assertExact(t, files)
}

func TestFileWithOwnHeadersGetsOwnRule(t *testing.T) {
	files := []collage.BuiltFile{
		ok("/", "X-Content-Type-Options", nosniff),
		ok("/feed.xml", "X-Content-Type-Options", nosniff, "Content-Type", "application/atom+xml"),
	}
	rules := deploy.Compact("", files)
	r := find(rules, "/feed.xml")
	if r == nil || r.Headers.Get("Content-Type") != "application/atom+xml" {
		t.Fatalf("rules = %v", rules)
	}
	assertExact(t, files)
}

func TestDifferingDirectoryGetsNoWildcard(t *testing.T) {
	files := []collage.BuiltFile{
		ok("/", "X-Content-Type-Options", nosniff),
		ok("/static/a.css", "X-Content-Type-Options", nosniff, "Cache-Control", immutable),
		ok("/static/b.css", "X-Content-Type-Options", nosniff, "Cache-Control", immutable),
		ok("/static/c.txt", "X-Content-Type-Options", nosniff, "Cache-Control", "no-cache"),
	}
	rules := deploy.Compact("", files)
	if find(rules, "/static/*") != nil {
		t.Fatalf("wildcard emitted for a differing directory: %v", rules)
	}
	for _, p := range []string{"/static/a.css", "/static/b.css", "/static/c.txt"} {
		if find(rules, p) == nil {
			t.Errorf("no rule for %s", p)
		}
	}
	assertExact(t, files)
}

func TestNestedDirectories(t *testing.T) {
	files := []collage.BuiltFile{
		ok("/", "A", "1"),
		ok("/x/a", "A", "1", "B", "2"),
		ok("/x/b", "A", "1", "B", "2"),
		ok("/x/y/c", "A", "1", "B", "2"),
		ok("/x/y/d", "A", "1", "B", "3"),
		ok("/p/q/e", "A", "1", "C", "4"),
		ok("/p/q/f", "A", "1", "C", "4"),
	}
	assertExact(t, files)
}

func TestIndexPageUnderItsOwnDirectory(t *testing.T) {
	files := []collage.BuiltFile{
		ok("/", "A", "1"),
		ok("/blog/", "A", "1", "B", "2"),
		ok("/blog/post/", "A", "1", "B", "2"),
	}
	rules := deploy.Compact("", files)
	if find(rules, "/blog/*") == nil {
		t.Fatalf("rules = %v", rules)
	}
	assertExact(t, files)
}

func TestUncapturedFilesGetOnlyRootHeaders(t *testing.T) {
	files := []collage.BuiltFile{
		ok("/", "A", "1"),
		ok("/b", "A", "1", "B", "2"),
		file("/uncaptured", 0, http.Header{"A": {"9"}}),
		file("/nilheaders", 200, nil),
		file("/404.html", 0, nil),
	}
	rules := deploy.Compact("", files)
	if root := find(rules, "/*"); root == nil || root.Headers.Get("A") != "1" {
		t.Fatalf("rules = %v", rules)
	}
	assertExact(t, files)
	if got := deploy.Expand(rules, "/404.html"); !reflect.DeepEqual(got, http.Header{"A": {"1"}}) {
		t.Errorf("404 page gets %v", got)
	}
}

func TestCapturedNon2xxBlocksRoot(t *testing.T) {
	files := []collage.BuiltFile{
		ok("/", "A", "1"),
		ok("/b", "A", "1"),
		file("/old", 301, http.Header{"Location": {"/new"}}),
	}
	rules := deploy.Compact("", files)
	if find(rules, "/*") != nil {
		t.Fatalf("/* emitted beside a captured redirect: %v", rules)
	}
	if find(rules, "/") == nil || find(rules, "/b") == nil {
		t.Fatalf("expected per-path rules: %v", rules)
	}
	assertExact(t, files)
	if got := deploy.Expand(rules, "/old"); len(got) != 0 {
		t.Errorf("the redirect gets %v", got)
	}
}

func TestUncapturedFileBlocksDirectoryWildcard(t *testing.T) {
	files := []collage.BuiltFile{
		ok("/static/a.css", "Cache-Control", immutable),
		ok("/static/b.css", "Cache-Control", immutable),
		file("/static/raw.bin", 0, nil),
	}
	if r := find(deploy.Compact("", files), "/static/*"); r != nil {
		t.Fatalf("wildcard reaches an uncaptured file: %v", r)
	}
	assertExact(t, files)
}

func TestNon2xxFileBlocksDirectoryWildcard(t *testing.T) {
	files := []collage.BuiltFile{
		ok("/docs/a", "A", "1"),
		ok("/docs/b", "A", "1"),
		file("/docs/gone", 404, http.Header{"A": {"1"}}),
	}
	if r := find(deploy.Compact("", files), "/docs/*"); r != nil {
		t.Fatalf("wildcard reaches a 404: %v", r)
	}
	assertExact(t, files)
}

func TestNothingCapturedGivesNoRules(t *testing.T) {
	if deploy.Compact("", nil) != nil {
		t.Error("nil")
	}
	if got := deploy.Compact("", []collage.BuiltFile{file("/a", 0, nil), file("/b", 404, http.Header{"A": {"1"}})}); got != nil {
		t.Errorf("rules = %v", got)
	}
}

func TestLoneFileGoesToRoot(t *testing.T) {
	files := []collage.BuiltFile{ok("/only", "A", "1"), file("/404.html", 0, nil)}
	rules := deploy.Compact("", files)
	if len(rules) != 1 || rules[0].Path != "/*" {
		t.Fatalf("rules = %v", rules)
	}
	assertExact(t, files)
}

func TestTwoSpellingsOfOneHeaderMergeDeterministically(t *testing.T) {
	a := file("/x", 200, http.Header{"x-a": {"1"}, "X-A": {"2"}})
	want := deploy.Compact("", []collage.BuiltFile{a})
	for range 30 {
		if got := deploy.Compact("", []collage.BuiltFile{a}); !reflect.DeepEqual(got, want) {
			t.Fatalf("merge order varies: %v vs %v", got, want)
		}
	}
	if len(want) != 1 || len(want[0].Headers["X-A"]) != 2 {
		t.Fatalf("rules = %v", want)
	}
}

func TestValueOrderAndMultipleValuesSurvive(t *testing.T) {
	files := []collage.BuiltFile{
		ok("/a", "Link", "<b>; rel=preload", "Link", "<a>; rel=preload"),
		ok("/b", "Link", "<b>; rel=preload", "Link", "<a>; rel=preload"),
		ok("/c", "Link", "<a>; rel=preload", "Link", "<b>; rel=preload"),
	}
	assertExact(t, files)
}

func TestDeterministicUnderShuffle(t *testing.T) {
	files := []collage.BuiltFile{
		ok("/", "X-Content-Type-Options", nosniff),
		ok("/about/", "X-Content-Type-Options", nosniff, "Content-Language", "en"),
		ok("/static/a.css", "X-Content-Type-Options", nosniff, "Cache-Control", immutable),
		ok("/static/b.css", "X-Content-Type-Options", nosniff, "Cache-Control", immutable),
		ok("/static/img/c.png", "X-Content-Type-Options", nosniff, "Cache-Control", immutable),
		ok("/feed.xml", "X-Content-Type-Options", nosniff, "Content-Type", "application/xml"),
	}
	want := deploy.Compact("", files)
	rng := rand.New(rand.NewSource(1))
	for range 20 {
		shuffled := slices.Clone(files)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if got := deploy.Compact("", shuffled); !reflect.DeepEqual(got, want) {
			t.Fatalf("order changed the rules:\n got %v\nwant %v", got, want)
		}
	}
	assertExact(t, files)
}

func TestRuleOrder(t *testing.T) {
	files := []collage.BuiltFile{
		ok("/z", "A", "1", "Z", "1"),
		ok("/static/a", "A", "1", "S", "1"),
		ok("/static/b", "A", "1", "S", "1"),
		ok("/a", "A", "1", "F", "1"),
	}
	var paths []string
	for _, r := range deploy.Compact("", files) {
		paths = append(paths, r.Path)
	}
	if want := []string{"/*", "/static/*", "/a", "/z"}; !slices.Equal(paths, want) {
		t.Fatalf("order = %v, want %v", paths, want)
	}
}

// A randomised site: whatever the headers, a host applying the rules must give
// every captured file exactly its own, and an uncaptured one only "/*". Each
// directory draws one shared header set that its files seldom stray from, so
// wildcards are common.
func TestExpandReproducesEveryFileProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	dirs := []string{"/", "/a/", "/a/b/", "/c/", "/c/d/e/"}
	names := []string{"A", "B", "C", "Cache-Control", "Content-Type"}
	vals := []string{"1", "2"}
	withWildcard := 0
	for trial := range 300 {
		shared := map[string]http.Header{}
		for _, d := range dirs {
			h := http.Header{}
			for _, n := range names {
				if rng.Intn(2) == 0 {
					h.Add(n, vals[rng.Intn(len(vals))])
				}
			}
			shared[d] = h
		}
		var files []collage.BuiltFile
		seen := map[string]bool{}
		for i := range 3 + rng.Intn(10) {
			d := dirs[rng.Intn(len(dirs))]
			p := d + string(rune('a'+i))
			if rng.Intn(4) == 0 {
				p += "/"
			}
			if seen[p] {
				continue
			}
			seen[p] = true
			h := shared[d].Clone()
			if rng.Intn(12) == 0 {
				h.Add(names[rng.Intn(len(names))], "stray")
			}
			status := 200
			switch rng.Intn(14) {
			case 0:
				status = 404
			case 1:
				status, h = 0, nil
			case 2:
				status = 301
			}
			files = append(files, file(p, status, h))
		}
		rules := deploy.Compact("", files)
		for _, r := range rules {
			if r.Path != "/*" && strings.HasSuffix(r.Path, "*") {
				withWildcard++
				break
			}
		}
		t.Run(fmt.Sprintf("site-%03d", trial), func(t *testing.T) { assertExact(t, files) })
	}
	if withWildcard < 50 {
		t.Errorf("only %d of 300 sites produced a directory wildcard; the property is not exercising them", withWildcard)
	}
}

// page is a page registered at path, as collage registers one by default — no
// trailing slash — written to its directory's index.html under out.
func page(out, path string, kv ...string) collage.BuiltFile {
	f := ok(path, kv...)
	f.Kind = "page"
	f.File = filepath.Join(out, filepath.FromSlash(strings.TrimPrefix(path, "/")), "index.html")
	return f
}

// TestServedAt: a file's served path comes from where it was written, so a page
// registered as "/about" is served at "/about/"; with no File, from its Path.
func TestServedAt(t *testing.T) {
	out := filepath.Join(string(filepath.Separator), "out")
	for _, tc := range []struct {
		f    collage.BuiltFile
		want string
	}{
		{page(out, "/about"), "/about/"},
		{page(out, "/"), "/"},
		{collage.BuiltFile{Path: "/feed.xml", File: filepath.Join(out, "feed.xml")}, "/feed.xml"},
		{collage.BuiltFile{Path: "/en/404.html", File: filepath.Join(out, "en", "404.html")}, "/en/404.html"},
		{collage.BuiltFile{Path: "/blog/index.html"}, "/blog/"},
		{collage.BuiltFile{Path: "/about"}, "/about"},
	} {
		if got := deploy.ServedAt(out, tc.f); got != tc.want {
			t.Errorf("ServedAt(%q, %+v) = %q, want %q", out, tc.f, got, tc.want)
		}
	}
}

// TestASectionIndexKeepsItsOwnHeaders: pages registered without a trailing
// slash — "/blog" beside "/blog/a" and "/blog/b" — are served at "/blog/",
// "/blog/a/" and "/blog/b/". A "/blog/*" carrying the posts' headers would
// reach the section index at "/blog/", so the index's headers have to be
// judged under the directory too.
func TestASectionIndexKeepsItsOwnHeaders(t *testing.T) {
	out := filepath.Join(string(filepath.Separator), "out")
	files := []collage.BuiltFile{
		page(out, "/", "X-Section", "index"),
		page(out, "/blog", "X-Section", "index"),
		page(out, "/blog/a", "X-Section", "post"),
		page(out, "/blog/b", "X-Section", "post"),
	}
	rules := deploy.Compact(out, files)
	if got := deploy.Expand(rules, "/blog/").Get("X-Section"); got != "index" {
		t.Errorf("/blog/ is served X-Section %q, want index\nrules: %v", got, rules)
	}
	if find(rules, "/blog") != nil {
		t.Errorf("a rule is written at /blog, which the host never serves the page at: %v", rules)
	}
	assertExactIn(t, out, files)
}

// TestPerPathHeadersNeverGoInAWildcard: Content-Type, Content-Disposition and
// Content-Language describe one file. "/*" and "/dir/*" also reach the files
// other plugins write into the output — share cards, a search index — so these
// stay at each file's own path even when every captured file shares them.
func TestPerPathHeadersNeverGoInAWildcard(t *testing.T) {
	files := []collage.BuiltFile{
		ok("/", "Content-Type", html, "Content-Language", "en", "Content-Disposition", "inline", "X-Content-Type-Options", nosniff),
		ok("/about/", "Content-Type", html, "Content-Language", "en", "Content-Disposition", "inline", "X-Content-Type-Options", nosniff),
		ok("/static/a.css", "Content-Type", "text/css", "Content-Language", "en", "Content-Disposition", "inline", "X-Content-Type-Options", nosniff, "Cache-Control", immutable),
		ok("/static/b.css", "Content-Type", "text/css", "Content-Language", "en", "Content-Disposition", "inline", "X-Content-Type-Options", nosniff, "Cache-Control", immutable),
	}
	rules := deploy.Compact("", files)
	if r := find(rules, "/*"); r == nil || r.Headers.Get("X-Content-Type-Options") != nosniff {
		t.Errorf("rules = %v, want X-Content-Type-Options under /*", rules)
	}
	if r := find(rules, "/static/*"); r == nil || r.Headers.Get("Cache-Control") != immutable {
		t.Errorf("rules = %v, want Cache-Control under /static/*", rules)
	}
	for _, p := range []string{"/", "/about/", "/static/a.css", "/static/b.css"} {
		if r := find(rules, p); r == nil || r.Headers.Get("Content-Type") == "" || r.Headers.Get("Content-Language") == "" || r.Headers.Get("Content-Disposition") == "" {
			t.Errorf("rule for %s = %v, want its own Content-Type, Content-Language and Content-Disposition", p, r)
		}
	}
	assertExact(t, files)
}

// TestAFailedCaptureBlocksWildcards: a file the build asked for and got no
// answer for has headers nobody knows. It blocks "/*" and its directory's
// wildcard, as a file answered with a redirect or an error does, so the headers
// the others share are not handed to it.
func TestAFailedCaptureBlocksWildcards(t *testing.T) {
	failed := collage.BuiltFile{Path: "/static/c.css", Captured: true}
	files := []collage.BuiltFile{
		ok("/static/a.css", "Cache-Control", immutable),
		ok("/static/b.css", "Cache-Control", immutable),
		failed,
	}
	for i := range files[:2] {
		files[i].Captured = true
	}
	rules := deploy.Compact("", files)
	if find(rules, "/*") != nil || find(rules, "/static/*") != nil {
		t.Fatalf("a wildcard reaches the file whose capture failed: %v", rules)
	}
	assertExact(t, files)

	// The same file not asked for — one the build made itself — is reached by
	// "/*", as before.
	files[2].Captured = false
	if find(deploy.Compact("", files), "/*") == nil {
		t.Errorf("a file the build made itself blocks /*")
	}
}
