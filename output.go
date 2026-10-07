package deploy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Elagoht/collage/pkg/collage"
)

// claim reports, as an error naming each, every one of names already in the
// output: written by the build (a document, a mounted file) or there before
// it. The plugin owns these files for its target and never merges into one it
// did not write. It reports whether all of them are free.
func claim(ev *collage.BuildFinishedEvent, names ...string) bool {
	free := true
	for _, name := range names {
		_, err := os.Lstat(filepath.Join(ev.OutDir, filepath.FromSlash(name)))
		source, isBuilt := builtSource(ev, name)
		if err == nil || isBuilt || !errors.Is(err, fs.ErrNotExist) {
			why := "it is already in the output"
			if isBuilt {
				why = "the build wrote it (" + source + ")"
			}
			ev.Error("/"+name, "deploy-existing-file", fmt.Sprintf("%s is not written: %s, and the plugin never overwrites or merges into a file it did not write; remove it, or move its rules into the application", name, why))
			free = false
		}
	}
	return free
}

// output is the plugin's writes into a build's output directory, recorded for
// its manifest.
type output struct {
	dir string
	// wrote maps each file written, by slash path under dir, to the hex
	// SHA-256 of what was written.
	wrote map[string]string
}

func newOutput(dir string) *output { return &output{dir: dir, wrote: map[string]string{}} }

// create writes data to name under the output, refusing a file (or link)
// already there, and records it.
func (o *output) create(name string, data []byte) error {
	if err := create(o.dir, name, data); err != nil {
		return err
	}
	o.wrote[name] = digest(data)
	return nil
}

// builtSource names the build's file at name, a slash path under the output —
// its kind, and its name when it has one — and reports whether the build wrote
// one there.
func builtSource(ev *collage.BuildFinishedEvent, name string) (string, bool) {
	for _, f := range ev.Files {
		at := strings.TrimPrefix(f.Path, "/") == name
		if !at && f.File != "" {
			rel, err := filepath.Rel(ev.OutDir, f.File)
			at = err == nil && filepath.ToSlash(rel) == name
		}
		if !at {
			continue
		}
		source := f.Kind
		if f.Name != "" {
			source += " " + fmt.Sprintf("%q", f.Name)
		}
		return source, true
	}
	return "", false
}

// create writes data to name under dir, refusing a file (or link) already
// there.
func create(dir, name string, data []byte) error {
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("elagoht/deploy: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("elagoht/deploy: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("elagoht/deploy: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("elagoht/deploy: %w", err)
	}
	return nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// sortedNames are h's header names in order: http.Header is a map, and a file
// written from it has to come out the same every time.
func sortedNames(h http.Header) []string {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// left is a list of what a host could not take, for a warning.
func left(items []string) string {
	return strings.Join(items, ", ")
}
