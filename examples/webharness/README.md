# Console

A browser front end for a Claude Code session where every tool is this
program's own. Go, htmx and server-sent events. No JavaScript framework, no
build step, no node_modules.

```
go run ./examples/webharness                       # the current directory
go run ./examples/webharness ../api ../web         # several projects
go run ./examples/webharness -addr :8080 ../api    # flags come first
```

Then open http://127.0.0.1:7777.

## What it is

A supervision panel, not a chat window. The agent has hands on your files, so
the job of the page is to make what it is doing legible and stoppable. Tool
activity is the content; the prose is secondary.

Six tools, all defined here in Go:

- `list_files`, `read_file`, `search`
- `write_file`, `edit_file`, both of which draw their own diff
- `todo_write`, which drives the task list in the sidebar

The only built-in kept is `Skill`, because that is how Claude Code loads a
project's own instructions and reimplementing it would be silly.
`StrictMCPConfig` keeps the machine's own MCP servers out.

## Projects, sessions and worktrees

Every directory you name on the command line is a project. Each session is its
own `claude` process with its own transcript, and the sidebar lists them across
every project.

A new session can open **in a git worktree**. Tick the box, give it a first
message, and the console branches off HEAD into `.worktrees/<name>` on
`console/<name>`, starts the session there, and sends the message straight into
it. Two sessions on one project then have a checkout each and cannot tread on
one another, which is the thing that bites first when more than one agent is
working at a time.

`.worktrees/` is added to `.git/info/exclude`, so it stays out of `git status`
without touching a tracked `.gitignore`.

Cleaning up is ordinary git:

```
git worktree remove .worktrees/<name>
git branch -D console/<name>
```

## What it borrows

- Plan and Act, from Cline. In Plan the writing tools are refused in Go, not
  discouraged in a prompt.
- A diff on every write, from Aider.
- A live task list, from Claude Code.
- A running cost meter, from OpenCode.

## The rail

The left column draws each tool call as it happens. The live one pulses, writes
settle green, refusals settle red. Click a tick to jump to that card.

It is the one bold thing on the page. Everything else stays quiet.

## How the streaming works

One SSE connection carries named events, and htmx swaps them into place:

| event | target | swap |
|---|---|---|
| `msg` | transcript | `beforeend scroll:#scroll:bottom transition:true` |
| `rail` | activity rail | `beforeend scroll:bottom` |
| `meter` | cost readout | `innerHTML` |
| `todos` | task list | `innerHTML` |
| `modes` | Plan/Act toggle | `innerHTML` |

Updates to something already on the page, a diff filling in a card or a tick
settling, ride the `msg` channel as `hx-swap-oob` fragments. htmx lifts those
out and puts them where their ids say, so there is no second channel and no
hidden sink element to keep in sync.

The scroll modifier means no scroll handler. The page's only JavaScript is a
keyboard shortcut and a click-to-jump on the rail.

## Design

Light, not dark. Every agent UI is dark, and diffs read better on paper. Space
Grotesk and IBM Plex Mono, violet as the agent's colour, muted green and red so
a diff informs rather than shouts.

## Streaming and replay

A session records what happened as state, not as the HTML that announced it.
Live updates and a reloaded page both render from the same `renderEntry`, so a
tab opened an hour later shows the same transcript as one that watched it
happen. Fragments are deltas; only the state is the truth.

That also means nothing is lost when no browser is connected. The agent never
waits for a tab.

## Caveats

Sessions live in memory. Restarting the console loses transcripts, though the
worktrees and branches it made are still there in git.

`-debug` adds `POST /debug/replay`, which renders a fake turn so the UI can be
worked on without paying a model.
