package deploy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Elagoht/collage/pkg/collage"
)

// ManifestFile is the file, at the root of the output, in which the plugin
// records what it wrote: the target and each file with its SHA-256. collage
// export does not clean its output by default, so the next build into the same
// directory finds those files there; one listed in the manifest and unchanged
// since is the plugin's own, and is replaced or, when the build no longer
// writes it, removed. Anything else is refused as a file the plugin did not
// write.
const ManifestFile = ".collage-deploy.json"

// manifest is ManifestFile's content. The field order is the key order in the
// file.
type manifest struct {
	Target string          `json:"target"`
	Files  []manifestEntry `json:"files"`
}

type manifestEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// readManifest reads the manifest under dir: none, when there is none.
func readManifest(dir string) (manifest, error) {
	path := filepath.Join(dir, ManifestFile)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return manifest{}, nil
	}
	if err != nil {
		return manifest{}, err
	}
	if !info.Mode().IsRegular() {
		return manifest{}, fmt.Errorf("it is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return manifest{}, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return manifest{}, err
	}
	return m, nil
}

// release removes the files m lists that are still the plugin's — inside dir,
// regular files reached through no link, unchanged since they were written,
// and not written by this build — and the directories that leaves empty, then
// m itself. What it leaves is refused like any file the plugin did not write.
func (m manifest) release(dir string, built map[string]bool) error {
	dir = filepath.Clean(dir)
	for _, e := range m.Files {
		if !manifestPath(e.Path) || built[e.Path] {
			continue
		}
		path := filepath.Join(dir, filepath.FromSlash(e.Path))
		if !regularInside(dir, e.Path) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil || digest(data) != e.SHA256 {
			continue
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("elagoht/deploy: %w", err)
		}
		for parent := filepath.Dir(path); parent != dir && strings.HasPrefix(parent, dir); parent = filepath.Dir(parent) {
			if os.Remove(parent) != nil {
				break
			}
		}
	}
	if err := os.Remove(filepath.Join(dir, ManifestFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("elagoht/deploy: %w", err)
	}
	return nil
}

// manifestPath reports whether name is a path the plugin could have written: a
// relative slash path inside the output, and not the manifest.
func manifestPath(name string) bool {
	return name != ManifestFile && !strings.ContainsRune(name, '\\') && filepath.IsLocal(filepath.FromSlash(name))
}

// regularInside reports whether name under dir is a regular file reached
// through directories only: no link anywhere on the way.
func regularInside(dir, name string) bool {
	parts := strings.Split(name, "/")
	path := dir
	for i, part := range parts {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil {
			return false
		}
		last := i == len(parts)-1
		if last && !info.Mode().IsRegular() || !last && !info.IsDir() {
			return false
		}
	}
	return true
}

// record writes the manifest of what o wrote for target, or nothing when it
// wrote nothing.
func (o *output) record(target string) error {
	if len(o.wrote) == 0 {
		return nil
	}
	m := manifest{Target: target}
	for name, sum := range o.wrote {
		m.Files = append(m.Files, manifestEntry{Path: name, SHA256: sum})
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return fmt.Errorf("elagoht/deploy: %s: %w", ManifestFile, err)
	}
	return create(o.dir, ManifestFile, buf.Bytes())
}

// builtFiles are the slash paths under the output of the files the build wrote.
func builtFiles(ev *collage.BuildFinishedEvent) map[string]bool {
	built := map[string]bool{}
	for _, f := range ev.Files {
		if f.File != "" {
			if rel, err := filepath.Rel(ev.OutDir, f.File); err == nil && filepath.IsLocal(rel) {
				built[filepath.ToSlash(rel)] = true
			}
		}
		built[strings.TrimPrefix(f.Path, "/")] = true
	}
	return built
}
