// A coding agent whose entire tool surface is this program's own Go functions.
//
// No built-in tools, no MCP servers from the machine, no shell. Five tools over
// the directory you are standing in, with the sandbox enforced in Go rather
// than asked for in the prompt.
//
//	harness                      # interactive, in the current directory
//	harness "fix the flaky test" # one shot
//	echo "..." | harness         # piped, one prompt per line
//	harness -C ./other "task"    # somewhere else
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
	"strings"
	"syscall"

	pi "github.com/TheLazyLemur/pi-claude"
)

const systemPrompt = `You are a coding agent working inside a single directory.

The only way to see or change anything is the tools you have been given. There
is no shell, no network, and nothing outside the directory is reachable: paths
that escape it are refused, not silently redirected. You cannot run tests or
builds, so never claim you have.

Read before you write. Prefer edit_file over write_file so you do not discard
work you have not read. If a tool refuses, read the message and adjust rather
than retrying the same call. Match the conventions already in the files you
touch. Be terse.`

func main() {
	dir := flag.String("C", ".", "directory to work in")
	model := flag.String("model", "", "model to use (default: the CLI's)")
	verbose := flag.Bool("v", false, "print full tool arguments")
	flag.Parse()

	w, err := newWorkspace(*dir)
	if err != nil {
		log.Fatal(err)
	}

	sess, err := pi.New(context.Background(), pi.Options{
		CWD:   w.root,
		Model: *model,

		// The agent's whole world is the tools below.
		NoTools:     pi.NoToolsAll,
		CustomTools: toolsFor(w),

		SystemPrompt: systemPrompt,
		MaxTurns:     40,

		// A second line of defence. The sandbox already lives in the tools;
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
		case pi.ToolCallEvent:
			if *verbose {
				fmt.Printf("  · %s %v\n", e.Name, e.Input)
				return
			}
			fmt.Printf("  · %s %s\n", e.Name, summarise(e.Input))
		case pi.ToolResultEvent:
			if e.IsError {
				fmt.Printf("    ! %s\n", firstLine(e.Text))
			}
		case pi.DeniedEvent:
			fmt.Printf("  · denied %s: %s\n", e.Name, e.Reason)
		case pi.ErrorEvent:
			fmt.Fprintln(os.Stderr, "  ! ", e.Err)
		}
	})

	// One Ctrl-C stops the turn in progress; a second leaves.
	ctx, stop := signalContext(sess)
	defer stop()

	if task := strings.Join(flag.Args(), " "); task != "" {
		ask(ctx, sess, task)
	} else {
		repl(ctx, sess, w)
	}

	if changes := w.Changes(); len(changes) > 0 {
		fmt.Printf("\n%d file%s changed\n", len(changes), plural(len(changes)))
		for _, c := range changes {
			fmt.Printf("  %-6s %s (%+d bytes)\n", c.Kind, c.Path, c.Bytes)
		}
	}
}

// repl reads prompts from stdin, one per line, on the same session, so each
// turn still has every earlier turn in context.
func repl(ctx context.Context, sess *pi.Session, w *workspace) {
	interactive := isTerminal(os.Stdin)
	if interactive {
		files, _ := w.walk()
		fmt.Printf("%s\n%d files, 5 tools, no built-ins. Ctrl-D to exit.\n", w.root, len(files))
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64*1024), 4*1024*1024)

	for {
		if interactive {
			fmt.Print("\n> ")
		}
		if !in.Scan() || ctx.Err() != nil {
			break
		}

		task := strings.TrimSpace(in.Text())
		switch task {
		case "":
			continue
		case "exit", "quit":
			return
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
	fmt.Printf("\n[%d turns · $%.4f · %d in / %d out]\n",
		turn.Turns, turn.CostUSD, turn.Usage.InputTokens, turn.Usage.OutputTokens)
}

// signalContext cancels on the first Ctrl-C, so the current turn stops and the
// prompt comes back. A second is left to the runtime, which exits.
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

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
