package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	pi "github.com/TheLazyLemur/pi-claude"
)

type todo struct {
	Task   string `json:"task" desc:"Short description of the piece of work"`
	Status string `json:"status" desc:"pending, doing or done"`
}

const systemPrompt = `You are a coding agent working in one directory, driven from a web console.

Every tool you have is provided by that console. Paths that escape the directory
are refused, not redirected.

{{SHELL}}
Work in small steps and say what you are doing as you go, briefly. Read before
you write, and prefer edit_file over write_file so you do not throw away work
you have not seen. Match the conventions already in the files you touch.

Use todo_write when a task has more than two steps. Keep it current: mark one
item doing, mark it done before starting the next. The person watching uses it
to know where you are.

In plan mode the writing tools are refused. Read, investigate, and
say what you would change and what you would run. Do not fight the refusal.

Be terse. No preamble, no summaries of what you just did unless asked.`

func (a *Session) tools() []pi.Tool {
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

	tools := []pi.Tool{
		pi.DefineTool("list_files", "List the files in the workspace.",
			func(_ context.Context, p listParams) (pi.ToolResult, error) {
				paths, truncated, err := w.walk()
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
				listing := strings.Join(paths, "\n")
				if truncated {
					listing += fmt.Sprintf("\n... stopped at %d files; narrow it with dir or search", maxFiles)
				}
				return pi.Text("%s", listing), nil
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
				paths, _, err := w.walk()
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
				a.setTodos(p.Items)
				a.emit("todos", todosHTML(p.Items))
				a.settle("todo_write", "read")
				return pi.Text("task list updated (%d items)", len(p.Items)), nil
			}),
	}

	if a.shell {
		tools = append(tools, a.shellTool())
	}
	return tools
}

// settle stops a tick pulsing once its tool has finished.
func (a *Session) settle(name, kind string) {
	a.settleCard(a.claimCard(name), kind)
}

func (a *Session) settleCard(card, kind string) {
	var name, arg string
	e := a.updateCard(card, func(e *entry) {
		e.Settled = kind
		name, arg = e.Name, e.Arg
	})
	if e == nil {
		return
	}
	a.emit("msg", tickHTML(card, name, arg, kind, false, true))
}

func (a *Session) showDiff(card, kind, path, before, after string) {
	hunks := unifiedDiff(before, after)
	added, removed := countChanges(hunks)
	a.ws.record(change{Path: path, Kind: kind, Added: added, Del: removed})
	if card == "" {
		return
	}
	a.updateCard(card, func(e *entry) { e.Hunks = hunks })
	a.emit("msg", diffHTML(card, hunks))
	a.settleCard(card, "write")
}

func (a *Session) fail(card, msg string) {
	if card == "" {
		return
	}
	a.updateCard(card, func(e *entry) { e.Note = msg })
	a.emit("msg", fmt.Sprintf(
		`<div id="%s" hx-swap-oob="beforeend"><div class="note">%s</div></div>`, card, esc(msg)))
	a.settleCard(card, "deny")
}

func (a *Session) refuse(card, name string) {
	if card == "" {
		return
	}
	note := "Refused. Plan mode is on, so " + name + " cannot run."
	a.updateCard(card, func(e *entry) { e.Note = note })
	a.emit("msg", fmt.Sprintf(
		`<div id="%s" hx-swap-oob="beforeend"><div class="note">%s</div></div>`, card, esc(note)))
	a.settleCard(card, "deny")
}

// shellParams is what the model sends to run something.
type shellParams struct {
	Command string `json:"command" desc:"The command to run, as you would type it in a shell"`
	Timeout int    `json:"timeout_seconds,omitempty" desc:"How long to allow. Defaults to 120, capped at 600."`
}

const (
	shellDefaultTimeout = 120 * time.Second
	shellMaxTimeout     = 600 * time.Second
	shellMaxOutput      = 96 * 1024
	shellTailLines      = 200
)

// shellTool runs a command in the workspace and streams its output into the
// page as it arrives.
//
// This is the one tool that is not a narrow verb. It hands the model arbitrary
// execution, which is the point: an agent that cannot run your tests cannot
// tell whether its change worked.
//
// It starts in the workspace, has a deadline, and is refused in plan mode. None
// of that is a security boundary. The very first real run of this tool began
// with "cd ~ && go test ./...", which is the whole story: a shell can leave the
// directory it was started in, and no amount of checking the command text will
// reliably stop it. Enable this for work you are watching, and use -shell=false
// when you want the workspace to mean something.
func (a *Session) shellTool() pi.Tool {
	return pi.DefineTool("shell",
		"Run a shell command in the project directory. Use it to build, test, lint, or inspect. "+
			"Output is streamed back with the exit code.",
		func(ctx context.Context, p shellParams) (pi.ToolResult, error) {
			card := a.claimCard("shell")
			command := strings.TrimSpace(p.Command)
			if command == "" {
				a.fail(card, "no command given")
				return pi.Errorf("give a command to run"), nil
			}
			if a.planning() {
				a.refuse(card, "shell")
				return pi.Errorf("plan mode: say what you would run and why, instead of running it"), nil
			}

			timeout := shellDefaultTimeout
			if p.Timeout > 0 {
				timeout = min(time.Duration(p.Timeout)*time.Second, shellMaxTimeout)
			}

			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			a.running(cancel)
			defer a.running(nil)

			cmd := exec.CommandContext(ctx, shellArgv(command)[0], shellArgv(command)[1:]...)
			cmd.Dir = a.ws.root
			// Its own group, so a timeout takes the children with it rather
			// than leaving a test runner behind.
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

			pipe, err := cmd.StdoutPipe()
			if err != nil {
				a.fail(card, err.Error())
				return pi.Errorf("could not start: %v", err), nil
			}
			cmd.Stderr = cmd.Stdout

			if err := cmd.Start(); err != nil {
				a.fail(card, err.Error())
				return pi.Errorf("could not start: %v", err), nil
			}

			var seen strings.Builder
			truncated := false
			scanner := bufio.NewScanner(pipe)
			scanner.Buffer(make([]byte, 64*1024), 1024*1024)

			for scanner.Scan() {
				line := scanner.Text()
				if seen.Len() >= shellMaxOutput {
					truncated = true
					continue
				}
				seen.WriteString(line)
				seen.WriteByte('\n')
				if card != "" {
					a.emit("msg", termLineHTML(card, line))
				}
			}

			waitErr := cmd.Wait()
			code := cmd.ProcessState.ExitCode()

			out := seen.String()
			if truncated {
				out += fmt.Sprintf("\n... output stopped at %d KB\n", shellMaxOutput/1024)
			}
			if ctx.Err() != nil {
				out += fmt.Sprintf("\n... killed after %s\n", timeout)
				code = -1
			}

			a.finishShell(card, out, code)

			summary := fmt.Sprintf("exit %d\n%s", code, tail(out, shellTailLines))
			if code != 0 || waitErr != nil {
				return pi.ToolResult{Text: summary, IsError: true}, nil
			}
			return pi.Text("%s", summary), nil
		})
}

// finishShell records the run and marks the card done.
func (a *Session) finishShell(card, out string, code int) {
	if card == "" {
		return
	}
	a.updateCard(card, func(e *entry) {
		e.Output, e.Exit, e.HasExit = out, code, true
	})
	a.emit("msg", termExitHTML(card, code))

	kind := "write"
	if code != 0 {
		kind = "deny"
	}
	a.settleCard(card, kind)
}

// tail keeps the end of a long output, which is where the failure usually is.
func tail(s string, lines int) string {
	all := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(all) <= lines {
		return strings.Join(all, "\n")
	}
	return fmt.Sprintf("... %d earlier lines\n%s", len(all)-lines, strings.Join(all[len(all)-lines:], "\n"))
}

// shellArgv prefers bash with pipefail, so a failing command in a pipeline is
// still a failure. Without it "go test | head" exits 0 whatever go test did,
// which quietly turns a red build green.
func shellArgv(command string) []string {
	if bash, err := exec.LookPath("bash"); err == nil {
		return []string{bash, "-o", "pipefail", "-c", command}
	}
	return []string{"sh", "-c", command}
}

// promptFor tailors the system prompt to the tools this session actually has.
// Telling a model about a shell it does not have is worse than saying nothing.
func promptFor(shell bool) string {
	if shell {
		return strings.Replace(systemPrompt, "{{SHELL}}",
			`You can run commands with the shell tool: builds, tests, linters, git, anything
you would type yourself. Use it. A change you have not run is a guess, so when
you change code, run the thing that proves it. In plan mode the shell is
refused too: say what you would run.
`, 1)
	}
	return strings.Replace(systemPrompt, "{{SHELL}}",
		`There is no shell and no network, so you cannot run tests or builds. Never
say or imply that you have.
`, 1)
}
