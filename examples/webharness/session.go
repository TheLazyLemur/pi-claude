package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	pi "github.com/TheLazyLemur/pi-claude"
	"github.com/TheLazyLemur/pi-claude/external/anthropic"
)

// Session is one conversation: its own claude subprocess, its own directory,
// and its own transcript. A session in a worktree has a checkout to itself, so
// two of them working on the same project cannot tread on each other.
type Session struct {
	ID          string
	WorkspaceID string
	Title       string
	Root        string
	Branch      string
	Worktree    bool
	Created     time.Time

	hub  *Hub
	ws   *workspace
	sess *pi.Session

	mu      sync.Mutex
	mode    string
	todos   []todo
	turns   int
	cost    float64
	in, out int
	busy    bool
	pending map[string][]string
	entries []*entry
	byCard  map[string]*entry
	killCmd context.CancelFunc
	shell   bool
}

// entry is one thing that happened, kept as state rather than as the HTML that
// announced it. Live updates go out as out-of-band fragments, which only apply
// during a swap; a tab opening later needs the finished article, so the page is
// rendered from these.
type entry struct {
	Kind string // user, agent, think, tool, deny, error
	Text string

	ID, Name, Arg string
	Hunks         []hunk
	Note          string
	Settled       string
	Rail          bool

	// shell only
	Output  string
	Exit    int
	HasExit bool
}

// NewSession starts a claude subprocess for one piece of work.
func NewSession(id, workspaceID, root, branch string, worktree bool, hub *Hub, model string, shell bool) (*Session, error) {
	ws, err := newWorkspace(root)
	if err != nil {
		return nil, err
	}

	s := &Session{
		ID:          id,
		WorkspaceID: workspaceID,
		Root:        ws.root,
		Branch:      branch,
		Worktree:    worktree,
		Created:     time.Now(),
		hub:         hub,
		ws:          ws,
		mode:        "act",
		shell:       shell,
		pending:     map[string][]string{},
		byCard:      map[string]*entry{},
	}

	agent, err := pi.Open(context.Background(), anthropic.New(), pi.Options{
		CWD:   ws.root,
		Model: model,

		// Skill is the one built-in kept: it is how Claude Code loads a
		// project's own instructions, and reimplementing it would be silly.
		// Everything else the agent can do is defined in this program.
		StrictMCPConfig: true,
		CustomTools:     s.tools(),

		SystemPrompt: promptFor(shell),
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
		return nil, err
	}

	s.sess = agent
	s.watch()
	return s, nil
}

func (s *Session) Close() error { return s.sess.Close() }

// emit pushes a fragment to any tab watching this session.
func (s *Session) emit(event, html string) {
	s.hub.Send(s.ID, event, html)
}

// note records something that happened, so it can be rendered again later.
func (s *Session) note(e *entry) *entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
	if e.ID != "" {
		s.byCard[e.ID] = e
	}
	return e
}

// updateCard changes a recorded card under the lock. Entries are shared with
// whatever is rendering a replay, so they are never touched unguarded.
func (s *Session) updateCard(id string, fn func(*entry)) *entry {
	s.mu.Lock()
	defer s.mu.Unlock()

	e := s.byCard[id]
	if e != nil {
		fn(e)
	}
	return e
}

// Replay renders the transcript and the rail as they now stand.
func (s *Session) Replay() (stream, rail string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var t, r strings.Builder
	for _, e := range s.entries {
		t.WriteString(renderEntry(e))
		if e.Rail {
			settled := e.Settled
			live := settled == ""
			if live {
				settled = kindOf(e.Name)
			}
			r.WriteString(tickHTML(e.ID, e.Name, e.Arg, settled, live, false))
		}
	}
	return t.String(), r.String()
}

func (s *Session) Mode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

func (s *Session) SetMode(m string) {
	s.mu.Lock()
	s.mode = m
	s.mu.Unlock()
	s.hub.Send(s.ID, "modes", modesHTML(s.ID, m))
}

func (s *Session) planning() bool { return s.Mode() == "plan" }

func (s *Session) Meter() (turns int, cost float64, in, out int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turns, s.cost, s.in, s.out
}

func (s *Session) setTodos(items []todo) {
	s.mu.Lock()
	s.todos = items
	s.mu.Unlock()
}

func (s *Session) Todos() []todo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]todo(nil), s.todos...)
}

// queueCard remembers a card so the tool can find it when it runs. The CLI
// announces every tool call in a message before running any of them, so one
// "current card" pointer would break the moment two arrive together.
func (s *Session) queueCard(name, id, arg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[name] = append(s.pending[name], id)
}

func (s *Session) claimCard(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.pending[name]
	if len(q) == 0 {
		return ""
	}
	s.pending[name] = q[1:]
	return q[0]
}

// watch turns agent events into page updates.
func (s *Session) watch() {
	s.sess.Subscribe(func(ev pi.Event) {
		switch e := ev.(type) {
		case pi.TextEvent:
			s.emit("msg", renderEntry(s.note(&entry{Kind: "agent", Text: e.Text})))

		case pi.ThinkingEvent:
			// Withheld reasoning has nothing to show.
			if e.Text == "" {
				return
			}
			s.emit("msg", renderEntry(s.note(&entry{Kind: "think", Text: e.Text})))

		case pi.ToolCallEvent:
			arg := summarise(e.Input)
			s.queueCard(e.Name, e.ID, arg)
			s.note(&entry{Kind: "tool", ID: e.ID, Name: e.Name, Arg: arg, Rail: true})
			s.emit("msg", toolHTML(e.ID, e.Name, arg))
			s.emit("rail", tickHTML(e.ID, e.Name, arg, kindOf(e.Name), true, false))

		case pi.DeniedEvent:
			s.emit("msg", renderEntry(s.note(&entry{Kind: "deny", Name: e.Name, Text: e.Reason})))

		case pi.TurnEvent:
			s.finish(e.Turn)

		case pi.ErrorEvent:
			s.emit("msg", renderEntry(s.note(&entry{Kind: "error", Name: "error", Text: e.Err.Error()})))
		}
	})
}

// Ask sends a prompt, unless a turn is already running.
func (s *Session) Ask(text string) {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		s.emit("msg", `<div class="tool deny"><header><span class="name">busy</span></header>`+
			`<div class="note">Still working on the last one. Stop it first, or wait.</div></div>`)
		return
	}
	s.busy = true
	if s.Title == "" {
		s.Title = text
	}
	s.mu.Unlock()

	s.emit("msg", renderEntry(s.note(&entry{Kind: "user", Text: text})))
	s.hub.Send(s.ID, "msg", strings.ReplaceAll(stopHTML(true), "{{SID}}", s.ID))

	go func() {
		if _, err := s.sess.Prompt(context.Background(), text); err != nil {
			s.emit("msg", renderEntry(s.note(&entry{Kind: "error", Name: "failed", Text: err.Error()})))
			s.idle()
		}
	}()
}

// Interrupt stops the turn, and any command it left running.
func (s *Session) Interrupt() error {
	s.mu.Lock()
	kill := s.killCmd
	s.mu.Unlock()
	if kill != nil {
		kill()
	}
	return s.sess.Interrupt()
}

// running records how to stop the command currently in flight.
func (s *Session) running(cancel context.CancelFunc) {
	s.mu.Lock()
	s.killCmd = cancel
	s.mu.Unlock()
}

func (s *Session) finish(t pi.Turn) {
	s.mu.Lock()
	s.turns += t.Turns
	s.cost += t.CostUSD
	s.in += t.Usage.InputTokens
	s.out += t.Usage.OutputTokens
	turns, cost, in, out := s.turns, s.cost, s.in, s.out
	s.mu.Unlock()

	s.hub.Send(s.ID, "meter", meterHTML(turns, cost, in, out))
	s.idle()
}

func (s *Session) idle() {
	s.mu.Lock()
	s.busy = false
	s.pending = map[string][]string{}
	s.mu.Unlock()
	s.hub.Send(s.ID, "msg", strings.ReplaceAll(stopHTML(false), "{{SID}}", s.ID))
}
