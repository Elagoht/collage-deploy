package deploy

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/Elagoht/collage/pkg/collage"
)

// lineHost is a host reading Netlify's _headers and _redirects format: Netlify
// itself, and Cloudflare Pages with its limits.
type lineHost struct {
	name string
	// statuses maps each redirect status the application may use to the one
	// the host is written with; a status it maps to another is warned about.
	statuses map[int]int
	// headerRules, headerLine, staticRedirects, dynamicRedirects and
	// redirectLine are the host's limits; 0 is none.
	headerRules, headerLine           int
	staticRedirects, dynamicRedirects int
	redirectLine                      int
	// slashTwins writes each redirect that is not a catch-all at its other
	// spelling too ("/old" and "/old/"): collage matches both, and the host
	// does not normalise a trailing "/" before matching.
	slashTwins bool
	// redirectsBeatFiles is a host that follows a redirect even where a file
	// is served: a catch-all whose base is a page would take the page away.
	redirectsBeatFiles bool
}

var netlify = lineHost{
	name: "Netlify",
	// Netlify supports 301 and 302 only; 307 is "currently unsupported" and
	// 308 is not listed. For the GET and HEAD a static site answers, 308 and
	// 301 (307 and 302) send a browser to the same place.
	statuses: map[int]int{301: 301, 302: 302, 307: 302, 308: 301},
}

var cloudflare = lineHost{
	name:               "Cloudflare Pages",
	statuses:           map[int]int{301: 301, 302: 302, 307: 307, 308: 308},
	headerRules:        100,
	headerLine:         2000,
	staticRedirects:    2000,
	dynamicRedirects:   100,
	redirectLine:       1000,
	slashTwins:         true,
	redirectsBeatFiles: true,
}

// lineSyntax is how _redirects spells a pattern: ":name" for a placeholder,
// "*" and ":splat" for a catch-all; placeholders match whole segments only.
var lineSyntax = syntax{
	param:        func(name string) string { return ":" + name },
	catchAllFrom: func(string) string { return "*" },
	catchAllTo:   func(string) string { return ":splat" },
	literal: func(text string) (string, error) {
		if why := lineLiteral(text); why != "" {
			return "", fmt.Errorf("%q %s", text, why)
		}
		return text, nil
	},
}

// lineLiteral reports why text cannot stand as literal text in a _headers or
// _redirects path, or "".
func lineLiteral(text string) string {
	switch {
	case strings.IndexFunc(text, unicode.IsSpace) >= 0:
		return "holds a space, which separates a line's fields"
	case strings.Contains(text, "*"):
		return "holds \"*\", which the host reads as a wildcard"
	case strings.HasPrefix(text, ":") || placeholderLike(text):
		return "holds \":\" before a name, which the host reads as a placeholder"
	}
	return ""
}

// lineTo checks a To's literal text for _redirects.
func lineTo(text string) error {
	switch {
	case strings.IndexFunc(text, unicode.IsSpace) >= 0:
		return fmt.Errorf("to %q holds a space, which separates a line's fields", text)
	case placeholderLike(text):
		return fmt.Errorf("to %q holds \":\" before a name, which the host reads as a placeholder", text)
	}
	return nil
}

// writeLines writes _headers and _redirects for h.
func writeLines(ev *collage.BuildFinishedEvent, rules []HeaderRule, h lineHost) error {
	if !claim(ev, "_headers", "_redirects") {
		return nil
	}
	headers := h.headers(ev, rules)
	redirects := h.redirects(ev)
	if headers != "" {
		if err := create(ev.OutDir, "_headers", []byte(headers)); err != nil {
			return err
		}
	}
	if redirects != "" {
		if err := create(ev.OutDir, "_redirects", []byte(redirects)); err != nil {
			return err
		}
	}
	return nil
}

// headers is the _headers file: a block per rule, the path then each header
// indented, one line per value, a blank line between blocks.
func (h lineHost) headers(ev *collage.BuildFinishedEvent, rules []HeaderRule) string {
	var blocks []string
	var unwritable, long, over []string
	for _, r := range rules {
		literal := strings.TrimSuffix(r.Path, "*")
		if why := lineLiteral(literal); why != "" {
			unwritable = append(unwritable, fmt.Sprintf("%s (%s)", r.Path, why))
			continue
		}
		if h.headerLine > 0 && len(r.Path) > h.headerLine {
			long = append(long, r.Path)
			continue
		}
		var b strings.Builder
		b.WriteString(r.Path + "\n")
		lines := 0
		for _, name := range sortedNames(r.Headers) {
			for _, v := range r.Headers[name] {
				line := "  " + name + ": " + v
				if h.headerLine > 0 && len(line) > h.headerLine {
					long = append(long, name+" on "+r.Path)
					continue
				}
				b.WriteString(line + "\n")
				lines++
			}
		}
		if lines == 0 {
			continue
		}
		if h.headerRules > 0 && len(blocks) == h.headerRules {
			over = append(over, r.Path)
			continue
		}
		blocks = append(blocks, b.String())
	}
	if len(unwritable) > 0 {
		ev.Warn("", "deploy-unsupported-header", fmt.Sprintf("%d header rules cannot be written in %s's _headers and are left out: %s", len(unwritable), h.name, strings.Join(unwritable, "; ")))
	}
	if len(long) > 0 {
		ev.Warn("", "deploy-line-length", fmt.Sprintf("%s reads _headers lines of at most %d characters; left out: %s", h.name, h.headerLine, left(long)))
	}
	if len(over) > 0 {
		ev.Warn("", "deploy-limit", fmt.Sprintf("%s reads at most %d header rules; %d are left out: %s", h.name, h.headerRules, len(over), left(over)))
	}
	return strings.Join(blocks, "\n")
}

// line is one _redirects line about to be written.
type line struct {
	text    string
	dynamic bool
	twin    bool
	label   string
}

// redirects is the _redirects file: "from to status", most specific first.
func (h lineHost) redirects(ev *collage.BuildFinishedEvent) string {
	served := map[string]bool{}
	for _, f := range ev.Files {
		served[ServedAt(ev.OutDir, f)] = true
	}
	var lines []line
	var gone, unwritable, downgraded []string
	for _, r := range readRedirects(ev) {
		label := fmt.Sprintf("%s (%s)", r.From, r.Source)
		if r.Status == 410 {
			gone = append(gone, label)
			continue
		}
		status, ok := h.statuses[r.Status]
		if !ok {
			unwritable = append(unwritable, fmt.Sprintf("%s: status %d", label, r.Status))
			continue
		}
		from, to, err := translate(r, lineSyntax, lineTo)
		if err != nil {
			if unsafeRedirect(ev, r.BuiltRedirect, err) {
				continue
			}
			unwritable = append(unwritable, fmt.Sprintf("%s: %v", label, err))
			continue
		}
		_, catchAll := r.from.catchAll()
		if catchAll && h.redirectsBeatFiles {
			base := r.from.base()
			if served[base] || served[strings.TrimSuffix(base, "/")] {
				unwritable = append(unwritable, fmt.Sprintf("%s: the host follows a redirect even where a file is served, and %s is a page the catch-all would take away", label, base))
				continue
			}
		}
		if status != r.Status {
			downgraded = append(downgraded, fmt.Sprintf("%s %d as %d", label, r.Status, status))
		}
		code := strconv.Itoa(status)
		lines = append(lines, line{text: from + " " + to + " " + code + "\n", dynamic: !r.from.literal(), label: label})
		if h.slashTwins && !catchAll {
			if twin := toggleSlash(from); twin != from {
				lines = append(lines, line{text: twin + " " + to + " " + code + "\n", dynamic: !r.from.literal(), twin: true, label: twin + " for " + label})
			}
		}
	}
	lines = h.fit(ev, lines)
	if len(gone) > 0 {
		ev.Warn("", "deploy-gone", fmt.Sprintf("%s's _redirects has no documented 410 form; these paths answer 404 instead: %s", h.name, left(gone)))
	}
	if len(downgraded) > 0 {
		ev.Warn("", "deploy-status", fmt.Sprintf("%s supports only 301 and 302 redirects; written as the status a browser treats alike for GET: %s", h.name, left(downgraded)))
	}
	if len(unwritable) > 0 {
		ev.Warn("", "deploy-unsupported-redirect", fmt.Sprintf("%d redirects cannot be written in %s's _redirects and are left out: %s", len(unwritable), h.name, strings.Join(unwritable, "; ")))
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.text)
	}
	return b.String()
}

// fit keeps the lines the host's limits allow, in order: a line over the
// length limit is left out; the static and dynamic budgets go to each
// redirect's own spelling first, and to its trailing-slash twin only while
// budget remains.
func (h lineHost) fit(ev *collage.BuildFinishedEvent, lines []line) []line {
	if h.staticRedirects == 0 && h.dynamicRedirects == 0 && h.redirectLine == 0 {
		return lines
	}
	keep := make([]bool, len(lines))
	var long, over []string
	used := map[bool]int{}
	budget := map[bool]int{false: h.staticRedirects, true: h.dynamicRedirects}
	for _, twins := range []bool{false, true} {
		for i, l := range lines {
			if l.twin != twins {
				continue
			}
			if h.redirectLine > 0 && len(l.text)-1 > h.redirectLine {
				long = append(long, l.label)
				continue
			}
			if used[l.dynamic] == budget[l.dynamic] {
				over = append(over, l.label)
				continue
			}
			used[l.dynamic]++
			keep[i] = true
		}
	}
	if len(long) > 0 {
		ev.Warn("", "deploy-line-length", fmt.Sprintf("%s reads _redirects lines of at most %d characters; left out: %s", h.name, h.redirectLine, left(long)))
	}
	if len(over) > 0 {
		ev.Warn("", "deploy-limit", fmt.Sprintf("%s reads at most %d static and %d dynamic redirects; left out: %s", h.name, h.staticRedirects, h.dynamicRedirects, left(over)))
	}
	out := lines[:0:0]
	for i, l := range lines {
		if keep[i] {
			out = append(out, l)
		}
	}
	return out
}
