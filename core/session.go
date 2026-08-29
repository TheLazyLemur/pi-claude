package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	// ErrSessionClosed is returned once a session has been closed.
	ErrSessionClosed = errors.New("pi: session closed")

	// ErrPromptInFlight is returned by Prompt when the session is already
	// working on a turn. One session runs one prompt at a time; start another
	// session if you want another turn in parallel.
	ErrPromptInFlight = errors.New("pi: a prompt is already in flight")
)

// Session is a running conversation. It owns the host's side of things: the
// tools, the policy, and the subscribers. The protocol belongs to whichever
// Backend it was opened with.
//
// Session implements [Runtime], which is how a backend reaches back in.
type Session struct {
	conv  Conversation
	opts  Options
	tools *registry
	hooks map[string]HookFunc

	mu       sync.RWMutex
	closed   bool
	subs     map[int]func(Event)
	nextSub  int
	inFlight bool
	text     strings.Builder
}

// Open starts a session on a backend.
func Open(ctx context.Context, backend Backend, opts Options, toolServer string) (*Session, error) {
	s := &Session{
		opts:  opts,
		tools: newRegistry(opts.CustomTools),
		hooks: hookCallbacks(opts.Hooks),
		subs:  map[int]func(Event){},
	}

	conv, err := backend.Open(ctx, Config{Options: opts, ToolServer: toolServer}, s)
	if err != nil {
		return nil, err
	}

	s.conv = conv
	return s, nil
}

// Prompt sends a message and blocks until the turn completes.
func (s *Session) Prompt(ctx context.Context, text string) (Turn, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Turn{}, ErrSessionClosed
	}
	if s.inFlight {
		s.mu.Unlock()
		return Turn{}, ErrPromptInFlight
	}
	s.inFlight = true
	s.text.Reset()
	s.mu.Unlock()

	started := time.Now()
	turn, err := s.conv.Prompt(ctx, text)

	s.mu.Lock()
	s.inFlight = false
	// Assembling the answer from the events seen keeps every backend from
	// having to do it, and keeps them honest about what they emitted.
	turn.Text = s.text.String()
	s.mu.Unlock()

	turn.Duration = time.Since(started)
	if err != nil {
		return Turn{}, err
	}

	s.Emit(TurnEvent{Turn: turn})
	return turn, nil
}

// Subscribe registers an event listener and returns a function that removes it.
func (s *Session) Subscribe(fn func(Event)) (cancel func()) {
	s.mu.Lock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = fn
	s.mu.Unlock()

	return func() {
		s.mu.Lock()
		delete(s.subs, id)
		s.mu.Unlock()
	}
}

// Interrupt stops the current turn.
func (s *Session) Interrupt() error { return s.conv.Interrupt() }

// Close ends the session. It is safe to call twice.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	return s.conv.Close()
}

// Backend returns the conversation, for backend-specific extras that do not
// belong on the port. Type-assert it to the backend's own interface.
func (s *Session) Backend() Conversation { return s.conv }

// ---- Runtime ---------------------------------------------------------------

func (s *Session) Tools() []Tool { return s.tools.list() }

// Owns reports whether a name is one of the host's tools.
func (s *Session) Owns(name string) bool { return s.tools.has(name) }

func (s *Session) CallTool(ctx context.Context, name string, args json.RawMessage) ToolResult {
	return s.tools.call(ctx, name, args)
}

func (s *Session) Approve(ctx context.Context, req ToolRequest) Decision {
	if s.opts.ApproveTool == nil {
		return Allow()
	}
	return s.opts.ApproveTool(ctx, req)
}

func (s *Session) Hooks() map[HookEvent][]HookMatcher { return s.opts.Hooks }

func (s *Session) RunHook(ctx context.Context, callbackID string, in HookInput) (HookOutput, error) {
	fn, found := s.hooks[callbackID]
	if !found {
		return HookOutput{}, errors.New("unknown hook callback: " + callbackID)
	}
	return fn(ctx, in)
}

// Emit publishes an event, and keeps the running answer up to date so a Turn
// can carry it.
func (s *Session) Emit(ev Event) {
	s.mu.Lock()
	if text, ok := ev.(TextEvent); ok && s.inFlight {
		s.text.WriteString(text.Text)
	}
	subs := make([]func(Event), 0, len(s.subs))
	for _, fn := range s.subs {
		subs = append(subs, fn)
	}
	s.mu.Unlock()

	for _, fn := range subs {
		fn(ev)
	}
}
