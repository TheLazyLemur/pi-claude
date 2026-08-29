package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// workspace is the only thing the agent can touch. Every tool goes through it,
// so the sandbox is Go code rather than a line in a prompt.
type workspace struct {
	root string

	mu      sync.Mutex
	changes []change
}

type change struct {
	Path  string
	Kind  string
	Added int
	Del   int
}

// maxFiles bounds a walk. Someone will eventually point this at a home
// directory, and neither the page nor the model wants a hundred thousand paths.
const maxFiles = 5000

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	".venv": true, "dist": true, "build": true, "target": true,
}

func newWorkspace(root string) (*workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", abs)
	}
	return &workspace{root: abs}, nil
}

// resolve refuses anything outside the workspace. A model that asks for
// ../../.ssh/id_rsa gets an error it can read, not a file.
func (w *workspace) resolve(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(w.root, abs)
	}
	abs = filepath.Clean(abs)
	if abs != w.root && !strings.HasPrefix(abs, w.root+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the workspace", path)
	}
	return abs, nil
}

func (w *workspace) rel(abs string) string {
	if r, err := filepath.Rel(w.root, abs); err == nil {
		return r
	}
	return abs
}

func (w *workspace) record(c change) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.changes = append(w.changes, c)
}

func (w *workspace) Changes() []change {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]change(nil), w.changes...)
}

// walk lists the files in the workspace, stopping at maxFiles. The second
// return says whether it gave up early, so callers can say so rather than
// quietly showing a partial answer.
func (w *workspace) walk() ([]string, bool, error) {
	var out []string
	truncated := false

	err := filepath.WalkDir(w.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory is not a reason to abandon the walk.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || (d.Name() != "." && strings.HasPrefix(d.Name(), ".") && path != w.root) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(out) >= maxFiles {
			truncated = true
			return filepath.SkipAll
		}
		out = append(out, w.rel(path))
		return nil
	})

	sort.Strings(out)
	return out, truncated, err
}
