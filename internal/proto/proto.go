// Package proto speaks the Claude Code stdio protocol: it spawns the CLI and
// turns its stdout into a stream of JSON frames.
package proto

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
)

// ErrClosed is returned by Write after the transport is closed.
var ErrClosed = errors.New("transport closed")

// Frame is one line of JSON from the CLI, or a read error.
type Frame struct {
	Type      string
	Subtype   string
	SessionID string
	Raw       []byte
	Err       error
}

// Transport is the seam the session talks through, so it can be faked in tests.
type Transport interface {
	Write(data []byte) error
	Frames() <-chan Frame
	Close() error
	Wait() error
}

// Config configures the claude subprocess.
type Config struct {
	Executable string
	Args       []string
	CWD        string
	Env        []string
	Stderr     func(string)
}

// BaseArgs are the flags the stdio protocol requires.
func BaseArgs() []string {
	return []string{
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--permission-prompt-tool", "stdio",
	}
}

type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	frames chan Frame

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	closed bool

	exitOnce sync.Once
	exitErr  error
}

// Spawn starts the claude CLI and begins reading frames from it.
func Spawn(ctx context.Context, cfg Config) (Transport, error) {
	ctx, cancel := context.WithCancel(ctx)

	executable := cfg.Executable
	if executable == "" {
		executable = "claude"
	}

	cmd := exec.CommandContext(ctx, executable, append(BaseArgs(), cfg.Args...)...)
	if cfg.CWD != "" {
		cmd.Dir = cfg.CWD
	}
	cmd.Env = os.Environ()
	if len(cfg.Env) > 0 {
		cmd.Env = append(cmd.Env, cfg.Env...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start %s: %w", executable, err)
	}

	p := &process{
		cmd:    cmd,
		stdin:  stdin,
		frames: make(chan Frame, 128),
		ctx:    ctx,
		cancel: cancel,
	}

	p.wg.Add(2)
	go p.readLoop(stdout)
	go p.stderrLoop(stderr, cfg.Stderr)

	return p, nil
}

func (p *process) readLoop(stdout io.Reader) {
	defer p.wg.Done()
	defer close(p.frames)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if os.Getenv("PI_CLAUDE_DEBUG") != "" {
			log.Printf("[recv] %s", line)
		}

		var head struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			SessionID string `json:"session_id"`
		}

		raw := make([]byte, len(line))
		copy(raw, line)

		frame := Frame{Raw: raw}
		if err := json.Unmarshal(raw, &head); err != nil {
			frame.Err = fmt.Errorf("parse frame: %w", err)
		} else {
			frame.Type, frame.Subtype, frame.SessionID = head.Type, head.Subtype, head.SessionID
		}

		select {
		case p.frames <- frame:
		case <-p.ctx.Done():
			return
		}
	}

	if err := scanner.Err(); err != nil {
		select {
		case p.frames <- Frame{Err: fmt.Errorf("read: %w", err)}:
		case <-p.ctx.Done():
		}
	}
}

func (p *process) stderrLoop(stderr io.Reader, handler func(string)) {
	defer p.wg.Done()

	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		if handler != nil {
			handler(scanner.Text())
		}
	}
}

func (p *process) Write(data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return ErrClosed
	}
	if os.Getenv("PI_CLAUDE_DEBUG") != "" {
		log.Printf("[send] %s", data)
	}

	_, err := p.stdin.Write(append(data, '\n'))
	return err
}

func (p *process) Frames() <-chan Frame { return p.frames }

func (p *process) Close() error {
	p.mu.Lock()
	already := p.closed
	p.closed = true
	p.mu.Unlock()

	if already {
		return nil
	}

	p.stdin.Close()
	p.cancel()
	return nil
}

func (p *process) Wait() error {
	p.wg.Wait()
	p.exitOnce.Do(func() { p.exitErr = p.cmd.Wait() })
	return p.exitErr
}
