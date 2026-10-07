// Package deploy is a collage plugin that turns a static build's captured headers
// and redirects into the configuration files a static host reads.
//
//	app, err := collage.New(&collage.Config{
//		Plugins: []collage.Plugin{deploy.NewWith(deploy.Config{Target: "netlify"})},
//	})
//
// The target is one of netlify, cloudflare, vercel and github-pages. With none
// set the plugin warns that nothing is written and the build goes on.
package deploy

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/Elagoht/collage/pkg/collage"
)

// Name is the plugin's name, and the key its configuration is found under.
const Name = "elagoht/deploy"

// Target is a host a build can be written for, as the configuration names it.
type Target string

// The targets. TargetNone, the empty string, writes nothing for a host.
const (
	TargetNone        Target = ""
	TargetNetlify     Target = "netlify"
	TargetCloudflare  Target = "cloudflare"
	TargetVercel      Target = "vercel"
	TargetGitHubPages Target = "github-pages"
)

// Targets are the hosts a build can be written for.
var Targets = []Target{TargetNetlify, TargetCloudflare, TargetVercel, TargetGitHubPages}

// Config configures the plugin.
type Config struct {
	// Target is the host to write for: "netlify", "cloudflare", "vercel" or
	// "github-pages". Empty writes nothing.
	Target Target `json:"target"`
}

// Plugin writes a build's headers and redirects in a host's form.
type Plugin struct {
	cfg Config
}

// New returns a plugin with no target, which the application's own configuration
// is then decoded over.
func New() *Plugin { return &Plugin{} }

// NewWith returns a plugin with cfg as its starting point, which the application's
// own configuration is then decoded over.
func NewWith(cfg Config) *Plugin { return &Plugin{cfg: cfg} }

func (p *Plugin) Name() string                   { return Name }
func (p *Plugin) Version() string                { return "0.1.1" }
func (p *Plugin) Shutdown(context.Context) error { return nil }

var (
	_ collage.Plugin            = (*Plugin)(nil)
	_ collage.BuildFinishedHook = (*Plugin)(nil)
)

// Init reads the configuration and refuses a target it does not know.
func (p *Plugin) Init(_ context.Context, host collage.Host) error {
	cfg, err := collage.PluginConfig(host, p.cfg)
	if err != nil {
		return err
	}
	if cfg.Target != TargetNone && !slices.Contains(Targets, cfg.Target) {
		return fmt.Errorf("elagoht/deploy: unknown target %q; want netlify, cloudflare, vercel or github-pages", cfg.Target)
	}
	p.cfg = cfg
	return nil
}

// OnBuildFinished writes the build's headers and redirects for the target.
func (p *Plugin) OnBuildFinished(_ context.Context, ev *collage.BuildFinishedEvent) error {
	if p.cfg.Target == TargetNone {
		return p.noTarget(ev)
	}
	if !checkText(ev) {
		return nil
	}
	return p.write(ev)
}

// noTarget writes nothing for a host, and removes what the last export wrote
// for one, unchanged since, so that it is not deployed with a build that has no
// target.
func (p *Plugin) noTarget(ev *collage.BuildFinishedEvent) error {
	message := "no target set: nothing is written for a static host"
	if ev.OutDir != "" {
		previous, ok := previousManifest(ev)
		if !ok {
			return nil
		}
		if previous.Target != "" {
			if err := previous.release(ev.OutDir, builtFiles(ev)); err != nil {
				return err
			}
			message += fmt.Sprintf(", and the files the last export wrote for %s are removed, but for any changed since", previous.Target)
		}
	}
	ev.Warn("", "deploy-target", message)
	return nil
}

// write hands the build to the target's writer.
func (p *Plugin) write(ev *collage.BuildFinishedEvent) error {
	if ev.OutDir == "" {
		return fmt.Errorf("elagoht/deploy: the build names no output directory to write into")
	}
	previous, ok := previousManifest(ev)
	if !ok {
		return nil
	}
	if err := previous.release(ev.OutDir, builtFiles(ev)); err != nil {
		return err
	}
	out := newOutput(ev.OutDir)
	err := p.writeTarget(ev, out)
	// What was written is recorded even when a writer failed part way, so the
	// next build can replace it.
	if recordErr := out.record(p.cfg.Target); err == nil {
		err = recordErr
	}
	return err
}

// writeTarget hands the build to the target's writer.
func (p *Plugin) writeTarget(ev *collage.BuildFinishedEvent, out *output) error {
	switch p.cfg.Target {
	case TargetNetlify:
		return writeLines(ev, out, Compact(ev.OutDir, ev.Files), netlify)
	case TargetCloudflare:
		return writeLines(ev, out, Compact(ev.OutDir, ev.Files), cloudflare)
	case TargetVercel:
		return writeVercel(ev, out, Compact(ev.OutDir, ev.Files))
	case TargetGitHubPages:
		return writeGitHubPages(ev, out)
	}
	return fmt.Errorf("elagoht/deploy: unknown target %q", p.cfg.Target)
}

// checkText reports, as an error naming where it was found, any control character
// in a redirect, a built file's path or a header: either would split a line of a host's file and write
// a rule nobody declared. It reports whether the build is clean.
func checkText(ev *collage.BuildFinishedEvent) bool {
	clean := true
	for _, r := range ev.Redirects {
		for _, field := range []struct{ name, value string }{{"from", r.From}, {"to", r.To}} {
			if hasControl(field.value) {
				ev.Error(findingPath(r.From), "deploy-control-character", fmt.Sprintf("redirect from %q (%s): %s %q holds a control character", r.From, r.Source, field.name, field.value))
				clean = false
			}
		}
	}
	for _, f := range ev.Files {
		// A file's served path, from its path or its file on disk, is a header
		// rule's path line in _headers.
		if served := ServedAt(ev.OutDir, f); hasControl(f.Path) || hasControl(served) {
			bad := f.Path
			if !hasControl(bad) {
				bad = served
			}
			ev.Error(findingPath(bad), "deploy-control-character", fmt.Sprintf("the path of built file %q holds a control character", bad))
			clean = false
		}
		names := make([]string, 0, len(f.Headers))
		for name := range f.Headers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if bad := headerText(name, f.Headers); bad != "" {
				ev.Error(findingPath(f.Path), "deploy-control-character", fmt.Sprintf("header %q of %q holds a control character: %q", name, f.Path, bad))
				clean = false
				continue
			}
			if !validHeaderName(name) {
				ev.Error(findingPath(f.Path), "deploy-unsupported-header", fmt.Sprintf("header name %q of %q is not an HTTP token: a host would read its line differently", name, f.Path))
				clean = false
			}
		}
	}
	return clean
}

// headerText returns the first piece of a header, its name or a value, holding a
// control character, or "".
func headerText(name string, h http.Header) string {
	if hasControl(name) {
		return name
	}
	for _, v := range h[name] {
		if hasControl(v) {
			return v
		}
	}
	return ""
}

// validHeaderName reports whether name is an HTTP token (RFC 9110 5.1, RFC
// 7230 3.2.6): one or more tchars, so no space, ":" or separator that would
// split a host's header line elsewhere.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// findingPath is a path made safe to print in a report: quoted when it holds a
// control character.
func findingPath(path string) string {
	if hasControl(path) {
		return strconv.Quote(path)
	}
	return path
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return true
		}
	}
	return false
}
