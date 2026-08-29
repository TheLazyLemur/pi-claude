package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/TheLazyLemur/pi-claude/internal/proto"
)

// ErrSessionClosed is returned once a session has been closed.
var ErrSessionClosed = errors.New("pi: session closed")

// Session is a running conversation with the Claude Code CLI. It keeps one
// subprocess alive across many prompts, so context and cost survive between
// turns.
type Session struct {
	transport proto.Transport
	opts      Options
	tools     *toolset
	hooks     map[string]HookFunc
	hookInit  map[string]any

	ctx    context.Context
	cancel context.CancelFunc

	mu           sync.RWMutex
	closed       bool
	initDone     bool
	sessionID    string
	model        string
	offeredTools []string
	subs         map[int]func(Event)
	nextSub      int
	pending      *pendingTurn
	awaiting     map[string]chan controlReply
}

// pendingTurn accumulates a turn until the CLI reports its result.
type pendingTurn struct {
	started time.Time
	text    strings.Builder
	done    chan Turn
}

// New starts a Claude Code session.
func New(ctx context.Context, opts Options) (*Session, error) {
	transport, err := proto.Spawn(ctx, proto.Config{
		Executable: opts.Executable,
		Args:       buildArgs(opts),
		CWD:        opts.CWD,
		Env:        opts.Env,
		Stderr:     opts.Stderr,
	})
	if err != nil {
		return nil, err
	}
	return newSession(ctx, transport, opts), nil
}

// Run is the one-shot form: start a session, send one prompt, return its turn.
func Run(ctx context.Context, prompt string, opts Options) (Turn, error) {
	sess, err := New(ctx, opts)
	if err != nil {
		return Turn{}, err
	}
	defer sess.Close()
	return sess.Prompt(ctx, prompt)
}

func newSession(ctx context.Context, transport proto.Transport, opts Options) *Session {
	ctx, cancel := context.WithCancel(ctx)

	hookInit, hookCallbacks := buildHooks(opts.Hooks)

	s := &Session{
		transport: transport,
		opts:      opts,
		tools:     newToolset(toolServerName, opts.CustomTools),
		hooks:     hookCallbacks,
		hookInit:  hookInit,
		ctx:       ctx,
		cancel:    cancel,
		subs:      make(map[int]func(Event)),
		awaiting:  make(map[string]chan controlReply),
	}

	go s.readLoop()
	return s
}

// buildArgs turns Options into CLI flags.
func buildArgs(opts Options) []string {
	var args []string

	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(opts.MaxTurns))
	}
	if opts.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(opts.MaxBudgetUSD, 'f', 2, 64))
	}
	if opts.SystemPrompt != "" {
		args = append(args, "--system-prompt", opts.SystemPrompt)
	}
	if opts.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.AppendSystemPrompt)
	}
	// Sent whenever explicitly set, default included: the machine config may be
	// set to auto-approve, and only an explicit flag brings ApproveTool back.
	if opts.PermissionMode != "" {
		args = append(args, "--permission-mode", string(opts.PermissionMode))
	}
	if opts.Resume != "" {
		args = append(args, "--resume", opts.Resume)
	}
	if opts.Continue {
		args = append(args, "--continue")
	}
	if opts.ResumeSessionAt != "" {
		args = append(args, "--resume-session-at", opts.ResumeSessionAt)
	}
	if opts.ForkSession {
		args = append(args, "--fork-session")
	}
	if opts.NoSessionPersistence {
		args = append(args, "--no-session-persistence")
	}
	if opts.IncludePartialMessages {
		args = append(args, "--include-partial-messages")
	}
	if opts.FallbackModel != "" {
		args = append(args, "--fallback-model", opts.FallbackModel)
	}
	if len(opts.AdditionalDirectories) > 0 {
		args = append(args, "--add-dir")
		args = append(args, opts.AdditionalDirectories...)
	}
	if len(opts.Betas) > 0 {
		args = append(args, "--betas")
		args = append(args, opts.Betas...)
	}
	if len(opts.SettingSources) > 0 {
		args = append(args, "--setting-sources", strings.Join(opts.SettingSources, ","))
	}
	if len(opts.MCPConfig) > 0 {
		args = append(args, "--mcp-config")
		args = append(args, opts.MCPConfig...)
	}
	if len(opts.OutputSchema) > 0 {
		if encoded, err := json.Marshal(opts.OutputSchema); err == nil {
			args = append(args, "--json-schema", string(encoded))
		}
	}
	if len(opts.Agents) > 0 {
		if encoded, err := json.Marshal(opts.Agents); err == nil {
			args = append(args, "--agents", string(encoded))
		}
	}

	// NoTools wins over an allowlist: an allowlist is meaningless once the
	// built-in set is dropped.
	strictMCP := opts.StrictMCPConfig
	switch {
	case opts.NoTools == NoToolsAll:
		args = append(args, "--tools", "")
		strictMCP = true
	case opts.NoTools == NoToolsBuiltin:
		args = append(args, "--tools", "")
	case len(opts.Tools) > 0:
		args = append(args, "--tools", strings.Join(opts.Tools, ","))
	}

	if strictMCP {
		args = append(args, "--strict-mcp-config")
	}

	return args
}

// Prompt sends a message and blocks until the turn completes.
func (s *Session) Prompt(ctx context.Context, text string) (Turn, error) {
	done := make(chan Turn, 1)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Turn{}, ErrSessionClosed
	}
	if s.pending != nil {
		s.mu.Unlock()
		return Turn{}, errors.New("pi: a prompt is already in flight")
	}
	needsInit := !s.initDone
	s.initDone = true
	s.pending = &pendingTurn{started: time.Now(), done: done}
	s.mu.Unlock()

	if needsInit {
		if err := s.write(map[string]any{
			"type":       "control_request",
			"request_id": requestID(),
			"request":    s.initRequest(),
		}); err != nil {
			s.clearPending()
			return Turn{}, err
		}
	}

	if err := s.write(map[string]any{
		"type":               "user",
		"message":            map[string]any{"role": "user", "content": text},
		"parent_tool_use_id": nil,
		"session_id":         s.ID(),
	}); err != nil {
		s.clearPending()
		return Turn{}, err
	}

	select {
	case turn := <-done:
		return turn, nil
	case <-ctx.Done():
		s.clearPending()
		return Turn{}, ctx.Err()
	case <-s.ctx.Done():
		s.clearPending()
		return Turn{}, ErrSessionClosed
	}
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

// controlReply is the CLI's answer to a control request we sent.
type controlReply struct {
	body map[string]any
	err  error
}

// request sends a control request and waits for the CLI's reply.
func (s *Session) request(ctx context.Context, subtype string, fields map[string]any) (map[string]any, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrSessionClosed
	}
	id := requestID()
	reply := make(chan controlReply, 1)
	s.awaiting[id] = reply
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.awaiting, id)
		s.mu.Unlock()
	}()

	fields["subtype"] = subtype
	if err := s.write(map[string]any{
		"type":       "control_request",
		"request_id": id,
		"request":    fields,
	}); err != nil {
		return nil, err
	}

	select {
	case r := <-reply:
		return r.body, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.ctx.Done():
		return nil, ErrSessionClosed
	}
}

// MCPStatus reports the connection state of external MCP servers.
func (s *Session) MCPStatus(ctx context.Context) (map[string]any, error) {
	return s.request(ctx, "mcp_status", map[string]any{})
}

// SetMCPServers replaces the external MCP servers for this session.
func (s *Session) SetMCPServers(ctx context.Context, servers map[string]any) (map[string]any, error) {
	return s.request(ctx, "mcp_set_servers", map[string]any{"servers": servers})
}

// RewindFiles undoes file edits back to a message id from the CLI's transcript.
// Pass dryRun to learn what would change without changing it.
//
// This needs file checkpointing enabled. Claude Code 2.1.251 answers
// {"canRewind": false, "error": "File rewinding is not enabled."} even with
// Options.EnableFileCheckpointing set, so treat the reply as authoritative and
// check canRewind before relying on it.
func (s *Session) RewindFiles(ctx context.Context, userMessageID string, dryRun bool) (map[string]any, error) {
	return s.request(ctx, "rewind_files", map[string]any{
		"user_message_id": userMessageID,
		"dry_run":         dryRun,
	})
}

// SetMaxThinkingTokens caps extended thinking. Zero or less clears the cap.
func (s *Session) SetMaxThinkingTokens(tokens int) error {
	var value any
	if tokens > 0 {
		value = tokens
	}
	return s.control("set_max_thinking_tokens", map[string]any{"max_thinking_tokens": value})
}

// Interrupt stops the current turn.
func (s *Session) Interrupt() error {
	return s.control("interrupt", map[string]any{})
}

// SetModel switches model mid-session.
func (s *Session) SetModel(model string) error {
	return s.control("set_model", map[string]any{"model": model})
}

// SetPermissionMode changes permission behaviour mid-session.
func (s *Session) SetPermissionMode(mode PermissionMode) error {
	return s.control("set_permission_mode", map[string]any{"mode": string(mode)})
}

// ID returns the CLI's session id, empty until the first handshake.
func (s *Session) ID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionID
}

// OfferedTools returns the tools the CLI reported at startup. Assert on this
// when you meant to lock the tool surface down.
func (s *Session) OfferedTools() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.offeredTools...)
}

// Close ends the session and stops the subprocess. It is safe to call twice.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	s.cancel()
	return s.transport.Close()
}

// Wait blocks until the subprocess exits.
func (s *Session) Wait() error { return s.transport.Wait() }

func (s *Session) initRequest() map[string]any {
	req := map[string]any{"subtype": "initialize"}
	if !s.tools.empty() {
		req["sdkMcpServers"] = []string{s.tools.server}
	}
	if len(s.hookInit) > 0 {
		req["hooks"] = s.hookInit
	}
	if s.opts.EnableFileCheckpointing {
		req["enableFileCheckpointing"] = true
	}
	return req
}

func (s *Session) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("pi: marshal: %w", err)
	}
	return s.transport.Write(data)
}

func (s *Session) control(subtype string, fields map[string]any) error {
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return ErrSessionClosed
	}

	fields["subtype"] = subtype
	return s.write(map[string]any{
		"type":       "control_request",
		"request_id": requestID(),
		"request":    fields,
	})
}

func (s *Session) emit(ev Event) {
	s.mu.RLock()
	subs := make([]func(Event), 0, len(s.subs))
	for _, fn := range s.subs {
		subs = append(subs, fn)
	}
	s.mu.RUnlock()

	for _, fn := range subs {
		fn(ev)
	}
}

func (s *Session) clearPending() {
	s.mu.Lock()
	s.pending = nil
	s.mu.Unlock()
}

func requestID() string {
	return fmt.Sprintf("pi-%d", time.Now().UnixNano())
}
