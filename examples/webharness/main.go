// Console: a browser front end for a Claude Code session where every tool is
// this program's own. Go, htmx and server-sent events. No JavaScript framework.
//
//	go run ./examples/webharness            # serves the current directory
//	go run ./examples/webharness -C ./proj -addr :8080
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	pi "github.com/TheLazyLemur/pi-claude"
)

func main() {
	dir := flag.String("C", ".", "directory the agent works in")
	addr := flag.String("addr", "127.0.0.1:7777", "address to serve on")
	model := flag.String("model", "", "model to use (default: the CLI's)")
	debug := flag.Bool("debug", false, "enable /debug/replay, which renders a fake turn")
	flag.Parse()

	ws, err := newWorkspace(*dir)
	if err != nil {
		log.Fatal(err)
	}

	hub := NewHub()
	app := NewApp(hub, ws)

	sess, err := pi.New(context.Background(), pi.Options{
		CWD:   ws.root,
		Model: *model,

		// Skill is the one built-in kept: it is how Claude Code loads a
		// project's own instructions, and reimplementing it would be silly.
		// Everything else the agent can do is ours.
		Tools:           []string{"Skill"},
		StrictMCPConfig: true,
		CustomTools:     app.tools(),

		SystemPrompt: systemPrompt,
		MaxTurns:     60,

		PermissionMode: pi.PermissionModeDefault,
		ApproveTool: func(_ context.Context, req pi.ToolRequest) pi.Decision {
			if req.Mine || req.Name == "Skill" {
				return pi.Allow()
			}
			return pi.Deny("this console only exposes its own tools")
		},

		Stderr: func(line string) { fmt.Fprintln(os.Stderr, "claude:", line) },
	})
	if err != nil {
		log.Fatal(err)
	}
	defer sess.Close()

	app.sess = sess
	app.watch()

	files, _ := ws.walk()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, renderPage(
			filepath.Base(ws.root), shortPath(ws.root),
			meterHTML(0, 0, 0, 0), modesHTML(app.Mode()), len(files), seeds(files)))
	})
	mux.Handle("GET /events", hub)

	mux.HandleFunc("POST /prompt", func(w http.ResponseWriter, r *http.Request) {
		text := strings.TrimSpace(r.FormValue("prompt"))
		if text == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		app.Ask(text)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /interrupt", func(w http.ResponseWriter, r *http.Request) {
		sess.Interrupt()
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /mode", func(w http.ResponseWriter, r *http.Request) {
		m := r.URL.Query().Get("m")
		if m != "plan" && m != "act" {
			http.Error(w, "mode must be plan or act", http.StatusBadRequest)
			return
		}
		app.SetMode(m)
		w.WriteHeader(http.StatusNoContent)
	})

	// A synthetic turn, so the rendering path can be exercised without paying a
	// model to reproduce something. Off unless asked for.
	mux.HandleFunc("POST /debug/replay", func(w http.ResponseWriter, r *http.Request) {
		if !*debug {
			http.NotFound(w, r)
			return
		}
		for i, name := range []string{"read_file", "write_file", "edit_file"} {
			id := fmt.Sprintf("dbg-%d", i)
			arg := "cart.go"
			app.queueCard(name, id, arg)
			hub.Send("msg", toolHTML(id, name, arg))
			hub.Send("rail", tickHTML(id, name, arg, kindOf(name), true, false))
		}
		app.settle("read_file", "read")
		app.showDiff(app.claimCard("write_file"), "write", "new.go", "", "package new\n\nfunc A() {}\n")
		app.showDiff(app.claimCard("edit_file"), "edit", "cart.go",
			"package cart\n\nfunc Total() int {\n\treturn 0\n}\n",
			"package cart\n\nfunc Total() int {\n\treturn 42\n}\n")
		w.WriteHeader(http.StatusNoContent)
	})

	fmt.Printf("console  http://%s\n", *addr)
	fmt.Printf("agent    %s (%d files)\n", ws.root, len(files))
	fmt.Printf("tools    6 custom, plus Skill. No other built-ins.\n\n")

	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

// watch turns session events into page updates.
func (a *App) watch() {
	a.sess.Subscribe(func(ev pi.Event) {
		switch e := ev.(type) {
		case pi.TextEvent:
			a.hub.Send("msg", agentHTML(e.Text))

		case pi.ThinkingEvent:
			a.hub.Send("msg", thinkHTML(e.Text))

		case pi.ToolCallEvent:
			arg := summarise(e.Input)
			a.queueCard(e.Name, e.ID, arg)
			a.hub.Send("msg", toolHTML(e.ID, e.Name, arg)+"")
			a.hub.Send("rail", tickHTML(e.ID, e.Name, arg, kindOf(e.Name), true, false))

		case pi.DeniedEvent:
			a.hub.Send("msg", fmt.Sprintf(
				`<div class="tool deny"><header><span class="name">%s</span></header><div class="note">%s</div></div>`,
				esc(e.Name), esc(e.Reason)))

		case pi.TurnEvent:
			a.finish(e.Turn)

		case pi.ErrorEvent:
			a.hub.Send("msg", fmt.Sprintf(
				`<div class="tool err"><header><span class="name">error</span></header><div class="note">%s</div></div>`,
				esc(e.Err.Error())))
		}
	})
}

// Ask sends a prompt, unless a turn is already running.
func (a *App) Ask(text string) {
	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		a.hub.Send("msg", `<div class="tool deny"><header><span class="name">busy</span></header>`+
			`<div class="note">Still working on the last one. Stop it first, or wait.</div></div>`)
		return
	}
	a.busy = true
	a.mu.Unlock()

	a.hub.Send("msg", userHTML(text))
	a.hub.Send("msg", `<button class="stop" id="stop" data-live="1" hx-post="/interrupt" hx-swap="none" hx-swap-oob="true">Stop</button>`)

	go func() {
		if _, err := a.sess.Prompt(context.Background(), text); err != nil {
			a.hub.Send("msg", fmt.Sprintf(
				`<div class="tool err"><header><span class="name">failed</span></header><div class="note">%s</div></div>`,
				esc(err.Error())))
			a.idle()
		}
	}()
}

func (a *App) finish(t pi.Turn) {
	a.mu.Lock()
	a.turns += t.Turns
	a.cost += t.CostUSD
	a.in += t.Usage.InputTokens
	a.out += t.Usage.OutputTokens
	turns, cost, in, out := a.turns, a.cost, a.in, a.out
	a.mu.Unlock()

	a.hub.Send("meter", meterHTML(turns, cost, in, out))
	a.idle()
}

func (a *App) idle() {
	a.mu.Lock()
	a.busy = false
	a.pending = map[string][]string{}
	a.mu.Unlock()
	a.hub.Send("msg", `<button class="stop" id="stop" data-live="0" hx-post="/interrupt" hx-swap="none" hx-swap-oob="true">Stop</button>`)
}

// kindOf colours a rail tick by what the tool does.
func kindOf(name string) string {
	switch name {
	case "write_file", "edit_file":
		return "write"
	default:
		return "read"
	}
}

// summarise is the one-line version of a tool's arguments for the rail.
func summarise(input map[string]any) string {
	for _, key := range []string{"path", "pattern", "dir"} {
		if v, ok := input[key].(string); ok && v != "" {
			return v
		}
	}
	if items, ok := input["items"].([]any); ok {
		return fmt.Sprintf("%d items", len(items))
	}
	if v, ok := input["command"].(string); ok {
		return v
	}
	return ""
}

// seeds are the openers offered on an empty console. They are suggestions, so
// they name the work rather than the tools.
func seeds(files []string) []string {
	out := []string{"Explain what this code does"}
	for _, f := range files {
		if strings.HasSuffix(f, ".go") || strings.HasSuffix(f, ".py") || strings.HasSuffix(f, ".ts") {
			out = append(out, "Find the bug in "+f, "Add tests for "+f)
			break
		}
	}
	return append(out, "What would you change first?")
}

// shortPath keeps a path readable in a header: home becomes ~, and anything
// still long is cut to its last few segments.
func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		p = "~" + strings.TrimPrefix(p, home)
	}
	if len(p) <= 44 {
		return p
	}
	parts := strings.Split(strings.TrimSuffix(p, "/"), "/")
	if len(parts) <= 3 {
		return p
	}
	return ".../" + strings.Join(parts[len(parts)-3:], "/")
}
