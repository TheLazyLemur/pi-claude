package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	pi "github.com/TheLazyLemur/pi-claude"
)

type listParams struct {
	Dir string `json:"dir,omitempty" desc:"Directory relative to the workspace root. Defaults to the root."`
}

type readParams struct {
	Path      string `json:"path" desc:"File path relative to the workspace root"`
	StartLine int    `json:"start_line,omitempty" desc:"First line to return, 1-based"`
	EndLine   int    `json:"end_line,omitempty" desc:"Last line to return, inclusive"`
}

type searchParams struct {
	Pattern string `json:"pattern" desc:"Substring to search for"`
	Glob    string `json:"glob,omitempty" desc:"Optional filename glob, e.g. *.go"`
}

type writeParams struct {
	Path    string `json:"path" desc:"File path relative to the workspace root"`
	Content string `json:"content" desc:"The complete new contents of the file"`
}

type editParams struct {
	Path string `json:"path" desc:"File path relative to the workspace root"`
	Old  string `json:"old" desc:"Exact text to replace. Must appear exactly once."`
	New  string `json:"new" desc:"Replacement text"`
}

// toolsFor builds the agent's entire tool surface. Nothing else is reachable.
func toolsFor(w *workspace) []pi.Tool {
	return []pi.Tool{
		pi.DefineTool("list_files", "List the files in the workspace.",
			func(_ context.Context, p listParams) (pi.ToolResult, error) {
				paths, err := w.walk()
				if err != nil {
					return pi.Errorf("could not list files: %v", err), nil
				}
				if p.Dir != "" && p.Dir != "." {
					var filtered []string
					for _, path := range paths {
						if strings.HasPrefix(path, p.Dir) {
							filtered = append(filtered, path)
						}
					}
					paths = filtered
				}
				if len(paths) == 0 {
					return pi.Text("no files"), nil
				}
				return pi.Text("%s", strings.Join(paths, "\n")), nil
			}),

		pi.DefineTool("read_file", "Read a file. Returns the contents with line numbers.",
			func(_ context.Context, p readParams) (pi.ToolResult, error) {
				abs, err := w.resolve(p.Path)
				if err != nil {
					return pi.Errorf("%v", err), nil
				}
				data, err := os.ReadFile(abs)
				if err != nil {
					return pi.Errorf("could not read %s: %v", p.Path, err), nil
				}

				lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
				first, last := 1, len(lines)
				if p.StartLine > 0 {
					first = max(first, p.StartLine)
				}
				if p.EndLine > 0 {
					last = min(last, p.EndLine)
				}

				var b strings.Builder
				for i := first; i <= last; i++ {
					fmt.Fprintf(&b, "%d\t%s\n", i, lines[i-1])
				}
				return pi.Text("%s", b.String()), nil
			}),

		pi.DefineTool("search", "Search file contents for a substring.",
			func(_ context.Context, p searchParams) (pi.ToolResult, error) {
				if p.Pattern == "" {
					return pi.Errorf("pattern must not be empty"), nil
				}
				paths, err := w.walk()
				if err != nil {
					return pi.Errorf("could not search: %v", err), nil
				}

				var hits []string
				for _, rel := range paths {
					if p.Glob != "" {
						if ok, _ := filepath.Match(p.Glob, filepath.Base(rel)); !ok {
							continue
						}
					}
					abs, err := w.resolve(rel)
					if err != nil {
						continue
					}
					data, err := os.ReadFile(abs)
					if err != nil {
						continue
					}
					for i, line := range strings.Split(string(data), "\n") {
						if strings.Contains(line, p.Pattern) {
							hits = append(hits, fmt.Sprintf("%s:%d:%s", rel, i+1, line))
						}
					}
				}
				if len(hits) == 0 {
					return pi.Text("no matches"), nil
				}
				return pi.Text("%s", strings.Join(hits, "\n")), nil
			}),

		pi.DefineTool("write_file", "Create a file or replace its entire contents.",
			func(_ context.Context, p writeParams) (pi.ToolResult, error) {
				abs, err := w.resolve(p.Path)
				if err != nil {
					return pi.Errorf("%v", err), nil
				}
				if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
					return pi.Errorf("could not create directory: %v", err), nil
				}
				if err := os.WriteFile(abs, []byte(p.Content), 0o644); err != nil {
					return pi.Errorf("could not write %s: %v", p.Path, err), nil
				}
				w.record(p.Path, "write", len(p.Content))
				return pi.Text("wrote %s (%d bytes)", p.Path, len(p.Content)), nil
			}),

		pi.DefineTool("edit_file", "Replace an exact string in a file. The string must appear exactly once.",
			func(_ context.Context, p editParams) (pi.ToolResult, error) {
				abs, err := w.resolve(p.Path)
				if err != nil {
					return pi.Errorf("%v", err), nil
				}
				data, err := os.ReadFile(abs)
				if err != nil {
					return pi.Errorf("could not read %s: %v", p.Path, err), nil
				}

				// Refusing an ambiguous edit is the whole point of taking the
				// tool into your own code: the model gets told, and retries.
				switch strings.Count(string(data), p.Old) {
				case 0:
					return pi.Errorf("that exact text is not in %s", p.Path), nil
				case 1:
				default:
					return pi.Errorf("that text appears more than once in %s; include more context", p.Path), nil
				}

				updated := strings.Replace(string(data), p.Old, p.New, 1)
				if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
					return pi.Errorf("could not write %s: %v", p.Path, err), nil
				}
				w.record(p.Path, "edit", len(p.New)-len(p.Old))
				return pi.Text("edited %s", p.Path), nil
			}),
	}
}
