// Console: a browser front end for Claude Code sessions where every tool is
// this program's own. Go, htmx and server-sent events. No JavaScript framework.
//
//	go run ./examples/webharness                       # the current directory
//	go run ./examples/webharness ../a ../b             # several projects
//	go run ./examples/webharness -addr :8080 ../a      # flags come first
//
// Each session is its own claude process with its own transcript. A session can
// be opened in a git worktree, so two of them on one project never collide.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:7777", "address to serve on")
	model := flag.String("model", "", "model to use (default: the CLI's)")
	debug := flag.Bool("debug", false, "enable /debug/replay, which renders a fake turn")
	flag.Parse()

	roots := flag.Args()
	if len(roots) == 0 {
		roots = []string{"."}
	}

	hub := NewHub()
	store := NewStore()
	for _, root := range roots {
		w, err := store.AddWorkspace(root)
		if err != nil {
			log.Fatalf("%s: %v", root, err)
		}
		fmt.Printf("project  %s%s\n", w.Root, map[bool]string{true: "  (git)"}[w.IsRepo])
	}

	srv := &server{store: store, hub: hub, model: *model, debug: *debug}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", srv.index)
	mux.HandleFunc("GET /s/{id}", srv.session)
	mux.HandleFunc("POST /s/{id}/prompt", srv.prompt)
	mux.HandleFunc("POST /s/{id}/interrupt", srv.interrupt)
	mux.HandleFunc("POST /s/{id}/mode", srv.mode)
	mux.HandleFunc("POST /sessions", srv.create)
	mux.Handle("GET /events", hub)
	mux.HandleFunc("POST /debug/replay", srv.replay)

	fmt.Printf("console  http://%s\n", *addr)
	fmt.Printf("tools    6 custom, plus Skill. No other built-ins.\n\n")

	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

type server struct {
	store *Store
	hub   *Hub
	model string
	debug bool
}

// index sends you to the newest session, or offers a blank one on the first
// project when there is nothing yet.
func (s *server) index(w http.ResponseWriter, r *http.Request) {
	if all := s.store.Sessions(); len(all) > 0 {
		http.Redirect(w, r, "/s/"+all[0].ID, http.StatusSeeOther)
		return
	}

	ws := s.store.Workspaces()
	if len(ws) == 0 {
		http.Error(w, "no projects", http.StatusInternalServerError)
		return
	}

	sess, err := s.start(ws[0], false, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/s/"+sess.ID, http.StatusSeeOther)
}

func (s *server) session(w http.ResponseWriter, r *http.Request) {
	sess := s.store.Session(r.PathValue("id"))
	if sess == nil {
		http.NotFound(w, r)
		return
	}

	byID := map[string]*Workspace{}
	for _, ws := range s.store.Workspaces() {
		byID[ws.ID] = ws
	}

	stream, rail := sess.Replay()

	files, _, _ := sess.ws.walk()
	turns, cost, in, out := sess.Meter()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, renderPage(pageData{
		Title:    filepath.Base(sess.Root),
		Root:     shortPath(sess.Root),
		Badge:    badgeHTML(sess.Branch),
		Meter:    meterHTML(turns, cost, in, out),
		Modes:    modesHTML(sess.ID, sess.Mode()),
		Files:    len(files),
		Seeds:    seeds(files),
		Sessions: sessionsHTML(s.store.Sessions(), byID, sess.ID),
		Options:  optionsHTML(s.store.Workspaces(), sess.WorkspaceID),
		Stream:   stream,
		Rail:     rail,
		Todos:    todosHTML(sess.Todos()),
		SID:      sess.ID,
		Error:    r.URL.Query().Get("err"),
	}))
}

func (s *server) prompt(w http.ResponseWriter, r *http.Request) {
	sess := s.store.Session(r.PathValue("id"))
	if sess == nil {
		http.NotFound(w, r)
		return
	}
	if text := strings.TrimSpace(r.FormValue("prompt")); text != "" {
		sess.Ask(text)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) interrupt(w http.ResponseWriter, r *http.Request) {
	if sess := s.store.Session(r.PathValue("id")); sess != nil {
		sess.Interrupt()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) mode(w http.ResponseWriter, r *http.Request) {
	sess := s.store.Session(r.PathValue("id"))
	m := r.URL.Query().Get("m")
	if sess == nil || (m != "plan" && m != "act") {
		http.Error(w, "unknown session or mode", http.StatusBadRequest)
		return
	}
	sess.SetMode(m)
	w.WriteHeader(http.StatusNoContent)
}

// create starts a session, adding the project first if it is a new one, and
// sends the first message straight into it.
func (s *server) create(w http.ResponseWriter, r *http.Request) {
	ws, err := s.pick(r)
	if err != nil {
		s.back(w, r, err)
		return
	}

	first := strings.TrimSpace(r.FormValue("prompt"))
	sess, err := s.start(ws, r.FormValue("worktree") == "1", first)
	if err != nil {
		s.back(w, r, err)
		return
	}
	http.Redirect(w, r, "/s/"+sess.ID, http.StatusSeeOther)
}

// pick resolves the chosen project, adding it when the person typed a path for
// one the console has not seen before.
func (s *server) pick(r *http.Request) (*Workspace, error) {
	if id := r.FormValue("workspace"); id != "new" {
		ws := s.store.Workspace(id)
		if ws == nil {
			return nil, fmt.Errorf("that project is no longer here")
		}
		return ws, nil
	}

	path, err := expandPath(r.FormValue("path"))
	if err != nil {
		return nil, err
	}
	return s.store.AddWorkspace(path)
}

// back returns to where the person was, carrying what went wrong.
func (s *server) back(w http.ResponseWriter, r *http.Request, err error) {
	to := "/"
	if all := s.store.Sessions(); len(all) > 0 {
		to = "/s/" + all[0].ID
	}
	http.Redirect(w, r, to+"?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
}

// start builds a session and, when asked, the worktree it lives in.
func (s *server) start(ws *Workspace, worktree bool, first string) (*Session, error) {
	root, branch := ws.Root, ""

	if worktree {
		repo := repoRoot(ws.Root)
		if repo == "" {
			return nil, fmt.Errorf("%s is not a git repository, so it has no worktrees", ws.Name)
		}
		name := sessionName(first) + "-" + strings.TrimPrefix(s.store.nextID(), "s")
		path, br, err := addWorktree(repo, name)
		if err != nil {
			return nil, err
		}
		root, branch = path, br
	}

	sess, err := NewSession(s.store.nextID(), ws.ID, root, branch, worktree, s.hub, s.model)
	if err != nil {
		return nil, err
	}
	sess.Title = first
	s.store.put(sess)

	// The browser has not connected to this session's stream yet, so the first
	// message waits for the redirect to land.
	if first != "" {
		go func() {
			waitForWatcher(s.hub, sess.ID)
			sess.Ask(first)
		}()
	}
	return sess, nil
}

// replay renders a fake turn so the UI can be worked on without paying a model.
func (s *server) replay(w http.ResponseWriter, r *http.Request) {
	if !s.debug {
		http.NotFound(w, r)
		return
	}
	sess := s.store.Session(r.URL.Query().Get("s"))
	if sess == nil {
		http.Error(w, "unknown session", http.StatusBadRequest)
		return
	}

	sess.emit("msg", renderEntry(sess.note(&entry{Kind: "user", Text: "a fake turn, for working on the UI"})))
	for i, name := range []string{"read_file", "write_file", "edit_file"} {
		id := fmt.Sprintf("dbg-%d", i)
		sess.queueCard(name, id, "cart.go")
		sess.note(&entry{Kind: "tool", ID: id, Name: name, Arg: "cart.go", Rail: true})
		sess.emit("msg", toolHTML(id, name, "cart.go"))
		sess.emit("rail", tickHTML(id, name, "cart.go", kindOf(name), true, false))
	}
	sess.settle("read_file", "read")
	sess.showDiff(sess.claimCard("write_file"), "write", "new.go", "", "package new\n\nfunc A() {}\n")
	sess.showDiff(sess.claimCard("edit_file"), "edit", "cart.go",
		"package cart\n\nfunc Total() int {\n\treturn 0\n}\n",
		"package cart\n\nfunc Total() int {\n\treturn 42\n}\n")
	w.WriteHeader(http.StatusNoContent)
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
	return ""
}

// seeds are the openers offered on an empty console. They name the work rather
// than the tools.
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
