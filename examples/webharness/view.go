package main

import (
	"fmt"
	"html"
	"strings"
)

const page = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Console &middot; {{TITLE}}</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Space+Grotesk:wght@400;500;600;700&family=IBM+Plex+Mono:wght@400;500;600&display=swap" rel="stylesheet">
<script src="https://unpkg.com/htmx.org@2.0.10/dist/htmx.min.js"></script>
<script src="https://unpkg.com/htmx-ext-sse@2.2.4/sse.js"></script>
<style>
:root{
  --paper:#eceff3; --card:#fff; --ink:#111827; --dim:#6b7688;
  --rule:#d8dee7; --rule-soft:#e6eaf0;
  --signal:#5b34d8; --signal-soft:#efe9fd;
  --add:#0f7b4f; --add-bg:#e8f6ef;
  --del:#b0273a; --del-bg:#fceceE;
  --warn:#b45309; --warn-bg:#fdf3e3;
  --mono:"IBM Plex Mono",ui-monospace,SFMono-Regular,Menlo,monospace;
  --sans:"Space Grotesk",system-ui,-apple-system,sans-serif;
  --u:8px;
}
*{box-sizing:border-box}
html,body{height:100%}
body{
  margin:0; background:var(--paper); color:var(--ink);
  font-family:var(--sans); font-size:15px; line-height:1.55;
  -webkit-font-smoothing:antialiased;
}
.label{
  font-family:var(--mono); font-size:10px; font-weight:500;
  letter-spacing:.09em; text-transform:uppercase; color:var(--dim);
}
button{font-family:inherit; font-size:inherit; cursor:pointer}
:focus-visible{outline:2px solid var(--signal); outline-offset:2px; border-radius:2px}

/* ---------- shell ---------- */
.shell{display:grid; grid-template-rows:auto 1fr; height:100dvh}
.top{
  display:flex; align-items:center; gap:calc(var(--u)*3);
  padding:0 calc(var(--u)*2.5); height:56px;
  background:var(--card); border-bottom:1px solid var(--rule);
}
.brand{display:flex; align-items:baseline; gap:10px; font-weight:700; letter-spacing:-.02em}
.brand b{font-size:17px}
.brand .dot{width:7px; height:7px; border-radius:50%; background:var(--signal); align-self:center}
.where{font-family:var(--mono); font-size:11.5px; color:var(--dim); overflow:hidden; text-overflow:ellipsis; white-space:nowrap; max-width:44ch}
.top .spacer{margin-left:auto}

.meter{display:flex; gap:calc(var(--u)*2.5); align-items:center}
.meter .cell{display:flex; flex-direction:column; line-height:1.2}
.meter .v{font-family:var(--mono); font-size:13px; font-weight:600; font-variant-numeric:tabular-nums}
.meter .k{font-family:var(--mono); font-size:9px; letter-spacing:.12em; text-transform:uppercase; color:var(--dim)}

.stop{
  border:1px solid var(--rule); background:var(--card); color:var(--dim);
  padding:6px 12px; border-radius:2px; font-size:12px; font-weight:500;
  transition:.12s;
}
.stop[data-live="1"]{border-color:var(--del); color:var(--del); background:var(--del-bg)}
.stop:hover{border-color:var(--ink); color:var(--ink)}

/* ---------- body ---------- */
.panes{display:grid; grid-template-columns:212px 1fr; min-height:0}
.side{
  border-right:1px solid var(--rule); background:var(--card);
  display:grid; grid-template-rows:auto 1fr auto; min-height:0;
}
.side section{padding:calc(var(--u)*2); min-height:0; overflow:auto}
.side .todos{border-top:1px solid var(--rule); max-height:34vh}
.side .work{border-bottom:1px solid var(--rule); max-height:38vh}
.side .head{display:flex; align-items:center; gap:8px; margin:0 0 calc(var(--u)*1.5)}
.side .head h2{margin:0; font:inherit}
.newbtn{margin-left:auto; border:1px solid var(--rule); background:var(--card); color:var(--dim);
  width:20px; height:20px; line-height:1; border-radius:2px; padding:0; font-size:14px}
.newbtn:hover{border-color:var(--signal); color:var(--signal)}

.slist{display:flex; flex-direction:column; gap:2px}
.srow{
  display:block; padding:6px 8px; border-radius:2px; text-decoration:none; color:var(--ink);
  border:1px solid transparent;
}
.srow:hover{background:var(--paper)}
.srow[aria-current="true"]{background:var(--signal-soft); border-color:#d9cdfa}
.srow .t{display:block; font-size:12.5px; line-height:1.3; overflow:hidden;
  text-overflow:ellipsis; white-space:nowrap}
.srow .m{display:block; font-family:var(--mono); font-size:10px; color:var(--dim); margin-top:2px}
.wt{color:var(--signal)}

dialog{border:1px solid var(--rule); border-radius:3px; padding:0; max-width:520px; width:92vw;
  box-shadow:0 24px 60px rgba(17,24,39,.16)}
dialog::backdrop{background:rgba(17,24,39,.28)}
dialog form{padding:calc(var(--u)*3)}
dialog h3{margin:0 0 4px; font-size:17px; letter-spacing:-.02em}
dialog .why{margin:0 0 18px; color:var(--dim); font-size:13.5px}
dialog textarea{width:100%; min-height:88px; resize:vertical; padding:11px 13px; font:inherit;
  background:var(--paper); border:1px solid var(--rule); border-radius:2px}
dialog textarea:focus{outline:none; border-color:var(--signal); background:var(--card)}
dialog select,dialog input[type=text]{width:100%; padding:8px 10px; font:inherit;
  background:var(--paper); border:1px solid var(--rule); border-radius:2px}
dialog input[type=text]{font-family:var(--mono); font-size:13px}
dialog select:focus,dialog input[type=text]:focus{outline:none; border-color:var(--signal); background:var(--card)}
.banner{
  display:flex; gap:10px; align-items:center; max-width:820px; margin:0 auto calc(var(--u)*2);
  border:1px solid var(--del); background:var(--del-bg); color:var(--del);
  padding:10px 14px; border-radius:2px; font-size:13.5px;
}
.banner .label{color:var(--del)}
.banner a{margin-left:auto; color:var(--del)}
dialog .field{margin-bottom:14px}
dialog .check{display:flex; gap:9px; align-items:flex-start; font-size:13.5px; margin:14px 0 20px}
dialog .check input{margin-top:3px}
dialog .check span{color:var(--dim)}
dialog .check b{display:block; color:var(--ink); font-weight:500}
dialog .actions{display:flex; gap:10px; justify-content:flex-end}
.ghost{border:1px solid var(--rule); background:var(--card); color:var(--dim); padding:8px 16px; border-radius:2px}
.ghost:hover{border-color:var(--ink); color:var(--ink)}

.badge{font-family:var(--mono); font-size:10px; letter-spacing:.06em; padding:2px 7px;
  border:1px solid #d9cdfa; color:var(--signal); background:var(--signal-soft); border-radius:999px}
.side h2{margin:0 0 calc(var(--u)*1.5); font:inherit}

/* ---------- the rail: this page's signature ---------- */
.rail{position:relative; padding-left:calc(var(--u)*2)}
.rail::before{
  content:""; position:absolute; left:calc(var(--u)*2 + 3px); top:6px; bottom:6px;
  width:1px; background:var(--rule-soft);
}
.tick{
  position:relative; display:block; width:100%; text-align:left;
  border:0; background:none; padding:5px 0 5px 20px; color:var(--ink);
  font-family:var(--mono); font-size:11.5px; line-height:1.4;
}
.tick::before{
  content:""; position:absolute; left:0; top:11px;
  width:7px; height:7px; border-radius:50%;
  background:var(--card); border:1.5px solid var(--rule); transition:.15s;
}
.tick:hover{color:var(--signal)}
.tick:hover::before{border-color:var(--signal)}
.tick .arg{color:var(--dim); display:block; overflow:hidden; text-overflow:ellipsis; white-space:nowrap}
.tick[data-live="1"]::before{background:var(--signal); border-color:var(--signal); animation:beat 1.1s ease-in-out infinite}
.tick[data-kind="write"]::before{border-color:var(--add)}
.tick[data-kind="deny"]::before{border-color:var(--del); background:var(--del)}
@keyframes beat{0%,100%{box-shadow:0 0 0 0 var(--signal-soft)}50%{box-shadow:0 0 0 5px var(--signal-soft)}}

.todo{display:flex; gap:8px; align-items:flex-start; padding:3px 0; font-size:12.5px}
.todo i{
  flex:0 0 auto; width:12px; height:12px; margin-top:4px; border-radius:2px;
  border:1.5px solid var(--rule); font-style:normal;
}
.todo[data-s="doing"] i{border-color:var(--signal); background:var(--signal-soft)}
.todo[data-s="done"] i{border-color:var(--add); background:var(--add)}
.todo[data-s="done"] span{color:var(--dim); text-decoration:line-through}

/* ---------- transcript ---------- */
.main{display:grid; grid-template-rows:1fr auto; min-height:0}
.scroll{overflow-y:auto; padding:calc(var(--u)*4) calc(var(--u)*4) calc(var(--u)*2)}
.stream{max-width:820px; margin:0 auto; display:flex; flex-direction:column; gap:calc(var(--u)*2)}

.empty{display:flex; flex-direction:column; align-items:center; justify-content:center;
  min-height:56vh; text-align:center; color:var(--dim)}
.empty h1{margin:0; font-size:26px; letter-spacing:-.025em; color:var(--ink); font-weight:600}
.empty .sub{margin:4px 0 26px}
.empty p{margin:0 0 4px; font-size:15px}
.empty .seeds{display:flex; flex-wrap:wrap; gap:8px; justify-content:center; margin-top:22px; max-width:520px}
.seed{
  border:1px solid var(--rule); background:var(--card); color:var(--ink);
  padding:7px 14px; border-radius:999px; font-size:13px; transition:.12s;
}
.seed:hover{border-color:var(--signal); color:var(--signal)}

.msg{display:grid; grid-template-columns:56px 1fr; gap:calc(var(--u)*2); align-items:start}
.msg .who{padding-top:3px}
.msg .body{min-width:0}
.msg p{margin:0 0 .7em} .msg p:last-child{margin:0}
.msg ul{margin:0 0 .7em; padding-left:1.1em} .msg ul:last-child{margin:0}
.msg li{margin:0 0 .25em}
.msg.user .body{
  background:var(--card); border:1px solid var(--rule); border-left:2px solid var(--ink);
  padding:calc(var(--u)*1.5) calc(var(--u)*2); border-radius:2px; white-space:pre-wrap;
}
.msg.think .body{color:var(--dim); font-size:14px; border-left:2px solid var(--rule-soft); padding-left:14px}
.msg code{font-family:var(--mono); font-size:.88em; background:var(--signal-soft); padding:1px 5px; border-radius:2px}
.msg pre{
  font-family:var(--mono); font-size:12.5px; background:var(--card);
  border:1px solid var(--rule); border-radius:2px; padding:12px 14px; overflow-x:auto;
}
.msg pre code{background:none; padding:0}

/* ---------- tool cards ---------- */
.tool{border:1px solid var(--rule); background:var(--card); border-radius:2px; overflow:hidden}
.tool > header{
  display:flex; align-items:center; gap:10px; padding:9px 12px;
  border-bottom:1px solid transparent;
}
.tool:has(.diff) > header,.tool:has(.note) > header{border-bottom-color:var(--rule-soft)}
.tool .name{font-family:var(--mono); font-size:12px; font-weight:600; color:var(--signal)}
.tool .args{font-family:var(--mono); font-size:12px; color:var(--dim); overflow:hidden; text-overflow:ellipsis; white-space:nowrap}
.tool .tally{margin-left:auto; font-family:var(--mono); font-size:11px; white-space:nowrap}
.tool .tally .a{color:var(--add)} .tool .tally .d{color:var(--del)}
.tool.err{border-color:var(--del)}
.tool.err .name{color:var(--del)}
.tool .note{padding:9px 12px; font-family:var(--mono); font-size:12px; color:var(--dim); white-space:pre-wrap}
.tool.err .note{color:var(--del); background:var(--del-bg)}
.tool.deny{border-color:var(--warn)}
.tool.deny .name{color:var(--warn)}
.tool.deny .note{color:var(--warn); background:var(--warn-bg)}

/* ---------- diff ---------- */
.diff{font-family:var(--mono); font-size:12px; line-height:1.65; overflow-x:auto}
.diff .row{display:grid; grid-template-columns:44px 1fr; white-space:pre}
.diff .n{text-align:right; padding-right:10px; color:#aab3c0; user-select:none; font-size:11px}
.diff .t{padding-right:14px}
.diff .add{background:var(--add-bg)} .diff .add .t::before{content:"+ "; color:var(--add)}
.diff .del{background:var(--del-bg)} .diff .del .t::before{content:"- "; color:var(--del)}
.diff .keep .t::before{content:"  "}
.diff .gap{color:#b9c1cc; padding:2px 0 2px 44px; background:#f7f8fa}

/* ---------- composer ---------- */
.composer{border-top:1px solid var(--rule); background:var(--card); padding:calc(var(--u)*2) calc(var(--u)*4)}
.composer .inner{max-width:820px; margin:0 auto; display:flex; flex-direction:column; gap:10px}
.composer textarea{
  width:100%; min-height:76px; resize:vertical; padding:12px 14px;
  font-family:var(--sans); font-size:15px; line-height:1.5; color:var(--ink);
  background:var(--paper); border:1px solid var(--rule); border-radius:2px;
}
.composer textarea:focus{outline:none; border-color:var(--signal); background:var(--card)}
.composer .row{display:flex; align-items:center; gap:12px}

.modes{display:flex; border:1px solid var(--rule); border-radius:2px; overflow:hidden}
.modes button{
  border:0; background:var(--card); color:var(--dim);
  padding:6px 14px; font-size:12px; font-weight:500;
}
.modes button[aria-pressed="true"]{background:var(--ink); color:var(--paper)}
.modes button:first-child[aria-pressed="true"]{background:var(--signal)}

.hint{margin-left:auto; font-family:var(--mono); font-size:11px; color:var(--dim)}
.send{
  border:0; background:var(--ink); color:var(--paper);
  padding:8px 20px; border-radius:2px; font-weight:600; font-size:13px;
}
.send:hover{background:var(--signal)}
.send:disabled{opacity:.4}

@view-transition{navigation:none}
::view-transition-new(*){animation:rise .22s cubic-bezier(.2,.7,.3,1)}
@keyframes rise{from{opacity:0; transform:translateY(6px)}to{opacity:1; transform:none}}
.send[disabled]{background:var(--dim)}
@media (prefers-reduced-motion:reduce){*{animation:none!important; transition:none!important}}
@media (max-width:860px){
  .panes{grid-template-columns:1fr} .side{display:none}
  .scroll,.composer{padding-left:calc(var(--u)*2); padding-right:calc(var(--u)*2)}
}
</style>
</head>
<body hx-ext="sse" sse-connect="/events?s={{SID}}"
      hx-on::sse-message="document.getElementById('empty')?.remove();document.getElementById('rail-empty')?.remove()">

<div class="shell">
  <header class="top">
    <div class="brand"><span class="dot"></span><b>Console</b></div>
    <div class="where">{{ROOT}}</div>{{BADGE}}
    <div class="spacer"></div>
    <div class="meter" id="meter" sse-swap="meter" hx-swap="innerHTML">{{METER}}</div>
    <button class="stop" id="stop" hx-post="/s/{{SID}}/interrupt" hx-swap="none" title="Stop the current turn">Stop</button>
  </header>

  <div class="panes">
    <aside class="side">
      <section class="work">
        <div class="head">
          <h2 class="label">Sessions</h2>
          <button class="newbtn" onclick="document.getElementById('newdlg').showModal()" title="New session">+</button>
        </div>
        <div class="slist">{{SESSIONS}}</div>
      </section>
      <section>
        <h2 class="label">Activity</h2>
        <span class="label" id="rail-empty" style="color:#aab3c0">Idle</span>
        <div class="rail" id="rail" sse-swap="rail" hx-swap="beforeend scroll:bottom">{{RAIL}}</div>
      </section>
      <section class="todos">
        <h2 class="label">Tasks</h2>
        <div id="todos" sse-swap="todos" hx-swap="innerHTML">{{TODOS}}</div>
      </section>
    </aside>

    <main class="main">
      <div class="scroll" id="scroll">
        {{BANNER}}
        <div class="stream" id="stream" sse-swap="msg" hx-swap="beforeend scroll:#scroll:bottom transition:true">{{STREAM}}
        </div>
      </div>

      <form class="composer" hx-post="/s/{{SID}}/prompt" hx-swap="none"
            hx-disabled-elt="find button[type=submit]"
            hx-on::after-request="if(event.detail.successful) this.querySelector('textarea').value=''">
        <div class="inner">
          <textarea name="prompt" id="prompt" placeholder="What needs doing?" autofocus></textarea>
          <div class="row">
            <div class="modes" id="modes" sse-swap="modes" hx-swap="innerHTML">{{MODES}}</div>
            <span class="hint">&#8984;&#9166; to send &middot; esc to stop</span>
            <button class="send" type="submit">Send</button>
          </div>
        </div>
      </form>
    </main>
  </div>
</div>


<dialog id="newdlg">
  <form method="post" action="/sessions">
    <h3>New session</h3>
    <p class="why">Its own claude process, its own transcript.</p>
    <div class="field">
      <label class="label" for="ws">Project</label>
      <select name="workspace" id="ws"
              onchange="document.getElementById('newpath').hidden = this.value !== 'new'">
        {{WSOPTIONS}}
        <option value="new">Add a project&hellip;</option>
      </select>
    </div>
    <div class="field" id="newpath" hidden>
      <label class="label" for="path">Path to the project</label>
      <input type="text" name="path" id="path" placeholder="~/code/api" spellcheck="false">
    </div>
    <div class="field">
      <label class="label" for="first">First message</label>
      <textarea name="prompt" id="first" placeholder="Optional. Leave empty to just open it."></textarea>
    </div>
    <label class="check">
      <input type="checkbox" name="worktree" value="1">
      <span><b>Work in a git worktree</b>
      Branches off HEAD into .worktrees, so this session cannot tread on anything else.</span>
    </label>
    <div class="actions">
      <button type="button" class="ghost" onclick="this.closest('dialog').close()">Cancel</button>
      <button type="submit" class="send">Start</button>
    </div>
  </form>
</dialog>

<script>
document.getElementById('prompt').addEventListener('keydown', ev => {
  if ((ev.metaKey || ev.ctrlKey) && ev.key === 'Enter') {
    ev.preventDefault();
    ev.target.closest('form').requestSubmit();
  }
});
document.addEventListener('keydown', ev => {
  if (ev.key === 'Escape') document.getElementById('stop').click();
});
document.getElementById('rail').addEventListener('click', ev => {
  const tick = ev.target.closest('[data-goto]');
  if (!tick) return;
  const card = document.getElementById(tick.dataset.goto);
  if (card) card.scrollIntoView({behavior:'smooth', block:'center'});
});
</script>
</body>
</html>`

func esc(s string) string { return html.EscapeString(s) }

// ---- fragments -------------------------------------------------------------

func meterHTML(turns int, cost float64, in, out int) string {
	cell := func(v, k string) string {
		return fmt.Sprintf(`<div class="cell"><span class="v">%s</span><span class="k">%s</span></div>`, v, k)
	}
	return cell(fmt.Sprintf("%d", turns), "turns") +
		cell(fmt.Sprintf("$%.3f", cost), "spent") +
		cell(fmt.Sprintf("%s / %s", compact(in), compact(out)), "tokens")
}

func compact(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

func modesHTML(sid, mode string) string {
	btn := func(id, text, help string) string {
		return fmt.Sprintf(
			`<button type="button" aria-pressed="%t" title="%s" hx-post="/s/%s/mode?m=%s" hx-swap="none">%s</button>`,
			mode == id, help, sid, id, text)
	}
	return btn("plan", "Plan", "Read and think. Edits are refused.") +
		btn("act", "Act", "Free to change files.")
}

func userHTML(text string) string {
	return fmt.Sprintf(`<div class="msg user"><span class="who label">You</span><div class="body">%s</div></div>`, esc(text))
}

func agentHTML(text string) string {
	return fmt.Sprintf(`<div class="msg agent"><span class="who label">Agent</span><div class="body">%s</div></div>`, markdown(text))
}

func thinkHTML(text string) string {
	return fmt.Sprintf(`<div class="msg think"><span class="who label">Thinking</span><div class="body">%s</div></div>`, esc(text))
}

func toolHTML(id, name, args string) string {
	return fmt.Sprintf(
		`<div class="tool" id="%s"><header><span class="name">%s</span><span class="args">%s</span>%s</header></div>`,
		id, esc(name), esc(args), tallySlot(id))
}

// diffHTML puts a patch into a card that is already on the page.
func diffHTML(cardID string, hunks []hunk) string {
	added, removed := countChanges(hunks)
	return fmt.Sprintf(`<div id="%s" hx-swap-oob="beforeend">%s</div>`, cardID, diffRows(hunks)) +
		fmt.Sprintf(`<span id="%s-tally" class="tally" hx-swap-oob="true">`+
			`<span class="a">+%d</span> <span class="d">-%d</span></span>`, cardID, added, removed)
}

// tallySlot is the placeholder a card carries so a diff can fill it in later.
func tallySlot(id string) string {
	return fmt.Sprintf(`<span id="%s-tally" class="tally"></span>`, id)
}

func todosHTML(items []todo) string {
	if len(items) == 0 {
		return `<span class="label" style="color:#aab3c0">None yet</span>`
	}
	var b strings.Builder
	for _, t := range items {
		fmt.Fprintf(&b, `<div class="todo" data-s="%s"><i></i><span>%s</span></div>`, esc(t.Status), esc(t.Task))
	}
	return b.String()
}

func tickHTML(cardID, name, arg, kind string, live bool, oob bool) string {
	swap := ""
	if oob {
		swap = ` hx-swap-oob="true"`
	}
	return fmt.Sprintf(
		`<button class="tick" data-live="%d" data-kind="%s" data-goto="%s" id="tick-%s"%s>%s<span class="arg">%s</span></button>`,
		btoi(live), kind, cardID, cardID, swap, esc(name), esc(arg))
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// markdown is a deliberately small renderer: fenced code, inline code,
// paragraphs and bullet lists. Anything cleverer belongs in a library, and this
// page does not need one.
//
// Soft line breaks inside a paragraph are joined. The model wraps its own
// output at around sixty characters, and honouring that would make the column
// as narrow as the model's line length rather than as wide as the page.
func markdown(src string) string {
	var b strings.Builder
	parts := strings.Split(src, "```")

	for i, part := range parts {
		if i%2 == 1 {
			body := part
			if nl := strings.IndexByte(body, '\n'); nl >= 0 {
				body = body[nl+1:]
			}
			fmt.Fprintf(&b, `<pre><code>%s</code></pre>`, esc(strings.TrimRight(body, "\n")))
			continue
		}
		for _, block := range strings.Split(strings.TrimSpace(part), "\n\n") {
			writeBlock(&b, block)
		}
	}
	return b.String()
}

func writeBlock(b *strings.Builder, block string) {
	lines := strings.Split(strings.TrimSpace(block), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return
	}

	if isBullet(lines[0]) {
		b.WriteString("<ul>")
		var item []string
		flush := func() {
			if len(item) > 0 {
				fmt.Fprintf(b, "<li>%s</li>", inlineCode(esc(strings.Join(item, " "))))
				item = nil
			}
		}
		for _, line := range lines {
			if isBullet(line) {
				flush()
				line = strings.TrimSpace(line)[1:]
			}
			item = append(item, strings.TrimSpace(line))
		}
		flush()
		b.WriteString("</ul>")
		return
	}

	joined := strings.Join(trimAll(lines), " ")
	fmt.Fprintf(b, "<p>%s</p>", inlineCode(esc(joined)))
}

func isBullet(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ")
}

func trimAll(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func inlineCode(s string) string {
	parts := strings.Split(s, "`")
	var b strings.Builder
	for i, p := range parts {
		if i%2 == 1 {
			fmt.Fprintf(&b, "<code>%s</code>", p)
			continue
		}
		b.WriteString(p)
	}
	return b.String()
}

// pageData is everything the shell needs to render.
type pageData struct {
	Title    string
	Root     string
	Badge    string
	Meter    string
	Modes    string
	Files    int
	Seeds    []string
	Sessions string
	Options  string
	Stream   string
	Rail     string
	Todos    string
	SID      string
	Error    string
}

func renderPage(d pageData) string {
	var seeds strings.Builder
	for _, seed := range d.Seeds {
		fmt.Fprintf(&seeds,
			`<button type="button" class="seed" onclick="const t=document.getElementById('prompt');t.value=this.textContent;t.focus()">%s</button>`,
			esc(seed))
	}

	stream := d.Stream
	if stream == "" {
		stream = `<div class="empty" id="empty">` +
			fmt.Sprintf(`<h1>%s</h1><span class="label sub">%d files &middot; 6 tools &middot; no built-ins</span>`, esc(d.Title), d.Files) +
			`<p>Nothing yet.</p><p>Ask for something, or tell it what is broken.</p>` +
			`<div class="seeds">` + seeds.String() + `</div></div>`
	}

	todos := d.Todos
	if todos == "" {
		todos = `<span class="label" style="color:#aab3c0">None yet</span>`
	}

	banner := ""
	if d.Error != "" {
		banner = fmt.Sprintf(
			`<div class="banner"><span class="label">Could not add</span> %s <a href="?">Dismiss</a></div>`,
			esc(d.Error))
	}

	return strings.NewReplacer(
		"{{BANNER}}", banner,
		"{{TITLE}}", esc(d.Title),
		"{{ROOT}}", esc(d.Root),
		"{{BADGE}}", d.Badge,
		"{{METER}}", d.Meter,
		"{{MODES}}", d.Modes,
		"{{SESSIONS}}", d.Sessions,
		"{{WSOPTIONS}}", d.Options,
		"{{STREAM}}", stream,
		"{{RAIL}}", d.Rail,
		"{{TODOS}}", todos,
		"{{SID}}", esc(d.SID),
	).Replace(page)
}

// renderEntry turns one recorded event into its finished markup. Streaming and
// replay both go through here, so a reloaded page cannot drift from a live one.
func renderEntry(e *entry) string {
	switch e.Kind {
	case "user":
		return userHTML(e.Text)
	case "agent":
		return agentHTML(e.Text)
	case "think":
		return thinkHTML(e.Text)
	case "deny":
		return fmt.Sprintf(
			`<div class="tool deny"><header><span class="name">%s</span></header><div class="note">%s</div></div>`,
			esc(e.Name), esc(e.Text))
	case "error":
		return fmt.Sprintf(
			`<div class="tool err"><header><span class="name">%s</span></header><div class="note">%s</div></div>`,
			esc(e.Name), esc(e.Text))
	}

	class := "tool"
	if e.Settled == "deny" {
		class = "tool deny"
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<div class="%s" id="%s"><header><span class="name">%s</span><span class="args">%s</span>`,
		class, esc(e.ID), esc(e.Name), esc(e.Arg))

	if len(e.Hunks) > 0 {
		added, removed := countChanges(e.Hunks)
		fmt.Fprintf(&b, `<span id="%s-tally" class="tally"><span class="a">+%d</span> <span class="d">-%d</span></span>`,
			esc(e.ID), added, removed)
	} else {
		b.WriteString(tallySlot(e.ID))
	}
	b.WriteString(`</header>`)

	if e.Note != "" {
		fmt.Fprintf(&b, `<div class="note">%s</div>`, esc(e.Note))
	}
	if len(e.Hunks) > 0 {
		b.WriteString(diffRows(e.Hunks))
	}
	b.WriteString(`</div>`)
	return b.String()
}

// diffRows is the diff body, shared by the live fragment and the replay.
func diffRows(hunks []hunk) string {
	var b strings.Builder
	b.WriteString(`<div class="diff">`)
	for _, h := range hunks {
		if h.Kind == "gap" {
			b.WriteString(`<div class="gap">&middot;&middot;&middot;</div>`)
			continue
		}
		n := h.New
		if h.Kind == "del" {
			n = h.Old
		}
		fmt.Fprintf(&b, `<div class="row %s"><span class="n">%d</span><span class="t">%s</span></div>`,
			h.Kind, n, esc(h.Text))
	}
	b.WriteString(`</div>`)
	return b.String()
}

func stopHTML(live bool) string {
	return fmt.Sprintf(
		`<button class="stop" id="stop" data-live="%d" hx-post="/s/{{SID}}/interrupt" hx-swap="none" hx-swap-oob="true">Stop</button>`,
		btoi(live))
}

func sessionsHTML(sessions []*Session, workspaces map[string]*Workspace, current string) string {
	if len(sessions) == 0 {
		return `<span class="label" style="color:#aab3c0">None yet</span>`
	}
	var b strings.Builder
	for _, s := range sessions {
		title := s.Title
		if title == "" {
			title = "Untitled"
		}
		mark := ""
		if s.Worktree {
			mark = ` <span class="wt">&#9282;</span>`
		}
		name := ""
		if w := workspaces[s.WorkspaceID]; w != nil {
			name = w.Name
		}
		fmt.Fprintf(&b,
			`<a class="srow" href="/s/%s" aria-current="%t"><span class="t">%s</span><span class="m">%s &middot; %s%s</span></a>`,
			esc(s.ID), s.ID == current, esc(title), esc(name), stamp(s.Created), mark)
	}
	return b.String()
}

func optionsHTML(workspaces []*Workspace, current string) string {
	var b strings.Builder
	for _, w := range workspaces {
		fmt.Fprintf(&b, `<option value="%s"%s>%s</option>`,
			esc(w.ID), map[bool]string{true: " selected"}[w.ID == current], esc(w.Root))
	}
	return b.String()
}

func badgeHTML(branch string) string {
	if branch == "" {
		return ""
	}
	return fmt.Sprintf(`<span class="badge">&#9282; %s</span>`, esc(branch))
}
