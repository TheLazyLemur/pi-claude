// A typical harness: every tool the agent has is one of ours.
//
// No built-in tools, no MCP servers from this machine. The agent can list,
// read, search, write and edit — all through Go functions closing over a
// workspace that enforces its own sandbox and keeps its own audit trail.
//
//	go run ./examples/harness                          # interactive, seeded workspace
//	go run ./examples/harness "fix the bug"            # one shot
//	echo "fix the bug" | go run ./examples/harness     # piped
//	go run ./examples/harness -dir ./somewhere "task"
//
// With no task the session stays open and reads prompts from stdin, one per
// line, so later turns keep everything the earlier ones learned.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	pi "github.com/TheLazyLemur/pi-claude"
)

const systemPrompt = `You are working inside a sandboxed workspace.

The only way to see or change anything is the tools you have been given. There
is no shell, no network, and nothing outside the workspace is reachable: paths
that escape it are refused, not silently redirected.

Read before you write. Prefer edit_file over write_file so you do not discard
work you have not read. If a tool refuses, read the message and adjust rather
than retrying the same call.`

func main() {
	dir := flag.String("dir", "", "workspace directory (default: a seeded scratch workspace)")
	verbose := flag.Bool("v", false, "print every tool call")
	flag.Parse()

	task := strings.Join(flag.Args(), " ")

	root := *dir
	var suggestion string
	if root == "" {
		seeded, seededTask, err := seedWorkspace()
		if err != nil {
			log.Fatal(err)
		}
		root, suggestion = seeded, seededTask
		fmt.Printf("workspace: %s\n", root)
	}

	w, err := newWorkspace(root)
	if err != nil {
		log.Fatal(err)
	}

	sess, err := pi.New(context.Background(), pi.Options{
		CWD: w.root,

		// The agent's whole world is the tools below.
		NoTools:     pi.NoToolsAll,
		CustomTools: toolsFor(w),

		SystemPrompt: systemPrompt,
		MaxTurns:     30,

		// A second line of defence. The sandbox already lives in the tools, but
		// this makes "nothing else runs" true by construction rather than by
		// having got the flags right.
		PermissionMode: pi.PermissionModeDefault,
		ApproveTool: func(_ context.Context, req pi.ToolRequest) pi.Decision {
			if !req.Mine {
				return pi.Deny("this harness only exposes its own tools")
			}
			return pi.Allow()
		},

		Stderr: func(line string) { fmt.Fprintln(os.Stderr, "claude:", line) },
	})
	if err != nil {
		log.Fatal(err)
	}
	defer sess.Close()

	sess.Subscribe(func(ev pi.Event) {
		switch e := ev.(type) {
		case pi.ReadyEvent:
			fmt.Printf("· %d tools, model %s\n", len(e.Tools), e.Model)
		case pi.ToolCallEvent:
			if *verbose {
				fmt.Printf("· %s %v\n", e.Name, e.Input)
			} else {
				fmt.Printf("· %s %s\n", e.Name, summarise(e.Input))
			}
		case pi.ToolResultEvent:
			if e.IsError {
				fmt.Printf("  ! %s\n", firstLine(e.Text))
			}
		case pi.DeniedEvent:
			fmt.Printf("· denied %s: %s\n", e.Name, e.Reason)
		}
	})

	// One Ctrl-C stops the turn in progress; a second one leaves.
	ctx, stop := signalContext(sess)
	defer stop()

	if task != "" {
		ask(ctx, sess, task)
	} else {
		repl(ctx, sess, suggestion)
	}

	changes := w.Changes()
	fmt.Printf("\n--- %d file changes ---\n", len(changes))
	for _, c := range changes {
		fmt.Printf("  %-6s %s (%+d bytes)\n", c.Kind, c.Path, c.Bytes)
	}
}

// repl reads prompts from stdin, one per line, on the same session, so each
// turn still has every earlier turn in context.
func repl(ctx context.Context, sess *pi.Session, suggestion string) {
	interactive := isTerminal(os.Stdin)
	if interactive {
		fmt.Println("\nType a task, or Ctrl-D to finish.")
		if suggestion != "" {
			fmt.Printf("Try: %s\n", suggestion)
		}
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64*1024), 4*1024*1024)

	for {
		if interactive {
			fmt.Print("\n> ")
		}
		if !in.Scan() {
			break
		}

		task := strings.TrimSpace(in.Text())
		if task == "" {
			continue
		}
		if task == "exit" || task == "quit" {
			break
		}
		if ctx.Err() != nil {
			break
		}

		if !interactive {
			fmt.Printf("\n> %s\n", task)
		}
		ask(ctx, sess, task)
	}

	if err := in.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "read:", err)
	}
}

// ask runs one turn and prints its answer.
func ask(ctx context.Context, sess *pi.Session, task string) {
	turn, err := sess.Prompt(ctx, task)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Println("\n(interrupted)")
			return
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return
	}

	fmt.Printf("\n%s\n", strings.TrimSpace(turn.Text))
	fmt.Printf("\n[%d turns, $%.4f, %d in / %d out]\n",
		turn.Turns, turn.CostUSD, turn.Usage.InputTokens, turn.Usage.OutputTokens)
}

// signalContext cancels on the first Ctrl-C, so the current turn stops and the
// prompt comes back. A second one is left to the runtime, which exits.
func signalContext(sess *pi.Session) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-ch
		signal.Stop(ch)
		sess.Interrupt()
		cancel()
	}()

	return ctx, func() {
		signal.Stop(ch)
		cancel()
	}
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// summarise keeps the trace readable when a tool is handed a whole file.
func summarise(input map[string]any) string {
	var parts []string
	for _, key := range []string{"path", "dir", "pattern", "glob"} {
		if v, ok := input[key].(string); ok && v != "" {
			parts = append(parts, v)
		}
	}
	if v, ok := input["content"].(string); ok {
		parts = append(parts, fmt.Sprintf("(%d bytes)", len(v)))
	}
	return strings.Join(parts, " ")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// seedWorkspace builds a small project with a deliberate bug in it, so the
// example does something real out of the box.
func seedWorkspace() (root, task string, err error) {
	root, err = os.MkdirTemp("", "pi-harness-*")
	if err != nil {
		return "", "", err
	}

	files := map[string]string{
		"go.mod": "module cart\n\ngo 1.25\n",
		"cart.go": `package cart

// Item is a line in a shopping cart.
type Item struct {
	Name     string
	Price    float64
	Quantity int
}

// Total returns the cost of every item in the cart.
func Total(items []Item) float64 {
	var total float64
	for _, item := range items {
		total += item.Price
	}
	return total
}
`,
		"README.md": "# cart\n\nA tiny shopping cart.\n",
	}

	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			return "", "", err
		}
	}

	return root, "Total ignores Quantity, so a cart with 3 of an item is priced as 1. " +
		"Find the bug, fix it, and add a table-driven test for Total in cart_test.go.", nil
}
