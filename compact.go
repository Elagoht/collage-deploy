package deploy

import (
	"net/http"
	"net/textproto"
	"path/filepath"
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

// ServedPath returns the path a host serves a file at, spelled by its path
// under the output: a leading slash, and "index.html" dropped, so
// "blog/index.html" is "/blog/". It is not a page's BuiltFile.Path, which is the
// route as registered — "/blog" for blog/index.html unless the application uses
// a trailing slash; ServedAt derives the served path from the file instead.
func ServedPath(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if dir, ok := strings.CutSuffix(path, "/index.html"); ok {
		return dir + "/"
	}
	return path
}

// ServedAt returns the path a host serves f at, from where the build wrote it:
// its File under outDir, so a page registered as "/about" and written to
// about/index.html is "/about/". A file with no File, or outside outDir, falls
// back to ServedPath of its Path.
func ServedAt(outDir string, f collage.BuiltFile) string {
	if f.File != "" && outDir != "" {
		if rel, err := filepath.Rel(outDir, f.File); err == nil && filepath.IsLocal(rel) {
			return ServedPath(filepath.ToSlash(rel))
		}
	}
	return ServedPath(f.Path)
}

// perPath are headers that describe one file, written only at its own path:
// "/*" and "/dir/*" also reach the files other plugins write into the output —
// a share card, a search index — which a host would then serve as, say,
// text/html.
var perPath = []string{"Content-Type", "Content-Disposition", "Content-Language"}

// wildcardable reports whether a header may go in a "/*" or "/dir/*" rule.
func wildcardable(name string) bool { return !slices.Contains(perPath, name) }

// ImpliedByExtension reports whether f's captured Content-Type is the one its
// extension implies in extensionTypes — Go's builtin mime table, which is what
// collage serves a file with where the machine adds nothing of its own,
// "text/html; charset=utf-8" for .html — compared without regard to case or to
// spaces around ";". The machine's own table is never consulted, so the rules
// are the same on every machine. A host types a file by its extension, so such a
// Content-Type needs no rule. The extension is the written file's, or, with no
// File, its served path's, a directory standing for its index.html. A file
// with no extension, or with no Content-Type, implies nothing.
func ImpliedByExtension(outDir string, f collage.BuiltFile) bool {
	captured := f.Headers.Values("Content-Type")
	if len(captured) != 1 {
		return false
	}
	name := f.File
	if name == "" || outDir == "" {
		name = ServedPath(f.Path)
		if strings.HasSuffix(name, "/") {
			name += "index.html"
		}
	}
	ext := filepath.Ext(name)
	if ext == "" {
		return false
	}
	implied := extensionTypes[strings.ToLower(ext)]
	return implied != "" && mediaType(captured[0]) == mediaType(implied)
}

// extensionTypes is a copy of Go's builtin mime table, builtinTypesLower in
// mime/type.go (Go 1.26). mime.TypeByExtension starts from it but lets the
// machine's own tables (/etc/mime.types, freedesktop globs2) override it — .xml
// is application/xml on one machine and text/xml on another — so the plugin
// keeps its own copy.
var extensionTypes = map[string]string{
	".ai":    "application/postscript",
	".apk":   "application/vnd.android.package-archive",
	".apng":  "image/apng",
	".avif":  "image/avif",
	".bin":   "application/octet-stream",
	".bmp":   "image/bmp",
	".com":   "application/octet-stream",
	".css":   "text/css; charset=utf-8",
	".csv":   "text/csv; charset=utf-8",
	".doc":   "application/msword",
	".docx":  "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".ehtml": "text/html; charset=utf-8",
	".eml":   "message/rfc822",
	".eps":   "application/postscript",
	".exe":   "application/octet-stream",
	".flac":  "audio/flac",
	".gif":   "image/gif",
	".gz":    "application/gzip",
	".htm":   "text/html; charset=utf-8",
	".html":  "text/html; charset=utf-8",
	".ico":   "image/vnd.microsoft.icon",
	".ics":   "text/calendar; charset=utf-8",
	".jfif":  "image/jpeg",
	".jpeg":  "image/jpeg",
	".jpg":   "image/jpeg",
	".js":    "text/javascript; charset=utf-8",
	".json":  "application/json",
	".m4a":   "audio/mp4",
	".mjs":   "text/javascript; charset=utf-8",
	".mp3":   "audio/mpeg",
	".mp4":   "video/mp4",
	".oga":   "audio/ogg",
	".ogg":   "audio/ogg",
	".ogv":   "video/ogg",
	".opus":  "audio/ogg",
	".pdf":   "application/pdf",
	".pjp":   "image/jpeg",
	".pjpeg": "image/jpeg",
	".png":   "image/png",
	".ppt":   "application/vnd.ms-powerpoint",
	".pptx":  "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".ps":    "application/postscript",
	".rdf":   "application/rdf+xml",
	".rtf":   "application/rtf",
	".shtml": "text/html; charset=utf-8",
	".svg":   "image/svg+xml",
	".text":  "text/plain; charset=utf-8",
	".tif":   "image/tiff",
	".tiff":  "image/tiff",
	".txt":   "text/plain; charset=utf-8",
	".vtt":   "text/vtt; charset=utf-8",
	".wasm":  "application/wasm",
	".wav":   "audio/wav",
	".webm":  "audio/webm",
	".webp":  "image/webp",
	".xbl":   "text/xml; charset=utf-8",
	".xbm":   "image/x-xbitmap",
	".xht":   "application/xhtml+xml",
	".xhtml": "application/xhtml+xml",
	".xls":   "application/vnd.ms-excel",
	".xlsx":  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".xml":   "text/xml; charset=utf-8",
	".xsl":   "text/xml; charset=utf-8",
	".zip":   "application/zip",
}

// mediaType is a Content-Type in one spelling: lower case, no space around
// ";".
func mediaType(v string) string {
	parts := strings.Split(strings.ToLower(v), ";")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ";")
}

// entry is one built file as compaction sees it.
type entry struct {
	path string
	// captured is a file the build asked for and answered 2xx: its headers are
	// known, and a rule may speak for it.
	captured bool
	// blocking is a file that was asked for and answered otherwise (a redirect,
	// a 404), or whose capture failed or was never reached: "/*" would hand it
	// headers it was never answered with.
	blocking bool
	headers  http.Header
}

// entries canonicalises every file, in an order that does not depend on the order
// they were given in.
func entries(outDir string, files []collage.BuiltFile) []entry {
	out := make([]entry, 0, len(files))
	for _, f := range files {
		e := entry{path: ServedAt(outDir, f), headers: http.Header{}}
		implied := ImpliedByExtension(outDir, f)
		switch {
		case f.Status >= 200 && f.Status <= 299 && f.Headers != nil:
			e.captured = true
			names := make([]string, 0, len(f.Headers))
			for name := range f.Headers {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if len(f.Headers[name]) == 0 {
					continue
				}
				key := textproto.CanonicalMIMEHeaderKey(name)
				if implied && key == "Content-Type" {
					continue
				}
				e.headers[key] = append(e.headers[key], f.Headers[name]...)
			}
		case f.Status < 200 || f.Status > 299:
			e.blocking = f.Status != 0 || f.Captured
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].path != out[j].path {
			return out[i].path < out[j].path
		}
		if out[i].captured != out[j].captured {
			return out[i].captured
		}
		return headerKey(out[i].headers) < headerKey(out[j].headers)
	})
	return out
}

// wildcardPart is a copy of the headers of h a wildcard rule may carry.
func wildcardPart(h http.Header) http.Header {
	out := http.Header{}
	for name, values := range h {
		if wildcardable(name) {
			out[name] = slices.Clone(values)
		}
	}
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
// that give every captured file exactly its own headers back: what every captured
// file shares in "/*", what every file of a directory shares in "/dir/*", the
// rest at the file's own path. The result does not depend on the order of files.
//
// Files the build did not ask for (status 0, or no headers) are not captured and
// contribute nothing, but a host cannot tell them from the rest, so:
//
//   - "/*" reaches them. A site's 404 pages are such files and receive the
//     headers every captured page shares; that is the intent of "/*".
//   - "/dir/*" is emitted only when every file under dir is a captured 2xx file
//     sharing the headers, so a wildcard never reaches an uncaptured file.
//   - A file asked for and answered other than 2xx (a redirect, a 404) blocks
//     "/*" altogether: its own response does not carry what the pages do. Rules
//     then fall back to directories and paths.
func Compact(outDir string, files []collage.BuiltFile) []HeaderRule {
	all := entries(outDir, files)
	var caps []entry
	blocked := false
	for _, e := range all {
		if e.captured {
			caps = append(caps, e)
		}
		if e.blocking {
			blocked = true
		}
	}
	if len(caps) == 0 {
		return nil
	}
	var rules []HeaderRule

	// 1. What every captured file carries.
	if !blocked {
		common := http.Header{}
		for name, values := range caps[0].headers {
			if !wildcardable(name) {
				continue
			}
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

	// 2. Directories, deepest first: one holding two or more files, every one of
	// them captured with the same non-empty remainder, gets it as a wildcard.
	dirs := map[string]bool{}
	for _, e := range all {
		for i := 1; i < len(e.path); i++ {
			if e.path[i] == '/' {
				dirs[e.path[:i+1]] = true
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
		var under []entry
		whole := true
		for _, e := range all {
			if !strings.HasPrefix(e.path, d) {
				continue
			}
			if !e.captured {
				whole = false
				break
			}
			under = append(under, e)
		}
		if !whole || len(under) < 2 {
			continue
		}
		shared := wildcardPart(under[0].headers)
		if len(shared) == 0 {
			continue
		}
		key := headerKey(shared)
		same := true
		for _, c := range under[1:] {
			if headerKey(wildcardPart(c.headers)) != key {
				same = false
				break
			}
		}
		if !same {
			continue
		}
		wildcards = append(wildcards, HeaderRule{Path: d + "*", Headers: shared})
		for _, c := range under {
			for name := range shared {
				delete(c.headers, name)
			}
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
// set. path is a served path, as ServedAt spells it. A Content-Type the rules
// leave out because the file's extension implies it (ImpliedByExtension) is the
// host's to supply, and is not in the result.
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
