"use strict";
// factory web UI — no framework, no build step. Talks to the JSON API under ./api/.

const $ = (sel, el = document) => el.querySelector(sel);
const $$ = (sel, el = document) => Array.from(el.querySelectorAll(sel));
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

const S = {
  projects: [],
  route: { view: "home" },
  detail: null,
  chat: { id: null, after: 0, waiting: false, active: false, exists: false },
  log: { id: null, offset: -1, follow: true },
  artifacts: [],
  artifact: null,
  openTask: null,
  tick: 0,
};

const PHASES = [
  { key: "spec", label: "Spec", sub: "interview & approve" },
  { key: "specified", label: "Plan", sub: "tasks & roundtable" },
  { key: "building", label: "Build", sub: "design → code → test → review" },
  { key: "accepting", label: "Accept", sub: "real use-case trials" },
  { key: "done", label: "Done", sub: "report" },
];
const PHASE_LABEL = { spec: "speccing", specified: "planning", building: "building", accepting: "acceptance", done: "done" };
const STATUS_ICON = { done: "✔", blocked: "✖", in_progress: "▶", pending: "·" };

// ---------- API ----------

async function api(path, opts = {}) {
  const init = { method: opts.method || "GET", credentials: "same-origin", headers: {} };
  if (opts.body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(opts.body);
  }
  const res = await fetch("api/" + path, init);
  if (res.status === 401) {
    showLogin();
    throw new Error("Please sign in");
  }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function toast(msg, bad = false) {
  const t = $("#toast");
  t.textContent = msg;
  t.className = "toast" + (bad ? " bad" : "");
  t.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => (t.hidden = true), bad ? 6000 : 3000);
}

async function act(fn, okMsg) {
  try {
    await fn();
    if (okMsg) toast(okMsg);
    await refresh(true);
  } catch (e) {
    toast(e.message, true);
  }
}

// ---------- auth ----------

function showLogin() {
  $("#login").hidden = false;
  setTimeout(() => $("#login-token").focus(), 50);
}

$("#login-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const err = $("#login-error");
  err.hidden = true;
  try {
    await api("login", { method: "POST", body: { token: $("#login-token").value } });
    $("#login").hidden = true;
    $("#login-token").value = "";
    $("#logout-btn").hidden = false;
    await refresh(true);
  } catch (e) {
    err.textContent = e.message;
    err.hidden = false;
  }
});

$("#logout-btn").addEventListener("click", async () => {
  await api("logout", { method: "POST", body: {} }).catch(() => {});
  location.reload();
});

$("#menu-btn").addEventListener("click", () => $("#sidebar").classList.toggle("open"));

// ---------- markdown (safe subset: everything is escaped first) ----------

function inline(s) {
  return esc(s)
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/(^|[\s(])_([^_\n]+)_(?=[\s).,;:!?]|$)/g, "$1<em>$2</em>");
}

function md(src) {
  const lines = String(src ?? "").replace(/\r/g, "").split("\n");
  const out = [];
  const isBlock = (l) => /^(```|#{1,6}\s|>\s?|\s*([-*•]|\d+\.)\s+|\|)/.test(l);
  let i = 0;
  while (i < lines.length) {
    const l = lines[i];
    if (/^```/.test(l)) {
      const buf = [];
      i++;
      while (i < lines.length && !/^```/.test(lines[i])) buf.push(lines[i++]);
      i++;
      out.push(`<pre><code>${esc(buf.join("\n"))}</code></pre>`);
    } else if (/^(#{1,6})\s+/.test(l)) {
      const m = l.match(/^(#{1,6})\s+(.*)$/);
      const n = Math.min(m[1].length + 1, 6);
      out.push(`<h${n}>${inline(m[2])}</h${n}>`);
      i++;
    } else if (/^\|/.test(l) && i + 1 < lines.length && /^\|?\s*:?-{3,}/.test(lines[i + 1])) {
      const row = (r) => r.replace(/^\|/, "").replace(/\|\s*$/, "").split(/(?<!\\)\|/).map((c) => c.trim().replace(/\\\|/g, "|"));
      const head = row(l);
      i += 2;
      let html = "<table><thead><tr>" + head.map((h) => `<th>${inline(h)}</th>`).join("") + "</tr></thead><tbody>";
      while (i < lines.length && /^\|/.test(lines[i])) {
        html += "<tr>" + row(lines[i++]).map((c) => `<td>${inline(c)}</td>`).join("") + "</tr>";
      }
      out.push(html + "</tbody></table>");
    } else if (/^\s*([-*•]|\d+\.)\s+/.test(l)) {
      const ordered = /^\s*\d+\./.test(l);
      const items = [];
      while (i < lines.length && /^\s*([-*•]|\d+\.)\s+/.test(lines[i])) {
        items.push(lines[i].replace(/^\s*([-*•]|\d+\.)\s+/, ""));
        i++;
        while (i < lines.length && /^\s{2,}\S/.test(lines[i]) && !/^\s*([-*•]|\d+\.)\s+/.test(lines[i])) items[items.length - 1] += " " + lines[i++].trim();
      }
      const tag = ordered ? "ol" : "ul";
      out.push(`<${tag}>` + items.map((it) => `<li>${inline(it)}</li>`).join("") + `</${tag}>`);
    } else if (/^>\s?/.test(l)) {
      const buf = [];
      while (i < lines.length && /^>\s?/.test(lines[i])) buf.push(lines[i++].replace(/^>\s?/, ""));
      out.push(`<blockquote>${inline(buf.join(" "))}</blockquote>`);
    } else if (!l.trim()) {
      i++;
    } else {
      const buf = [];
      while (i < lines.length && lines[i].trim() && !isBlock(lines[i])) buf.push(lines[i++]);
      if (!buf.length) buf.push(lines[i++]);
      out.push(`<p>${buf.map(inline).join("<br>")}</p>`);
    }
  }
  return `<div class="md">${out.join("\n")}</div>`;
}

// ---------- helpers ----------

function ago(t) {
  if (!t) return "";
  const s = Math.max(0, (Date.now() - new Date(t).getTime()) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return Math.floor(s / 60) + "m ago";
  if (s < 86400) return Math.floor(s / 3600) + "h ago";
  return Math.floor(s / 86400) + "d ago";
}
const pct = (a, b) => (b ? Math.round((100 * a) / b) : 0);
const pill = (cls, text) => `<span class="pill ${esc(cls)}">${esc(text)}</span>`;

function phasePill(p) {
  if (p.outcome) return pill(p.outcome, p.outcome === "complete" ? "complete" : "finished with issues");
  return pill(p.phase, PHASE_LABEL[p.phase] || p.phase);
}

// ---------- routing ----------

function parseRoute() {
  const parts = location.hash.replace(/^#\/?/, "").split("/").filter(Boolean).map(decodeURIComponent);
  if (parts[0] === "new") return { view: "new" };
  if (parts[0] === "agents") return { view: "agents" };
  if (parts[0] === "p" && parts[1]) return { view: "project", id: parts[1], tab: parts[2] || "overview" };
  return { view: "home" };
}

window.addEventListener("hashchange", () => route());

function route() {
  S.route = parseRoute();
  $("#sidebar").classList.remove("open");
  renderSidebar();
  const r = S.route;
  if (r.view === "new") return renderNew();
  if (r.view === "agents") return renderAgents();
  if (r.view === "project") return renderProjectShell();
  renderHome();
}

// ---------- sidebar ----------

function renderSidebar() {
  const list = $("#project-list");
  if (!S.projects.length) {
    list.innerHTML = `<p class="muted pad">No projects yet.<br><a href="#/new">Create your first one →</a></p>`;
    return;
  }
  const cur = S.route.view === "project" ? S.route.id : null;
  list.innerHTML = S.projects
    .map((p) => {
      const indicator = p.waiting ? `<span class="dot ask" title="Waiting for your answer"></span>` : p.running || p.interview ? `<span class="dot pulse" title="Working"></span>` : "";
      const meta = p.total ? `${p.done}/${p.total} tasks${p.blocked ? ` · ${p.blocked} blocked` : ""}` : p.phase === "spec" ? (p.waiting ? "waiting for you" : "spec interview") : "";
      return `<a class="project-item ${p.id === cur ? "active" : ""}" href="#/p/${encodeURIComponent(p.id)}">
        <div class="row">${indicator}<span class="name">${esc(p.name)}</span>${phasePill(p)}</div>
        <div class="meta">${esc(meta)}${meta ? " · " : ""}${esc(ago(p.updated))}</div>
        ${p.total ? `<div class="bar" style="margin-top:6px"><span style="width:${pct(p.done, p.total)}%"></span></div>` : ""}
      </a>`;
    })
    .join("") + `<a class="project-item only-mobile" href="#/agents"><span class="name">Agents & health</span></a>`;
}

// ---------- home ----------

function renderHome() {
  $("#main").innerHTML = `<div class="hero">
    <h1>Submit an idea. Spec it together. Walk away.</h1>
    <p class="lead">factory interviews you until the idea is buildable, has your agents critique the spec, then plans, designs, builds, tests, reviews and trials every genuine use case until it's done — while you're somewhere else.</p>
    <div class="flow">
      ${[
        ["Interview", "Your interviewer asks targeted questions with sensible defaults. Leave any blank."],
        ["Spec roundtable", "The panel attacks the draft: missing use cases, scope, testability."],
        ["Plan", "Ordered tasks with acceptance criteria and an end-to-end test per use case."],
        ["Design roundtable", "Before each task, the panel agrees approach, edge cases and required tests."],
        ["Build → test → review", "The builder codes; factory runs the tests; a strict reviewer rejects fake tests."],
        ["Acceptance", "Agents actually use the software as its users would. Gaps become fix tasks."],
      ].map(([h, p], i) => `<div class="card"><div class="n">${i + 1}</div><h3>${h}</h3><p>${p}</p></div>`).join("")}
    </div>
    <a class="btn primary" href="#/new">+ Start a new project</a>
    ${S.projects.length ? `<a class="btn" href="#/p/${encodeURIComponent(S.projects[0].id)}" style="margin-left:8px">Open ${esc(S.projects[0].name)}</a>` : ""}
  </div>`;
}

// ---------- new project ----------

function renderNew() {
  $("#main").innerHTML = `<div style="max-width:720px">
    <h1>New project</h1>
    <p class="muted">Describe the idea in your own words. The interviewer will ask follow-up questions in the next step.</p>
    <form id="new-form" class="card">
      <label for="np-name">Name</label>
      <input id="np-name" required pattern="[A-Za-z0-9][A-Za-z0-9._\\-]{0,63}" placeholder="e.g. recipe-planner" autocomplete="off">
      <div class="hint">Letters, digits, dots, dashes or underscores. It becomes the project folder.</div>
      <label for="np-idea">Idea</label>
      <textarea id="np-idea" rows="9" required placeholder="Who is it for, what should they be able to do, anything you already know about platform, stack or constraints…"></textarea>
      <label class="check"><input id="np-auto" type="checkbox" checked> Start the autonomous build as soon as I approve the spec</label>
      <div class="form-actions"><button class="btn primary" type="submit">Create & start interview</button><a class="btn ghost" href="#/">Cancel</a></div>
    </form>
  </div>`;
  $("#np-name").focus();
  $("#new-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const btn = $("button[type=submit]", ev.target);
    btn.disabled = true;
    try {
      const res = await api("projects", { method: "POST", body: { name: $("#np-name").value.trim(), idea: $("#np-idea").value, auto_start: $("#np-auto").checked } });
      await refresh();
      location.hash = `#/p/${encodeURIComponent(res.id)}/interview`;
    } catch (e) {
      toast(e.message, true);
      btn.disabled = false;
    }
  });
}

// ---------- agents ----------

async function renderAgents() {
  $("#main").innerHTML = `<h1>Agents</h1><p class="muted">Loaded from your factory config. New projects snapshot this config when they're created.</p><div id="agents-body" class="muted">Loading…</div>`;
  let cfg;
  try {
    cfg = await api("config");
  } catch (e) {
    $("#agents-body").textContent = e.message;
    return;
  }
  if (cfg.error) {
    $("#agents-body").innerHTML = `<div class="card"><p class="error-text">${esc(cfg.error)}</p><p class="muted">Create one with <code>factory init</code> and restart the server, or pass <code>--config</code>.</p></div>`;
    return;
  }
  const roles = cfg.roles || {};
  const roleOf = (name) => Object.entries(roles).filter(([, v]) => v === name).map(([k]) => k);
  $("#agents-body").innerHTML = `
    <div class="card"><div class="toolbar"><h2 style="margin:0">Configured agents</h2><span class="muted mono" style="margin-left:auto">${esc(cfg.path)}</span></div>
      <div class="table-wrap"><table class="table"><thead><tr><th>Agent</th><th>Type</th><th>Model</th><th>Roles</th><th>Health</th></tr></thead><tbody>
      ${cfg.agents.map((a) => `<tr><td><strong>${esc(a.name)}</strong></td><td>${esc(a.type)}${a.command ? ` <span class="muted mono">${esc(a.command)}</span>` : ""}</td><td class="mono">${esc(a.model || "default")}</td><td>${roleOf(a.name).map((r) => pill("", r)).join(" ") || '<span class="muted">roundtable only</span>'}</td><td id="health-${esc(a.name)}" class="muted">—</td></tr>`).join("")}
      </tbody></table></div>
      <div class="form-actions"><button id="doctor-btn" class="btn primary">Check all agents</button><span class="muted" style="align-self:center">Sends each agent a one-word prompt.</span></div>
    </div>
    <div class="card"><h2>Roundtable</h2>
      <p class="muted">${cfg.roundtable.rounds} round(s) · on spec: ${cfg.roundtable.on_spec !== false ? "yes" : "no"} · on plan: ${cfg.roundtable.on_plan !== false ? "yes" : "no"} · on every task: ${cfg.roundtable.on_tasks !== false ? "yes" : "no"}</p>
      ${(cfg.roundtable.participants || []).map((p, i) => `<p><strong>Seat ${i + 1} · ${esc(p.agent)}</strong><br><span class="muted">${esc(p.persona)}</span></p>`).join("") || '<p class="muted">No panel configured — the moderator decides alone.</p>'}
    </div>
    <div class="card"><h2>Limits</h2><dl class="kv">
      ${Object.entries(cfg.limits).map(([k, v]) => `<dt>${esc(k.replace(/_/g, " "))}</dt><dd class="mono">${esc(v)}</dd>`).join("")}
      <dt>notifications</dt><dd>${cfg.notify ? "notify_command set" : "none"}</dd></dl></div>`;
  $("#doctor-btn").addEventListener("click", async (ev) => {
    ev.target.disabled = true;
    ev.target.textContent = "Checking…";
    cfg.agents.forEach((a) => ($("#health-" + CSS.escape(a.name)).textContent = "checking…"));
    try {
      const res = await api("doctor", { method: "POST", body: {} });
      res.results.forEach((r) => {
        const cell = $("#health-" + CSS.escape(r.name));
        if (cell) cell.innerHTML = r.ok ? `<span class="s-done">✔ ${esc(r.duration)}</span>` : `<span class="s-blocked" title="${esc(r.error)}">✖ ${esc(r.error).slice(0, 120)}</span>`;
      });
    } catch (e) {
      toast(e.message, true);
    }
    ev.target.disabled = false;
    ev.target.textContent = "Check all agents";
  });
}

// ---------- project ----------

function renderProjectShell() {
  const { id, tab } = S.route;
  if (!S.detail || S.detail.summary.id !== id) S.detail = null;
  $("#main").innerHTML = `<div id="p-head" class="p-head"><h1>${esc(id)}</h1></div><div id="p-stepper"></div>
    <nav id="p-tabs" class="tabs"></nav><section id="p-body"><p class="muted">Loading…</p></section>`;
  if (S.chat.id !== id) S.chat = { id, after: 0, waiting: false, active: false, exists: false };
  if (S.log.id !== id) S.log = { id, offset: -1, follow: true };
  renderTabBody(tab);
  loadProject();
}

async function loadProject() {
  const { id } = S.route;
  try {
    const d = await api("projects/" + encodeURIComponent(id));
    if (S.route.view !== "project" || S.route.id !== id) return;
    S.detail = d;
    // Keep the sidebar entry in step with the freshest detail.
    const i = S.projects.findIndex((x) => x.id === id);
    if (i >= 0) {
      S.projects[i] = { ...S.projects[i], ...d.summary, interview: d.session.active, waiting: !!d.session.waiting };
      renderSidebar();
    }
    renderProjectChrome();
    if (S.route.tab === "overview") renderOverview();
  } catch (e) {
    if (S.route.view === "project") $("#p-body").innerHTML = `<div class="card"><p class="error-text">${esc(e.message)}</p></div>`;
  }
}

function renderProjectChrome() {
  const d = S.detail;
  const p = d.project, sum = d.summary, sess = d.session;
  const running = sum.running, interviewing = sess.active;
  const btns = [];
  if (interviewing) btns.push(`<button class="btn" data-act="pause-spec">Pause interview</button>`);
  else if (p.phase === "spec") btns.push(`<button class="btn primary" data-act="resume-spec">${p.interview && p.interview.length ? "Resume interview" : "Start interview"}</button>`);
  if (running) btns.push(`<button class="btn danger" data-act="stop">■ Stop build</button>`);
  else if (p.phase !== "spec" && p.phase !== "done") btns.push(`<button class="btn primary" data-act="run">▶ ${p.tasks && p.tasks.some((t) => t.attempts) ? "Resume build" : "Start build"}</button>`);
  if (p.phase !== "spec" && !running && !interviewing) btns.push(`<button class="btn ghost" data-act="respec">Reopen spec</button>`);

  $("#p-head").innerHTML = `<h1>${esc(p.name)}</h1>${phasePill(sum)}
    ${running ? `<span class="pill building"><span class="dot pulse"></span> running · pid ${sum.pid}</span>` : ""}
    ${sess.waiting ? `<span class="pill spec"><span class="dot ask"></span> waiting for your answer</span>` : ""}
    <div class="actions">${btns.join("")}</div>`;
  $$("#p-head [data-act]").forEach((b) => b.addEventListener("click", () => projectAction(b.dataset.act)));

  const idx = PHASES.findIndex((x) => x.key === p.phase);
  $("#p-stepper").innerHTML = `<div class="stepper">${PHASES.map((ph, i) => `<div class="step ${i < idx || p.phase === "done" ? "done" : i === idx ? "current" : ""}">${ph.label}<small>${ph.sub}</small></div>`).join("")}</div>`;

  const tabs = [
    ["overview", "Overview", ""],
    ["interview", "Interview", sess.waiting ? `<span class="dot ask"></span>` : interviewing ? `<span class="dot pulse"></span>` : ""],
    ["log", "Live log", running ? `<span class="dot pulse"></span>` : ""],
    ["artifacts", "Roundtables & files", ""],
  ];
  $("#p-tabs").innerHTML = tabs.map(([k, label, extra]) => `<a class="tab ${S.route.tab === k ? "active" : ""}" href="#/p/${encodeURIComponent(sum.id)}/${k}">${label}${extra}</a>`).join("");
}

async function projectAction(a) {
  const id = encodeURIComponent(S.route.id);
  if (a === "run") return act(() => api(`projects/${id}/run`, { method: "POST", body: {} }), "Build started in the background");
  if (a === "stop") {
    if (!confirm("Stop the build? Progress is saved and you can resume later.")) return;
    return act(() => api(`projects/${id}/stop`, { method: "POST", body: {} }), "Stop requested");
  }
  if (a === "pause-spec") return act(() => api(`projects/${id}/spec`, { method: "DELETE" }), "Interview paused");
  if (a === "resume-spec") {
    await act(() => api(`projects/${id}/spec`, { method: "POST", body: { auto_start: true } }));
    location.hash = `#/p/${id}/interview`;
    return;
  }
  if (a === "respec") {
    if (!confirm("Reopen the spec? This discards the current plan and task progress (code and git history are kept). The build will restart after you approve the new spec.")) return;
    await act(() => api(`projects/${id}/spec`, { method: "POST", body: { reset: true, auto_start: true } }));
    S.chat = { id: S.route.id, after: 0, waiting: false, active: false, exists: false };
    location.hash = `#/p/${id}/interview`;
  }
}

function renderTabBody(tab) {
  if (tab === "interview") return renderChat();
  if (tab === "log") return renderLog();
  if (tab === "artifacts") return renderArtifacts();
  if (S.detail) renderOverview();
}

// ---------- overview ----------

function renderOverview() {
  const d = S.detail;
  if (!d) return;
  const p = d.project, sum = d.summary;
  const tasks = p.tasks || [];
  const ucs = (p.spec && p.spec.use_cases) || [];
  const body = $("#p-body");
  const scrollY = window.scrollY;

  if (p.phase === "spec") {
    body.innerHTML = `<div class="card"><h2>Spec interview</h2>
      <p>${d.session.active ? "The interview is in progress." : "The spec hasn't been approved yet."} Answer the questions on the <a href="#/p/${encodeURIComponent(sum.id)}/interview">Interview tab</a>; once you approve the spec, the build ${d.session.auto_start ? "starts automatically" : "can be started from here"}.</p>
      <h3>Your idea</h3>${md(p.idea)}
      ${p.interview && p.interview.length ? `<h3>Answers so far</h3>${p.interview.map((qa) => `<p><strong>${esc(qa.q)}</strong><br>${esc(qa.a)}</p>`).join("")}` : ""}</div>`;
    return;
  }

  body.innerHTML = `
    <div class="grid stats">
      <div class="card stat"><div class="label">Tasks</div><div class="value">${sum.done}/${sum.total}</div><div class="bar" style="margin-top:8px"><span style="width:${pct(sum.done, sum.total)}%"></span></div></div>
      <div class="card stat"><div class="label">Blocked</div><div class="value ${sum.blocked ? "s-blocked" : ""}">${sum.blocked}</div><div class="sub">after max attempts</div></div>
      <div class="card stat"><div class="label">Use cases</div><div class="value">${sum.satisfied}/${sum.use_cases}</div><div class="sub">satisfied at acceptance</div></div>
      <div class="card stat"><div class="label">Acceptance</div><div class="value">${p.acceptance_round}</div><div class="sub">round(s) completed</div></div>
    </div>
    <div class="card"><div class="toolbar"><h2 style="margin:0">Tasks</h2><span class="muted" style="margin-left:auto">tests: <code>${esc(p.test_command || "—")}</code></span></div>
      ${tasks.length ? `<div class="table-wrap"><table class="table"><thead><tr><th></th><th>ID</th><th>Task</th><th>Kind</th><th>Attempts</th><th>Commit</th></tr></thead><tbody>
        ${tasks.map((t) => taskRow(t)).join("")}</tbody></table></div>` : `<p class="muted">${p.phase === "specified" ? "Planning will create the task list when the build starts." : "No tasks yet."}</p>`}
    </div>
    <div class="card"><h2>Use cases</h2>${ucs.map((u) => `
      <div style="margin-bottom:14px"><div class="row" style="display:flex;gap:8px;align-items:center;flex-wrap:wrap"><strong>${esc(u.id)}</strong> ${esc(u.goal)} ${pill(u.verdict || "", u.verdict || "not judged yet")}</div>
      <div class="muted" style="font-size:13px">${esc(u.actor)} · ${esc(u.scenario)}</div>
      <div style="font-size:13px">Success: ${esc(u.success)}</div>
      ${u.gaps && u.gaps.length ? `<ul class="gaps">${u.gaps.map((g) => `<li>${esc(g)}</li>`).join("")}</ul>` : ""}</div>`).join("") || '<p class="muted">No use cases in the spec.</p>'}
    </div>
    <div class="card"><h2>Project</h2><dl class="kv">
      <dt>Summary</dt><dd>${esc(p.spec.summary)}</dd>
      <dt>Stack</dt><dd>${esc(p.spec.stack || "—")}</dd>
      <dt>Directory</dt><dd class="mono">${esc(sum.dir)}</dd>
      <dt>Created</dt><dd>${esc(new Date(p.created).toLocaleString())}</dd>
      <dt>Updated</dt><dd>${esc(ago(p.updated))}</dd></dl></div>`;
  $$("tr[data-task]", body).forEach((tr) =>
    tr.addEventListener("click", () => {
      S.openTask = S.openTask === tr.dataset.task ? null : tr.dataset.task;
      renderOverview();
    })
  );
  window.scrollTo(0, scrollY);
}

function taskRow(t) {
  const open = S.openTask === t.id;
  let html = `<tr class="clickable" data-task="${esc(t.id)}"><td class="status-icon s-${esc(t.status)}" title="${esc(t.status)}">${STATUS_ICON[t.status] || "·"}</td>
    <td class="mono">${esc(t.id)}</td><td>${esc(t.title)}${t.status === "in_progress" ? ' <span class="pill building">working</span>' : ""}</td>
    <td>${pill("", t.kind || "feature")}</td><td>${t.attempts}</td><td class="mono">${esc(t.commit || "")}</td></tr>`;
  if (open) {
    html += `<tr class="detail"><td></td><td colspan="5">
      ${md(t.description)}
      ${t.acceptance && t.acceptance.length ? `<h3>Acceptance criteria</h3><ul>${t.acceptance.map((a) => `<li>${esc(a)}</li>`).join("")}</ul>` : ""}
      ${t.depends_on && t.depends_on.length ? `<p class="muted">Depends on ${t.depends_on.map(esc).join(", ")}</p>` : ""}
      ${t.brief ? `<h3>Design brief (roundtable)</h3>${md(t.brief)}` : ""}
      ${t.last_feedback ? `<h3>Feedback for the next attempt</h3>${md(t.last_feedback)}` : ""}
      ${t.notes && t.notes.length ? `<h3>Notes</h3><ul>${t.notes.map((n) => `<li>${esc(n)}</li>`).join("")}</ul>` : ""}
      <p><a href="#/p/${encodeURIComponent(S.route.id)}/artifacts">Briefs, builder summaries, test logs and reviews →</a></p>
    </td></tr>`;
  }
  return html;
}

// ---------- interview chat ----------

function renderChat() {
  $("#p-body").innerHTML = `<div class="card chat">
    <div id="chat-banner" class="chat-banner" hidden></div>
    <div id="chat-log" class="chat-log"></div>
    <div id="chat-typing" class="typing" hidden>agents are working</div>
    <form id="composer" class="composer">
      <textarea id="chat-input" rows="2" placeholder="Type your answer…  (Ctrl/⌘+Enter to send)"></textarea>
      <button id="chat-send" class="btn primary" type="submit">Send</button>
    </form></div>`;
  S.chat.after = 0;
  const input = $("#chat-input");
  const autosize = () => {
    input.style.height = "auto";
    input.style.height = Math.min(200, input.scrollHeight) + "px";
    $("#chat-send").textContent = input.value.trim() ? "Send" : "Send (no preference)";
  };
  input.addEventListener("input", autosize);
  input.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter" && (ev.ctrlKey || ev.metaKey)) {
      ev.preventDefault();
      $("#composer").requestSubmit();
    }
  });
  $("#composer").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    await sendAnswer(input.value);
  });
  autosize();
  pollChat();
}

async function sendAnswer(text) {
  const input = $("#chat-input");
  $("#chat-send").disabled = true;
  try {
    await api(`projects/${encodeURIComponent(S.route.id)}/chat`, { method: "POST", body: { text } });
    if (input) input.value = "";
    S.chat.waiting = false;
    $("#chat-send").textContent = "Send (no preference)";
  } catch (e) {
    toast(e.message, true);
  }
  await pollChat();
}

async function pollChat() {
  const id = S.route.id;
  let res;
  try {
    res = await api(`projects/${encodeURIComponent(id)}/chat?after=${S.chat.after}`);
  } catch (e) {
    return;
  }
  if (S.route.view !== "project" || S.route.id !== id || S.route.tab !== "interview") return;
  const log = $("#chat-log");
  if (!log) return;
  const nearBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 80;
  for (const m of res.messages) {
    S.chat.after = Math.max(S.chat.after, m.id);
    const div = document.createElement("div");
    div.className = "msg " + m.role;
    div.textContent = m.text;
    if (m.quick && m.quick.length) {
      const q = document.createElement("div");
      q.className = "quick";
      q.dataset.quick = "1";
      for (const b of m.quick) {
        const btn = document.createElement("button");
        btn.className = "btn small" + (b.value === "y" ? " primary" : "");
        btn.type = "button";
        btn.textContent = b.label;
        btn.addEventListener("click", () => sendAnswer(b.value));
        q.appendChild(btn);
      }
      div.appendChild(q);
    }
    log.appendChild(div);
  }
  // Only the latest question's quick replies are live.
  const quicks = $$("[data-quick]", log);
  quicks.forEach((q, i) => (q.hidden = !(res.waiting && i === quicks.length - 1 && log.lastElementChild && log.lastElementChild.contains(q))));

  S.chat.waiting = res.waiting;
  S.chat.active = res.active;
  S.chat.exists = res.exists;
  $("#chat-send").disabled = !res.waiting;
  $("#chat-input").disabled = !res.waiting;
  $("#chat-typing").hidden = !(res.active && !res.waiting);

  const banner = $("#chat-banner");
  const phase = S.detail && S.detail.project.phase;
  if (!res.active) {
    banner.hidden = false;
    if (phase === "spec") {
      banner.innerHTML = `<span>${res.exists ? "The interview is paused." : "No interview is running."} Your answers so far are saved.</span><button class="btn small primary" id="banner-resume">Resume interview</button>`;
      $("#banner-resume").addEventListener("click", () => projectAction("resume-spec"));
    } else {
      banner.innerHTML = `<span>The spec is approved${res.exists ? "" : " (the conversation from before a server restart isn't kept, but SPEC.md is)"}.</span><a class="btn small" href="#/p/${encodeURIComponent(id)}/artifacts">Read SPEC.md</a>`;
    }
  } else banner.hidden = true;
  if (!res.exists && !res.messages.length && !log.children.length && S.detail && S.detail.project.interview) {
    for (const qa of S.detail.project.interview) {
      for (const [role, text] of [["agent", qa.q], ["user", qa.a]]) {
        const div = document.createElement("div");
        div.className = "msg " + role;
        div.textContent = text;
        log.appendChild(div);
      }
    }
  }
  if (res.messages.length && (nearBottom || S.chat.after === res.messages[res.messages.length - 1].id)) log.scrollTop = log.scrollHeight;
  if (res.waiting && document.activeElement !== $("#chat-input") && window.innerWidth > 860) $("#chat-input").focus();
}

// ---------- live log ----------

function renderLog() {
  $("#p-body").innerHTML = `<div class="toolbar"><label class="check" style="margin:0"><input id="log-follow" type="checkbox" ${S.log.follow ? "checked" : ""}> Follow</label>
    <span class="muted">Build output from <code>.factory/run.log</code>. Updates every few seconds.</span></div>
    <pre id="log" class="log"></pre>`;
  S.log.offset = -1;
  $("#log-follow").addEventListener("change", (e) => (S.log.follow = e.target.checked));
  pollLog();
}

function colorLine(l) {
  const e = esc(l);
  if (/✔|passed|complete/.test(l)) return `<span class="l-ok">${e}</span>`;
  if (/✖|failed|error|rejected|blocked/i.test(l)) return `<span class="l-bad">${e}</span>`;
  if (/▶|■|=====|acceptance round|planning/.test(l)) return `<span class="l-run">${e}</span>`;
  if (/^\[\d\d:\d\d:\d\d\]\s{3}/.test(l)) return `<span class="l-dim">${e}</span>`;
  return e;
}

async function pollLog() {
  const id = S.route.id;
  let res;
  try {
    res = await api(`projects/${encodeURIComponent(id)}/log?offset=${S.log.offset}`);
  } catch (e) {
    return;
  }
  const pre = $("#log");
  if (!pre || S.route.id !== id || S.route.tab !== "log") return;
  if (S.log.offset === -1) {
    pre.innerHTML = res.text ? "" : `<span class="l-dim">No build output yet. Start the build and it will stream here.</span>`;
  }
  if (res.text) pre.insertAdjacentHTML("beforeend", res.text.split("\n").map(colorLine).join("\n"));
  S.log.offset = res.offset;
  if (S.log.follow) pre.scrollTop = pre.scrollHeight;
}

// ---------- artifacts ----------

const GROUP_LABEL = { project: "Project", plan: "Spec & plan", roundtables: "Roundtables", tasks: "Task attempts", acceptance: "Acceptance" };

async function renderArtifacts() {
  $("#p-body").innerHTML = `<div class="split"><div class="card file-list" id="files"><p class="muted">Loading…</p></div><div class="card viewer" id="viewer"><p class="muted">Pick a file. Roundtable transcripts show every panelist's position in each round and the moderator's decision.</p></div></div>`;
  let res;
  try {
    res = await api(`projects/${encodeURIComponent(S.route.id)}/artifacts`);
  } catch (e) {
    $("#files").textContent = e.message;
    return;
  }
  S.artifacts = res.artifacts;
  const groups = {};
  res.artifacts.forEach((a) => (groups[a.group] = groups[a.group] || []).push(a));
  $("#files").innerHTML = Object.keys(GROUP_LABEL).filter((g) => groups[g]).map((g) =>
    `<div class="file-group">${GROUP_LABEL[g]} (${groups[g].length})</div>` +
    groups[g].map((a) => `<a class="file ${S.artifact === a.path ? "active" : ""}" href="#" data-path="${esc(a.path)}" title="${esc(a.path)}">${esc(a.path.split("/").pop())}</a>`).join("")
  ).join("") || '<p class="muted">Nothing yet.</p>';
  $$("#files [data-path]").forEach((el) => el.addEventListener("click", (ev) => {
    ev.preventDefault();
    openArtifact(el.dataset.path);
  }));
  const initial = S.artifact && res.artifacts.some((a) => a.path === S.artifact) ? S.artifact : res.artifacts.length ? res.artifacts[0].path : null;
  if (initial) openArtifact(initial);
}

async function openArtifact(path) {
  S.artifact = path;
  $$("#files .file").forEach((el) => el.classList.toggle("active", el.dataset.path === path));
  const v = $("#viewer");
  v.innerHTML = `<p class="muted">Loading…</p>`;
  try {
    const res = await api(`projects/${encodeURIComponent(S.route.id)}/artifact?path=${encodeURIComponent(path)}`);
    let body;
    if (/\.md$/.test(path)) body = md(res.content);
    else if (/\.json$/.test(path)) {
      let txt = res.content;
      try { txt = JSON.stringify(JSON.parse(txt), null, 2); } catch (_) {}
      body = `<pre class="log" style="height:auto;max-height:70vh">${esc(txt)}</pre>`;
    } else body = `<pre class="log" style="height:auto;max-height:70vh">${esc(res.content)}</pre>`;
    v.innerHTML = `<div class="toolbar"><strong class="mono">${esc(path)}</strong>${res.truncated ? pill("blocked", "truncated") : ""}</div>${body}`;
    if (window.innerWidth <= 860) v.scrollIntoView({ behavior: "smooth" });
  } catch (e) {
    v.innerHTML = `<p class="error-text">${esc(e.message)}</p>`;
  }
}

// ---------- polling ----------

async function refresh(force = false) {
  try {
    const res = await api("projects");
    S.projects = res.projects;
    $("#workspace").textContent = "workspace: " + res.workspace;
    renderSidebar();
  } catch (e) {
    return;
  }
  if (S.route.view === "project") {
    await loadProject();
    if (force && S.route.tab === "interview") pollChat();
  }
}

async function tick() {
  if (document.hidden || !$("#login").hidden) return;
  S.tick++;
  const r = S.route;
  if (r.view === "project") {
    if (r.tab === "interview") pollChat();
    if (r.tab === "log") pollLog();
    if (S.tick % 2 === 0) refresh();
  } else if (S.tick % 3 === 0) {
    refresh();
  }
}

async function boot() {
  try {
    const s = await fetch("api/session", { credentials: "same-origin" }).then((r) => r.json());
    $("#logout-btn").hidden = !s.auth_required;
    if (s.auth_required && !s.authenticated) showLogin();
  } catch (e) {
    toast("Cannot reach the factory server", true);
  }
  route();
  await refresh();
  if (S.route.view === "home" && !location.hash && S.projects.length === 0) renderHome();
  setInterval(tick, 1500);
  document.addEventListener("visibilitychange", () => !document.hidden && refresh());
}

boot();
