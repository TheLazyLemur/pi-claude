package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	pi "github.com/TheLazyLemur/pi-claude"
)

type todo struct {
	Task   string `json:"task" desc:"Short description of the piece of work"`
	Status string `json:"status" desc:"pending, doing or done"`
}

// App holds everything one browser session drives.
type App struct {
	hub  *Hub
	ws   *workspace
	sess *pi.Session

	mu      sync.Mutex
	mode    string // "plan" or "act"
	todos   []todo
	turns   int
	cost    float64
	in, out int
	busy    bool
	pending map[string][]string // tool name -> card ids awaiting execution
	ticks   map[string]tick     // card id -> what its rail entry says
}

// tick is what a rail entry shows, kept so the entry can be redrawn whole when
// its tool finishes. htmx replaces elements, not attributes.
type tick struct {
	Name string
	Arg  string
}

func NewApp(hub *Hub, ws *workspace) *App {
	return &App{
		hub:     hub,
		ws:      ws,
		mode:    "act",
		pending: map[string][]string{},
		ticks:   map[string]tick{},
	}
}

// queueCard remembers a card so the tool can find it when it runs. The CLI
// announces every tool call in a message before running any of them, so one
// "current card" pointer would break the moment two calls arrive together.
func (a *App) queueCard(name, id, arg string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pending[name] = append(a.pending[name], id)
	a.ticks[id] = tick{Name: name, Arg: arg}
}

func (a *App) claimCard(name string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	q := a.pending[name]
	if len(q) == 0 {
		return ""
	}
	a.pending[name] = q[1:]
	return q[0]
}

func (a *App) Mode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *App) SetMode(m string) {
	a.mu.Lock()
	a.mode = m
	a.mu.Unlock()
	a.hub.Send("modes", modesHTML(m))
}

func (a *App) planning() bool { return a.Mode() == "plan" }

const systemPrompt = `You are a coding agent working in one directory, driven from a web console.

Every tool you have is provided by that console. There is no shell and no
network. Nothing outside the directory is reachable: paths that escape it are
refused, not redirected. You cannot run tests or builds, so never say you have.

Work in small steps and say what you are doing as you go, briefly. Read before
you write, and prefer edit_file over write_file so you do not throw away work
you have not seen. Match the conventions already in the files you touch.

Use todo_write when a task has more than two steps. Keep it current: mark one
item doing, mark it done before starting the next. The person watching uses it
to know where you are.

In plan mode the file-writing tools are refused. Read, investigate, and say what
you would change and why. Do not fight the refusal.

Be terse. No preamble, no summaries of what you just did unless asked.`

func (a *App) tools() []pi.Tool {
	w := a.ws

	type listParams struct {
		Dir string `json:"dir,omitempty" desc:"Directory relative to the root. Defaults to the root."`
	}
	type readParams struct {
		Path      string `json:"path" desc:"File path relative to the root"`
		StartLine int    `json:"start_line,omitempty" desc:"First line, 1-based"`
		EndLine   int    `json:"end_line,omitempty" desc:"Last line, inclusive"`
	}
	type searchParams struct {
		Pattern string `json:"pattern" desc:"Substring to look for"`
		Glob    string `json:"glob,omitempty" desc:"Optional filename glob such as *.go"`
	}
	type writeParams struct {
		Path    string `json:"path" desc:"File path relative to the root"`
		Content string `json:"content" desc:"The complete new contents"`
	}
	type editParams struct {
		Path string `json:"path" desc:"File path relative to the root"`
		Old  string `json:"old" desc:"Exact text to replace. Must appear exactly once."`
		New  string `json:"new" desc:"Replacement text"`
	}
	type todoParams struct {
		Items []todo `json:"items" desc:"The full task list, replacing whatever was there"`
	}

	return []pi.Tool{
		pi.DefineTool("list_files", "List the files in the workspace.",
			func(_ context.Context, p listParams) (pi.ToolResult, error) {
				paths, err := w.walk()
				if err != nil {
					return pi.Errorf("could not list files: %v", err), nil
				}
				if p.Dir != "" && p.Dir != "." {
					var keep []string
					for _, path := range paths {
						if strings.HasPrefix(path, p.Dir) {
							keep = append(keep, path)
						}
					}
					paths = keep
				}
				a.settle("list_files", "read")
				if len(paths) == 0 {
					return pi.Text("no files"), nil
				}
				return pi.Text("%s", strings.Join(paths, "\n")), nil
			}),

		pi.DefineTool("read_file", "Read a file. Returns it with line numbers.",
			func(_ context.Context, p readParams) (pi.ToolResult, error) {
				defer a.settle("read_file", "read")
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
				defer a.settle("search", "read")
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

		pi.DefineTool("write_file", "Create a file or replace its whole contents.",
			func(_ context.Context, p writeParams) (pi.ToolResult, error) {
				card := a.claimCard("write_file")
				if a.planning() {
					a.refuse(card, "write_file")
					return pi.Errorf("plan mode: describe the change instead of writing it"), nil
				}
				abs, err := w.resolve(p.Path)
				if err != nil {
					a.fail(card, err.Error())
					return pi.Errorf("%v", err), nil
				}
				before, _ := os.ReadFile(abs)
				if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
					a.fail(card, err.Error())
					return pi.Errorf("could not create directory: %v", err), nil
				}
				if err := os.WriteFile(abs, []byte(p.Content), 0o644); err != nil {
					a.fail(card, err.Error())
					return pi.Errorf("could not write %s: %v", p.Path, err), nil
				}
				a.showDiff(card, "write", p.Path, string(before), p.Content)
				return pi.Text("wrote %s (%d bytes)", p.Path, len(p.Content)), nil
			}),

		pi.DefineTool("edit_file", "Replace an exact string in a file. It must appear exactly once.",
			func(_ context.Context, p editParams) (pi.ToolResult, error) {
				card := a.claimCard("edit_file")
				if a.planning() {
					a.refuse(card, "edit_file")
					return pi.Errorf("plan mode: describe the change instead of making it"), nil
				}
				abs, err := w.resolve(p.Path)
				if err != nil {
					a.fail(card, err.Error())
					return pi.Errorf("%v", err), nil
				}
				data, err := os.ReadFile(abs)
				if err != nil {
					a.fail(card, err.Error())
					return pi.Errorf("could not read %s: %v", p.Path, err), nil
				}
				switch strings.Count(string(data), p.Old) {
				case 0:
					a.fail(card, "that exact text is not in "+p.Path)
					return pi.Errorf("that exact text is not in %s", p.Path), nil
				case 1:
				default:
					a.fail(card, "that text appears more than once; include more context")
					return pi.Errorf("that text appears more than once in %s; include more context", p.Path), nil
				}
				updated := strings.Replace(string(data), p.Old, p.New, 1)
				if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
					a.fail(card, err.Error())
					return pi.Errorf("could not write %s: %v", p.Path, err), nil
				}
				a.showDiff(card, "edit", p.Path, string(data), updated)
				return pi.Text("edited %s", p.Path), nil
			}),

		pi.DefineTool("todo_write", "Set the task list the person is watching. Send the whole list every time.",
			func(_ context.Context, p todoParams) (pi.ToolResult, error) {
				a.mu.Lock()
				a.todos = p.Items
				a.mu.Unlock()
				a.hub.Send("todos", todosHTML(p.Items))
				a.settle("todo_write", "read")
				return pi.Text("task list updated (%d items)", len(p.Items)), nil
			}),
	}
}

// settle stops a tick pulsing once its tool has finished.
func (a *App) settle(name, kind string) {
	a.settleCard(a.claimCard(name), kind)
}

func (a *App) settleCard(card, kind string) {
	if card == "" {
		return
	}
	a.mu.Lock()
	t := a.ticks[card]
	a.mu.Unlock()
	a.hub.Send("msg", tickHTML(card, t.Name, t.Arg, kind, false, true))
}

func (a *App) showDiff(card, kind, path, before, after string) {
	hunks := unifiedDiff(before, after)
	added, removed := countChanges(hunks)
	a.ws.record(change{Path: path, Kind: kind, Added: added, Del: removed})
	if card == "" {
		return
	}
	a.hub.Send("msg", diffHTML(card, hunks))
	a.settleCard(card, "write")
}

func (a *App) fail(card, msg string) {
	if card == "" {
		return
	}
	a.hub.Send("msg", fmt.Sprintf(
		`<div id="%s" hx-swap-oob="beforeend"><div class="note">%s</div></div>`, card, esc(msg)))
	a.settleCard(card, "deny")
}

func (a *App) refuse(card, name string) {
	if card == "" {
		return
	}
	a.hub.Send("msg", fmt.Sprintf(
		`<div id="%s" hx-swap-oob="beforeend"><div class="note">Refused. Plan mode is on, so %s cannot run.</div></div>`,
		card, esc(name)))
	a.settleCard(card, "deny")
}
