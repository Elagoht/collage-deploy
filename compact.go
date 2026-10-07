package deploy

import (
	"net/http"
	"net/textproto"
	"slices"
	"sort"
	"strings"

	"github.com/Elagoht/collage/pkg/collage"
)

// HeaderRule is a set of headers a host applies to every path its Path matches:
// "/*" for every path, "/static/*" for everything under /static/, "/a/" for that
// one path.
type HeaderRule struct {
	Path    string
	Headers http.Header
}

// ServedPath returns the path a host serves a built file at: the path with a
// leading slash, and "index.html" dropped, so "blog/index.html" is "/blog/". The
// build already names files this way; the function makes the spelling certain for
// every writer that matches on it.
func ServedPath(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if dir, ok := strings.CutSuffix(path, "/index.html"); ok {
		return dir + "/"
	}
	return path
}

// captured is a file whose headers the build recorded, canonicalised.
type captured struct {
	path    string
	headers http.Header
}

// capturedFiles keeps the files a host should carry headers for: those the build
// asked for with a 2xx status and headers. A redirect's or an error page's headers
// are not what the path's own answer carries, so they are left out.
func capturedFiles(files []collage.BuiltFile) []captured {
	var out []captured
	for _, f := range files {
		if f.Status < 200 || f.Status > 299 || f.Headers == nil {
			continue
		}
		h := http.Header{}
		for name, values := range f.Headers {
			if len(values) == 0 {
				continue
			}
			key := textproto.CanonicalMIMEHeaderKey(name)
			h[key] = append(h[key], values...)
		}
		out = append(out, captured{path: ServedPath(f.Path), headers: h})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].path != out[j].path {
			return out[i].path < out[j].path
		}
		return headerKey(out[i].headers) < headerKey(out[j].headers)
	})
	return out
}

// headerKey is a header set as one comparable string.
func headerKey(h http.Header) string {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		for _, v := range h[name] {
			b.WriteByte(0)
			b.WriteString(v)
		}
		b.WriteByte(1)
	}
	return b.String()
}

// Compact turns the headers each file was answered with into the fewest rules
// that give every captured file exactly its own headers back: what every file
// shares in "/*", what every file of a directory shares in "/dir/*", the rest at
// the file's own path. The result does not depend on the order of files.
func Compact(files []collage.BuiltFile) []HeaderRule {
	caps := capturedFiles(files)
	if len(caps) == 0 {
		return nil
	}
	var rules []HeaderRule

	// 1. What every file carries. A lone file's headers stay at its own path: "/*"
	// would also reach the files the build did not capture.
	if len(caps) >= 2 {
		common := http.Header{}
		for name, values := range caps[0].headers {
			shared := true
			for _, c := range caps[1:] {
				if !slices.Equal(c.headers[name], values) {
					shared = false
					break
				}
			}
			if shared {
				common[name] = slices.Clone(values)
			}
		}
		if len(common) > 0 {
			rules = append(rules, HeaderRule{Path: "/*", Headers: common})
			for _, c := range caps {
				for name := range common {
					delete(c.headers, name)
				}
			}
		}
	}

	// 2. Directories, deepest first: one holding two or more files that all
	// carry the same non-empty remainder gets it as a wildcard.
	dirs := map[string]bool{}
	for _, c := range caps {
		for i := 1; i < len(c.path); i++ {
			if c.path[i] == '/' {
				dirs[c.path[:i+1]] = true
			}
		}
	}
	ordered := make([]string, 0, len(dirs))
	for d := range dirs {
		ordered = append(ordered, d)
	}
	sort.Slice(ordered, func(i, j int) bool {
		di, dj := strings.Count(ordered[i], "/"), strings.Count(ordered[j], "/")
		if di != dj {
			return di > dj
		}
		return ordered[i] < ordered[j]
	})
	var wildcards []HeaderRule
	for _, d := range ordered {
		var under []captured
		for _, c := range caps {
			if strings.HasPrefix(c.path, d) {
				under = append(under, c)
			}
		}
		if len(under) < 2 || len(under[0].headers) == 0 {
			continue
		}
		key := headerKey(under[0].headers)
		same := true
		for _, c := range under[1:] {
			if headerKey(c.headers) != key {
				same = false
				break
			}
		}
		if !same {
			continue
		}
		shared := http.Header{}
		for name, values := range under[0].headers {
			shared[name] = slices.Clone(values)
		}
		wildcards = append(wildcards, HeaderRule{Path: d + "*", Headers: shared})
		for _, c := range under {
			clear(c.headers)
		}
	}
	sort.Slice(wildcards, func(i, j int) bool { return wildcards[i].Path < wildcards[j].Path })
	rules = append(rules, wildcards...)

	// 3. What is left is the file's own.
	for _, c := range caps {
		if len(c.headers) > 0 {
			rules = append(rules, HeaderRule{Path: c.path, Headers: c.headers})
		}
	}
	return rules
}

// Expand returns the headers a host applies to path under rules: every rule that
// matches, in order, a later rule replacing the values of a header an earlier one
// set. path is a served path, as ServedPath spells it.
func Expand(rules []HeaderRule, path string) http.Header {
	out := http.Header{}
	for _, r := range rules {
		if !matches(r.Path, path) {
			continue
		}
		for name, values := range r.Headers {
			out[name] = slices.Clone(values)
		}
	}
	return out
}

func matches(pattern, path string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(path, prefix)
	}
	return pattern == path
}
