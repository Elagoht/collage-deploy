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
	"unicode"

	"github.com/Elagoht/collage/pkg/collage"
)

// Name is the plugin's name, and the key its configuration is found under.
const Name = "elagoht/deploy"

// Targets are the hosts a build can be written for.
var Targets = []string{"netlify", "cloudflare", "vercel", "github-pages"}

// Config configures the plugin.
type Config struct {
	// Target is the host to write for: "netlify", "cloudflare", "vercel" or
	// "github-pages". Empty writes nothing.
	Target string `json:"target"`
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
func (p *Plugin) Version() string                { return "0.1.0" }
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
	if cfg.Target != "" && !slices.Contains(Targets, cfg.Target) {
		return fmt.Errorf("elagoht/deploy: unknown target %q; want netlify, cloudflare, vercel or github-pages", cfg.Target)
	}
	p.cfg = cfg
	return nil
}

// OnBuildFinished writes the build's headers and redirects for the target.
func (p *Plugin) OnBuildFinished(_ context.Context, ev *collage.BuildFinishedEvent) error {
	if p.cfg.Target == "" {
		ev.Warn("", "deploy-target", "no target set: nothing is written for a static host")
		return nil
	}
	if !checkText(ev) {
		return nil
	}
	return p.write(ev)
}

// write hands the build to the target's writer.
func (p *Plugin) write(*collage.BuildFinishedEvent) error {
	// The writers come in the next step.
	return nil
}

// checkText reports, as an error naming where it was found, any control character
// in a redirect or a header: either would split a line of a host's file and write
// a rule nobody declared. It reports whether the build is clean.
func checkText(ev *collage.BuildFinishedEvent) bool {
	clean := true
	for _, r := range ev.Redirects {
		for _, field := range []struct{ name, value string }{{"from", r.From}, {"to", r.To}} {
			if hasControl(field.value) {
				ev.Error(r.From, "deploy-control-character", fmt.Sprintf("redirect from %q (%s): %s %q holds a control character", r.From, r.Source, field.name, field.value))
				clean = false
			}
		}
	}
	for _, f := range ev.Files {
		names := make([]string, 0, len(f.Headers))
		for name := range f.Headers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if bad := headerText(name, f.Headers); bad != "" {
				ev.Error(f.Path, "deploy-control-character", fmt.Sprintf("header %q of %s holds a control character: %q", name, f.Path, bad))
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

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return true
		}
	}
	return false
}
