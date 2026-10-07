package deploy

import (
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
	built := map[string]string{}
	for _, f := range ev.Files {
		source := f.Kind
		if f.Name != "" {
			source += " " + fmt.Sprintf("%q", f.Name)
		}
		built[strings.TrimPrefix(f.Path, "/")] = source
	}
	free := true
	for _, name := range names {
		_, err := os.Lstat(filepath.Join(ev.OutDir, filepath.FromSlash(name)))
		source, isBuilt := built[name]
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
