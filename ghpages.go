package deploy

import (
	"errors"
	"fmt"
	"html"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Elagoht/collage/pkg/collage"
)

// writeGitHubPages writes what GitHub Pages can carry: .nojekyll, so files and
// directories beginning with "_" are published, and a meta-refresh page at
// each literal redirect's From. GitHub Pages sets no custom headers and
// answers no redirect of its own.
func writeGitHubPages(ev *collage.BuildFinishedEvent) error {
	if _, err := os.Lstat(filepath.Join(ev.OutDir, ".nojekyll")); errors.Is(err, fs.ErrNotExist) {
		if err := create(ev.OutDir, ".nojekyll", nil); err != nil {
			return err
		}
	}

	names := map[string]bool{}
	paths := 0
	for _, f := range ev.Files {
		if f.Status < 200 || f.Status > 299 || len(f.Headers) == 0 {
			continue
		}
		paths++
		for name := range f.Headers {
			names[name] = true
		}
	}
	if paths > 0 {
		ev.Warn("", "deploy-headers-lost", fmt.Sprintf("%d header names on %d paths cannot be set on GitHub Pages", len(names), paths))
	}

	var patterned, gone, notHTML, written []string
	// pages are the redirect pages written so far, by file, with the redirect
	// each was written for.
	pages := map[string]string{}
	for _, r := range readRedirects(ev) {
		label := fmt.Sprintf("%s (%s)", r.From, r.Source)
		switch {
		case r.Status == 410:
			gone = append(gone, label)
			continue
		case !r.from.literal():
			patterned = append(patterned, label)
			continue
		}
		if authorityPlaceholder(r.To) {
			unsafeRedirect(ev, r.BuiltRedirect, fmt.Errorf("%w: to %q", errAuthority, r.To))
			continue
		}
		name, ok := refreshFile(r.From)
		if !ok {
			notHTML = append(notHTML, label)
			continue
		}
		if first, ok := pages[name]; ok {
			ev.Error(r.From, "deploy-existing-file", fmt.Sprintf("the redirect page for %s is not written: GitHub Pages serves %s for both it and %s, whose page is written there", label, name, first))
			continue
		}
		if _, err := os.Lstat(filepath.Join(ev.OutDir, filepath.FromSlash(name))); !errors.Is(err, fs.ErrNotExist) {
			ev.Error(r.From, "deploy-existing-file", fmt.Sprintf("the redirect page for %s is not written: %s is already in the output", label, name))
			continue
		}
		if err := create(ev.OutDir, name, refreshPage(r.To)); err != nil {
			return err
		}
		pages[name] = label
		written = append(written, fmt.Sprintf("%s %d", r.From, r.Status))
	}
	if len(patterned) > 0 {
		ev.Warn("", "deploy-unsupported-redirect", fmt.Sprintf("GitHub Pages answers no patterned redirect; these are not written: %s", left(patterned)))
	}
	if len(gone) > 0 {
		ev.Warn("", "deploy-gone", fmt.Sprintf("GitHub Pages cannot answer 410; these paths answer 404 instead: %s", left(gone)))
	}
	if len(notHTML) > 0 {
		ev.Warn("", "deploy-unsupported-redirect", fmt.Sprintf("these redirects cannot be meta-refresh pages, which have to be served as HTML from a file inside the output; not written: %s", left(notHTML)))
	}
	if len(written) > 0 {
		ev.Warn("", "deploy-meta-refresh", fmt.Sprintf("GitHub Pages answers redirects with a meta-refresh page and status 200, so their status is not sent: %s", left(written)))
	}
	return nil
}

// refreshFile is the file GitHub Pages serves at from: "old/index.html" for
// "/old" and "/old/", "old.html" for "/old.html"; false for a path with an
// extension other than .html, and for one with a "." or ".." segment or a backslash,
// which would name a file outside the output (and which a browser never sends).
func refreshFile(from string) (string, bool) {
	trimmed := strings.Trim(from, "/")
	if strings.ContainsRune(trimmed, '\\') {
		return "", false
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "." || segment == ".." {
			return "", false
		}
	}
	switch ext := path.Ext(trimmed); {
	case trimmed == "":
		return "index.html", true
	case strings.HasSuffix(from, "/") || ext == "":
		return trimmed + "/index.html", true
	case ext == ".html" || ext == ".htm":
		return trimmed, true
	}
	return "", false
}

// refreshPage is a page that sends a browser on to to, and tells a search
// engine where the content lives and not to index this address.
func refreshPage(to string) []byte {
	u := html.EscapeString(to)
	return []byte(`<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>Redirecting</title>
<meta http-equiv="refresh" content="0; url=` + u + `">
<link rel="canonical" href="` + u + `">
<meta name="robots" content="noindex">
</head>
<body>
<p><a href="` + u + `">` + u + `</a></p>
</body>
</html>
`)
}
