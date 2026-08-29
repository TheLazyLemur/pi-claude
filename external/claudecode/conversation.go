package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/TheLazyLemur/pi-claude/core"
	"github.com/TheLazyLemur/pi-claude/internal/proto"
)

// Backend starts Claude Code sessions.
type Backend struct{}

// New returns a backend that drives the claude CLI.
func New() *Backend { return &Backend{} }

// Open spawns a CLI process configured for one conversation.
func (b *Backend) Open(ctx context.Context, cfg core.Config, rt core.Runtime) (core.Conversation, error) {
	transport, err := proto.Spawn(ctx, proto.Config{
		Executable: cfg.Executable,
		Args:       buildArgs(cfg.Options),
		CWD:        cfg.CWD,
		Env:        cfg.Env,
		Stderr:     cfg.Stderr,
	})
	if err != nil {
		return nil, err
	}
	return Start(ctx, transport, cfg, rt), nil
}

// Start wires a conversation onto an already-running transport. Tests use it
// with a fake in place of the CLI.
func Start(ctx context.Context, transport proto.Transport, cfg core.Config, rt core.Runtime) *Conversation {
	ctx, cancel := context.WithCancel(ctx)

	server := cfg.ToolServer
	if server == "" {
		server = "pi"
	}

	c := &Conversation{
		transport: transport,
		cfg:       cfg,
		rt:        rt,
		tools:     newToolset(server, rt),
		ctx:       ctx,
		cancel:    cancel,
		awaiting:  map[string]chan controlReply{},
	}
	c.hookInit = buildHooks(rt.Hooks())

	go c.readLoop()
	return c
}

// Conversation is one exchange with the CLI. It owns the protocol; everything
// about the host reaches it through core.Runtime.
type Conversation struct {
	transport proto.Transport
	cfg       core.Config
	rt        core.Runtime
	tools     *toolset
	hookInit  map[string]any

	ctx    context.Context
	cancel context.CancelFunc

	mu           sync.RWMutex
	closed       bool
	initDone     bool
	sessionID    string
	model        string
	offeredTools []string
	pending      chan core.Turn
	awaiting     map[string]chan controlReply
}

// Prompt sends a message and waits for the CLI to finish the turn.
func (c *Conversation) Prompt(ctx context.Context, text string) (core.Turn, error) {
	done := make(chan core.Turn, 1)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return core.Turn{}, core.ErrSessionClosed
	}
	needsInit := !c.initDone
	c.initDone = true
	c.pending = done
	c.mu.Unlock()

	if needsInit {
		if err := c.write(map[string]any{
			"type":       "control_request",
			"request_id": requestID(),
			"request":    c.initRequest(),
		}); err != nil {
			c.clearPending()
			return core.Turn{}, err
		}
	}

	if err := c.write(map[string]any{
		"type":               "user",
		"message":            map[string]any{"role": "user", "content": text},
		"parent_tool_use_id": nil,
		"session_id":         c.ID(),
	}); err != nil {
		c.clearPending()
		return core.Turn{}, err
	}

	select {
	case turn := <-done:
		return turn, nil
	case <-ctx.Done():
		c.clearPending()
		// The CLI does not know the caller gave up, and would keep working the
		// turn, and billing for it, until it finished.
		c.Interrupt()
		return core.Turn{}, ctx.Err()
	case <-c.ctx.Done():
		c.clearPending()
		return core.Turn{}, core.ErrSessionClosed
	}
}

// Interrupt stops the turn in flight.
func (c *Conversation) Interrupt() error {
	return c.control("interrupt", map[string]any{})
}

// Close ends the conversation and reaps the subprocess.
func (c *Conversation) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	c.cancel()
	if err := c.transport.Close(); err != nil {
		return err
	}

	// Killing the subprocess without waiting leaves a zombie for the life of
	// the parent, which matters as soon as sessions are short-lived and many.
	c.transport.Wait()
	return nil
}

// Wait blocks until the subprocess exits.
func (c *Conversation) Wait() error { return c.transport.Wait() }

// ID is the CLI's session id, empty until the first handshake.
func (c *Conversation) ID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessionID
}

// OfferedTools is what the CLI reported at startup. Assert on it when you meant
// to lock the tool surface down.
func (c *Conversation) OfferedTools() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.offeredTools...)
}

func (c *Conversation) initRequest() map[string]any {
	req := map[string]any{"subtype": "initialize"}
	if !c.tools.empty() {
		req["sdkMcpServers"] = []string{c.tools.server}
	}
	if len(c.hookInit) > 0 {
		req["hooks"] = c.hookInit
	}
	if c.cfg.EnableFileCheckpointing {
		req["enableFileCheckpointing"] = true
	}
	return req
}

func (c *Conversation) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("pi: marshal: %w", err)
	}
	return c.transport.Write(data)
}

func (c *Conversation) clearPending() {
	c.mu.Lock()
	c.pending = nil
	c.mu.Unlock()
}

func requestID() string {
	return fmt.Sprintf("pi-%d", time.Now().UnixNano())
}
