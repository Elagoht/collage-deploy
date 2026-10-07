package deploy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	deploy "github.com/Elagoht/collage-deploy"
	"github.com/Elagoht/collage/pkg/collage"
)

func newApp(t *testing.T, p *deploy.Plugin, config map[string]json.RawMessage) error {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server:       collage.ServerConfig{Host: "localhost", Port: 3000},
		Template:     collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>page</p>`)}}, Root: "t"},
		Plugins:      []collage.Plugin{p},
		PluginConfig: config,
	})
	if err != nil {
		return err
	}
	return app.Start()
}

func TestUnknownTargetFailsInit(t *testing.T) {
	err := newApp(t, deploy.New(), map[string]json.RawMessage{deploy.Name: json.RawMessage(`{"target":"heroku"}`)})
	if err == nil || !strings.Contains(err.Error(), `unknown target "heroku"; want netlify, cloudflare, vercel or github-pages`) {
		t.Fatalf("err = %v", err)
	}
	if err := newApp(t, deploy.NewWith(deploy.Config{Target: "nope"}), nil); err == nil {
		t.Error("an unknown target set in Go was accepted")
	}
}

func TestKnownTargetsAndConfigOverride(t *testing.T) {
	for _, target := range deploy.Targets {
		if err := newApp(t, deploy.NewWith(deploy.Config{Target: target}), nil); err != nil {
			t.Errorf("%s: %v", target, err)
		}
	}
	if err := newApp(t, deploy.NewWith(deploy.Config{Target: "nope"}), map[string]json.RawMessage{deploy.Name: json.RawMessage(`{"target":"vercel"}`)}); err != nil {
		t.Errorf("configuration should override the Go value: %v", err)
	}
	if err := newApp(t, deploy.New(), nil); err != nil {
		t.Errorf("no target is allowed: %v", err)
	}
}

func TestVersion(t *testing.T) {
	if v := deploy.New().Version(); v != "0.1.0" {
		t.Errorf("Version = %q", v)
	}
	if deploy.New().Name() != "elagoht/deploy" {
		t.Error("Name")
	}
}

func TestEmptyTargetWarnsOnce(t *testing.T) {
	dir := t.TempDir()
	ev := &collage.BuildFinishedEvent{
		OutDir:    dir,
		Files:     []collage.BuiltFile{{Path: "/", Status: 200, Headers: http.Header{"A": {"1"}}}},
		Redirects: []collage.BuiltRedirect{{From: "/a", To: "/b", Status: 301}},
	}
	if err := deploy.New().OnBuildFinished(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Errorf("the output directory holds %v (%v)", entries, err)
	}
	if len(ev.Findings) != 1 || ev.Findings[0].Rule != "deploy-target" || ev.Findings[0].Level != collage.FindingWarning {
		t.Fatalf("findings = %+v", ev.Findings)
	}
}

func finishedWith(t *testing.T, ev *collage.BuildFinishedEvent) {
	t.Helper()
	if err := deploy.NewWith(deploy.Config{Target: "netlify"}).OnBuildFinished(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
}

func TestControlCharacterInRedirectIsAnErrorNamingItsSource(t *testing.T) {
	ev := &collage.BuildFinishedEvent{Redirects: []collage.BuiltRedirect{
		{From: "/fine", To: "/ok", Status: 301, Source: "page:fine"},
		{From: "/old\n/injected /x 301", To: "/new", Status: 301, Source: "elagoht/redirects"},
	}}
	finishedWith(t, ev)
	if len(ev.Findings) != 1 {
		t.Fatalf("findings = %+v", ev.Findings)
	}
	f := ev.Findings[0]
	if strings.ContainsAny(f.Path, "\n\r") {
		t.Errorf("finding path holds a raw control character: %q", f.Path)
	}
	if f.Level != collage.FindingError || f.Rule != "deploy-control-character" || !strings.Contains(f.Message, "elagoht/redirects") || !strings.Contains(f.Message, "/old") {
		t.Errorf("finding = %+v", f)
	}
}

func TestControlCharacterInHeaderIsAnError(t *testing.T) {
	ev := &collage.BuildFinishedEvent{Files: []collage.BuiltFile{
		{Path: "/x", Status: 200, Headers: http.Header{"Link": {"</a>\r\nSet-Cookie: x=1"}}},
	}}
	finishedWith(t, ev)
	if len(ev.Findings) != 1 || ev.Findings[0].Level != collage.FindingError || ev.Findings[0].Path != "/x" {
		t.Fatalf("findings = %+v", ev.Findings)
	}
}

func TestLineSeparatorsAreControlCharacters(t *testing.T) {
	for _, sep := range []string{"\u2028", "\u2029"} {
		ev := &collage.BuildFinishedEvent{Redirects: []collage.BuiltRedirect{{From: "/a" + sep + "b", To: "/c", Status: 301}}}
		finishedWith(t, ev)
		if len(ev.Findings) != 1 {
			t.Errorf("%q: findings = %+v", sep, ev.Findings)
		}
	}
}

func TestCleanBuildHasNoFindings(t *testing.T) {
	ev := &collage.BuildFinishedEvent{
		OutDir:    t.TempDir(),
		Files:     []collage.BuiltFile{{Path: "/", Status: 200, Headers: http.Header{"A": {"1"}}}},
		Redirects: []collage.BuiltRedirect{{From: "/a/{rest...}", To: "/b/{rest}", Status: 301}},
	}
	finishedWith(t, ev)
	if len(ev.Findings) != 0 {
		t.Fatalf("findings = %+v", ev.Findings)
	}
}

func TestControlCharacterInFilePathIsAnError(t *testing.T) {
	ev := &collage.BuildFinishedEvent{OutDir: t.TempDir(), Files: []collage.BuiltFile{
		{Path: "/a\nb/", Status: 200, Headers: http.Header{"A": {"1"}}},
	}}
	finishedWith(t, ev)
	if len(ev.Findings) != 1 || ev.Findings[0].Level != collage.FindingError || ev.Findings[0].Rule != "deploy-control-character" || strings.ContainsAny(ev.Findings[0].Path, "\n") {
		t.Fatalf("findings = %+v", ev.Findings)
	}
	if entries, _ := os.ReadDir(ev.OutDir); len(entries) != 0 {
		t.Errorf("wrote %v", entries)
	}
}

// TestControlCharacterInTheServedPathIsAnError: a header rule's path comes from
// the file on disk, so a control character there is refused as one in Path is.
func TestControlCharacterInTheServedPathIsAnError(t *testing.T) {
	out := t.TempDir()
	ev := &collage.BuildFinishedEvent{OutDir: out, Files: []collage.BuiltFile{
		{Path: "/ab", File: filepath.Join(out, "a\nb", "index.html"), Status: 200, Headers: http.Header{"A": {"1"}}},
	}}
	finishedWith(t, ev)
	if len(ev.Findings) != 1 || ev.Findings[0].Rule != "deploy-control-character" || strings.ContainsAny(ev.Findings[0].Path, "\n") {
		t.Fatalf("findings = %+v", ev.Findings)
	}
}
