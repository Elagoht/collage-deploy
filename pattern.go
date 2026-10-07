package deploy

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Elagoht/collage/pkg/collage"
)

// segKind is the kind of one segment of a redirect's From, ranked as the router
// prefers them when two patterns could take one path: literal text first, then a
// placeholder with text around it, then a bare placeholder, then a catch-all.
type segKind int

const (
	segStatic segKind = iota
	segAffixed
	segDynamic
	segCatchAll
)

// seg is one "/"-separated piece of a redirect's From, read with the router's
// grammar: "old", "{slug}", "{slug}.md", "post-{id}" or "{rest...}".
type seg struct {
	kind           segKind
	text           string // the literal text, or the placeholder's name
	prefix, suffix string // an affixed placeholder's text around it
}

// pattern is a redirect's From, read.
type pattern struct {
	segs []seg
	// slash is a From written with a trailing "/" ("/old/").
	slash bool
}

// parseFrom reads from as the router reads a pattern. The core has already
// refused every From that does not parse, so a failure here is a rule the
// plugin cannot read and does not write.
func parseFrom(from string) (pattern, error) {
	if !strings.HasPrefix(from, "/") {
		return pattern{}, fmt.Errorf("does not start with \"/\"")
	}
	p := pattern{slash: len(from) > 1 && strings.HasSuffix(from, "/")}
	trimmed := strings.Trim(from, "/")
	if trimmed == "" {
		return p, nil
	}
	parts := strings.Split(trimmed, "/")
	for i, part := range parts {
		if part == "" {
			return pattern{}, fmt.Errorf("has an empty segment")
		}
		open := strings.IndexByte(part, '{')
		if open < 0 {
			if strings.IndexByte(part, '}') >= 0 {
				return pattern{}, fmt.Errorf("segment %q has a \"}\" with no \"{\"", part)
			}
			p.segs = append(p.segs, seg{kind: segStatic, text: part})
			continue
		}
		end := strings.IndexByte(part, '}')
		if end < open {
			return pattern{}, fmt.Errorf("segment %q is not a placeholder", part)
		}
		prefix, inner, suffix := part[:open], part[open+1:end], part[end+1:]
		if strings.ContainsAny(prefix+suffix, "{}") || inner == "" {
			return pattern{}, fmt.Errorf("segment %q is not one placeholder", part)
		}
		if name, ok := strings.CutSuffix(inner, "..."); ok {
			if name == "" || prefix != "" || suffix != "" || i != len(parts)-1 {
				return pattern{}, fmt.Errorf("catch-all segment %q is not a whole final segment", part)
			}
			p.segs = append(p.segs, seg{kind: segCatchAll, text: name})
			continue
		}
		kind := segDynamic
		if prefix != "" || suffix != "" {
			kind = segAffixed
		}
		p.segs = append(p.segs, seg{kind: kind, text: inner, prefix: prefix, suffix: suffix})
	}
	return p, nil
}

func (p pattern) literal() bool {
	for _, s := range p.segs {
		if s.kind != segStatic {
			return false
		}
	}
	return true
}

func (p pattern) catchAll() (string, bool) {
	if n := len(p.segs); n > 0 && p.segs[n-1].kind == segCatchAll {
		return p.segs[n-1].text, true
	}
	return "", false
}

// base is the path a catch-all pattern's placeholder hangs from: "/docs/" for
// "/docs/{rest...}".
func (p pattern) base() string {
	var b strings.Builder
	b.WriteByte('/')
	for _, s := range p.segs[:len(p.segs)-1] {
		b.WriteString(s.text)
		b.WriteByte('/')
	}
	return b.String()
}

// moreSpecific reports whether a's segments rank before b's, read left to right,
// as the router would try them: a host's redirect file is read top down, first
// match wins, so the file has to list what the router prefers first.
func moreSpecific(a, b pattern) bool {
	for i := 0; i < len(a.segs) && i < len(b.segs); i++ {
		if a.segs[i].kind != b.segs[i].kind {
			return a.segs[i].kind < b.segs[i].kind
		}
	}
	return len(a.segs) < len(b.segs)
}

// redirect is one BuiltRedirect read, in the order a host file lists it.
type redirect struct {
	collage.BuiltRedirect
	from pattern
}

// readRedirects reads every redirect and orders them most specific first,
// registration order kept among equals. One whose From does not parse is
// reported and left out.
func readRedirects(ev *collage.BuildFinishedEvent) []redirect {
	out := make([]redirect, 0, len(ev.Redirects))
	for _, r := range ev.Redirects {
		p, err := parseFrom(r.From)
		if err != nil {
			ev.Warn(r.From, "deploy-unsupported-redirect", fmt.Sprintf("redirect from %q (%s) is not written: its pattern %v", r.From, r.Source, err))
			continue
		}
		out = append(out, redirect{BuiltRedirect: r, from: p})
	}
	sort.SliceStable(out, func(i, j int) bool { return moreSpecific(out[i].from, out[j].from) })
	return out
}

// syntax is how a host spells a redirect pattern.
type syntax struct {
	// param spells a placeholder named name.
	param func(name string) string
	// catchAllFrom and catchAllTo spell a catch-all in a From and in a To.
	catchAllFrom, catchAllTo func(name string) string
	// affixed is whether a placeholder may have text around it in a segment.
	affixed bool
	// literal spells a From's literal text, or reports why it cannot.
	literal func(text string) (string, error)
}

// nameOK is a placeholder name every host reads as one name: letters, digits
// and "_", the characters Cloudflare allows and path-to-regexp reads as \w.
func nameOK(name string) bool {
	if name == "" || name == "splat" {
		return false
	}
	for _, r := range name {
		if !(r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

func wordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// translate spells r in a host's syntax, or reports why it cannot be. A
// placeholder name a host cannot read ("post-id", "splat") is renamed p1, p2, …
// in the order From holds them, for From and To alike.
func translate(r redirect, sx syntax, toLiteral func(string) error) (from, to string, err error) {
	names := map[string]string{}
	rename := false
	for _, s := range r.from.segs {
		if s.kind != segStatic && !nameOK(s.text) {
			rename = true
		}
	}
	n := 0
	for _, s := range r.from.segs {
		if s.kind == segStatic {
			continue
		}
		n++
		names[s.text] = s.text
		if rename {
			names[s.text] = "p" + strconv.Itoa(n)
		}
	}
	catchAll, hasCatchAll := r.from.catchAll()

	var b strings.Builder
	for _, s := range r.from.segs {
		b.WriteByte('/')
		switch s.kind {
		case segStatic:
			text, err := sx.literal(s.text)
			if err != nil {
				return "", "", err
			}
			b.WriteString(text)
		case segAffixed:
			if !sx.affixed {
				return "", "", fmt.Errorf("the host's placeholders match whole segments only, and %q has text around its placeholder", s.prefix+"{"+s.text+"}"+s.suffix)
			}
			if s.suffix != "" && wordByte(s.suffix[0]) {
				return "", "", fmt.Errorf("the text after the placeholder in %q would be read as part of its name", s.prefix+"{"+s.text+"}"+s.suffix)
			}
			pre, err := sx.literal(s.prefix)
			if err != nil {
				return "", "", err
			}
			suf, err := sx.literal(s.suffix)
			if err != nil {
				return "", "", err
			}
			b.WriteString(pre + sx.param(names[s.text]) + suf)
		case segDynamic:
			b.WriteString(sx.param(names[s.text]))
		case segCatchAll:
			b.WriteString(sx.catchAllFrom(names[s.text]))
		}
	}
	if b.Len() == 0 || r.from.slash {
		b.WriteByte('/')
	}
	from = b.String()

	if r.Status == 410 {
		return from, "", nil
	}
	// To: every "{name}" (or "{name...}" for the catch-all) becomes the host's
	// spelling; the text between is the host's to read as it stands.
	var t strings.Builder
	rest := r.To
	for {
		open := strings.IndexByte(rest, '{')
		if open < 0 {
			break
		}
		end := strings.IndexByte(rest[open:], '}')
		if end < 0 {
			return "", "", fmt.Errorf("to %q has a \"{\" with no \"}\"", r.To)
		}
		end += open
		if err := toLiteral(rest[:open]); err != nil {
			return "", "", err
		}
		t.WriteString(rest[:open])
		name := strings.TrimSuffix(rest[open+1:end], "...")
		host, ok := names[name]
		if !ok {
			return "", "", fmt.Errorf("to %q names placeholder %q, which from does not capture", r.To, name)
		}
		if end+1 < len(rest) && wordByte(rest[end+1]) {
			return "", "", fmt.Errorf("the text after {%s} in to %q would be read as part of its name", name, r.To)
		}
		if hasCatchAll && name == catchAll {
			t.WriteString(sx.catchAllTo(host))
		} else {
			t.WriteString(sx.param(host))
		}
		rest = rest[end+1:]
	}
	if err := toLiteral(rest); err != nil {
		return "", "", err
	}
	t.WriteString(rest)
	return from, t.String(), nil
}

// toggleSlash is from with its trailing "/" added or taken away: the other
// spelling of one path, which collage's router matches alike.
func toggleSlash(from string) string {
	if from == "/" {
		return from
	}
	if s, ok := strings.CutSuffix(from, "/"); ok {
		return s
	}
	return from + "/"
}

// placeholderLike reports whether text holds ":" followed by a name character,
// which a host's file reads as a placeholder.
func placeholderLike(text string) bool {
	for i := 0; i+1 < len(text); i++ {
		if text[i] == ':' && wordByte(text[i+1]) {
			return true
		}
	}
	return false
}
