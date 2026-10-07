package deploy_test

import (
	"math/rand"
	"net/http"
	"reflect"
	"slices"
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

func assertExact(t *testing.T, files []collage.BuiltFile) {
	t.Helper()
	rules := deploy.Compact(files)
	for _, f := range files {
		if f.Status < 200 || f.Status > 299 || f.Headers == nil {
			continue
		}
		got := deploy.Expand(rules, deploy.ServedPath(f.Path))
		if !reflect.DeepEqual(got, f.Headers) {
			t.Errorf("Expand(%s) = %v, want %v\nrules: %v", f.Path, got, f.Headers, rules)
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
	rules := deploy.Compact(files)
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
	rules := deploy.Compact(files)
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
	rules := deploy.Compact(files)
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
	rules := deploy.Compact(files)
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
	rules := deploy.Compact(files)
	if find(rules, "/blog/*") == nil {
		t.Fatalf("rules = %v", rules)
	}
	assertExact(t, files)
}

func TestNotCapturedContributeNothing(t *testing.T) {
	files := []collage.BuiltFile{
		ok("/", "A", "1"),
		ok("/b", "A", "1"),
		file("/uncaptured", 0, http.Header{"A": {"9"}}),
		file("/nilheaders", 200, nil),
		file("/old", 301, http.Header{"Location": {"/new"}, "A": {"7"}}),
		file("/gone", 404, http.Header{"A": {"7"}}),
	}
	rules := deploy.Compact(files)
	if len(rules) != 1 || rules[0].Path != "/*" || rules[0].Headers.Get("A") != "1" {
		t.Fatalf("rules = %v", rules)
	}
	if deploy.Compact(nil) != nil || deploy.Compact(files[2:]) != nil {
		t.Error("nothing captured should give no rules")
	}
}

func TestLoneFileKeepsItsOwnRule(t *testing.T) {
	files := []collage.BuiltFile{ok("/only", "A", "1")}
	rules := deploy.Compact(files)
	if len(rules) != 1 || rules[0].Path != "/only" {
		t.Fatalf("rules = %v", rules)
	}
	assertExact(t, files)
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
	want := deploy.Compact(files)
	rng := rand.New(rand.NewSource(1))
	for range 20 {
		shuffled := slices.Clone(files)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if got := deploy.Compact(shuffled); !reflect.DeepEqual(got, want) {
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
	for _, r := range deploy.Compact(files) {
		paths = append(paths, r.Path)
	}
	if want := []string{"/*", "/static/*", "/a", "/z"}; !slices.Equal(paths, want) {
		t.Fatalf("order = %v, want %v", paths, want)
	}
}

// A randomised site: whatever the headers, a host applying the rules must give
// every captured file exactly its own.
func TestExpandReproducesEveryFileProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	dirs := []string{"/", "/a/", "/a/b/", "/c/", "/c/d/e/"}
	names := []string{"A", "B", "C", "Cache-Control"}
	vals := []string{"1", "2"}
	for trial := range 300 {
		var files []collage.BuiltFile
		seen := map[string]bool{}
		for i := range 2 + rng.Intn(10) {
			p := dirs[rng.Intn(len(dirs))] + string(rune('a'+i))
			if rng.Intn(4) == 0 {
				p += "/"
			}
			if seen[p] {
				continue
			}
			seen[p] = true
			h := http.Header{}
			for _, n := range names {
				if rng.Intn(3) > 0 {
					h.Add(n, vals[rng.Intn(len(vals))])
					if rng.Intn(5) == 0 {
						h.Add(n, "extra")
					}
				}
			}
			status := 200
			if rng.Intn(8) == 0 {
				status = 404
			}
			files = append(files, file(p, status, h))
		}
		t.Run("", func(t *testing.T) {
			assertExact(t, files)
		})
		_ = trial
	}
}
