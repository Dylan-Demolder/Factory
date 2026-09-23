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
  if (!res.ok) {
    const err = new Error(data.error || res.statusText);
    // PUT /api/config reports validation failures as [{field, message}].
    if (Array.isArray(data.fields)) err.fields = data.fields;
    // The status lets callers tell a 409 race from a hard failure.
    err.status = res.status;
    throw err;
  }
  return data;
}

function toast(msg, bad = false, undo = null) {
  const t = $("#toast");
  t.textContent = msg;
  if (undo) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "btn ghost small";
    b.textContent = "Undo";
    b.addEventListener("click", () => {
      t.hidden = true;
      undo();
    });
    t.appendChild(b);
  }
  t.className = "toast" + (bad ? " bad" : "");
  t.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => (t.hidden = true), bad ? 6000 : undo ? 9000 : 3000);
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
$("#project-list").addEventListener("click", onProjectListClick);

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
      // The row is a wrapper (not the <a> itself) so the delete button can be a
      // real sibling button — a button inside an anchor would be invalid HTML.
      return `<div class="project-row">
        <a class="project-item ${p.id === cur ? "active" : ""}" href="#/p/${encodeURIComponent(p.id)}">
          <div class="row">${indicator}<span class="name">${esc(p.name)}</span>${phasePill(p)}</div>
          <div class="meta">${esc(meta)}${meta ? " · " : ""}${esc(ago(p.updated))}</div>
          ${p.total ? `<div class="bar" style="margin-top:6px"><span style="width:${pct(p.done, p.total)}%"></span></div>` : ""}
        </a>
        <button type="button" class="project-del" data-act="del-project" data-id="${esc(p.id)}" title="Delete ${esc(p.name)}" aria-label="Delete project ${esc(p.name)}">🗑</button>
      </div>`;
    })
    .join("") + `<a class="project-item only-mobile" href="#/agents"><span class="name">Agents & health</span></a>`;
}

// ---------- destructive confirmation (project delete) ----------
// factory's rule: nothing irreversible happens without a human saying yes.
// y/n keys like the terminal, Esc and backdrop cancel, focus starts on Cancel
// so a stray Enter can never be the destructive one.

let DIALOG = null;

const findProject = (id) => S.projects.find((p) => p.id === id);

function onProjectListClick(ev) {
  const btn = ev.target && ev.target.closest && ev.target.closest("[data-act='del-project']");
  if (btn) confirmDeleteProject(btn.dataset.id);
}

function closeConfirmDialog() {
  if (DIALOG && DIALOG.keyHandler) document.removeEventListener("keydown", DIALOG.keyHandler, true);
  DIALOG = null;
  const d = $("#app-dialog");
  if (d) d.remove();
}

function setDialogBusy(busy) {
  const wrap = $("#app-dialog");
  if (!wrap || !DIALOG) return;
  wrap.querySelectorAll("button").forEach((b) => (b.disabled = busy));
  const ok = wrap.querySelector("[data-dlg='ok']");
  if (ok) ok.textContent = busy ? DIALOG.busyLabel : DIALOG.okLabel;
}

function runDialogConfirm() {
  const d = DIALOG;
  if (!d || d.busy) return;
  d.busy = true;
  setDialogBusy(true);
  Promise.resolve()
    .then(() => d.onConfirm())
    .catch((e) => {
      closeConfirmDialog();
      toast((e && e.message) || "Something went wrong", true);
    })
    .then(() => {
      if (DIALOG === d) { d.busy = false; setDialogBusy(false); } // flow always closes on purpose; safety net
    });
}

function openConfirmDialog(opts) {
  closeConfirmDialog();
  const wrap = document.createElement("div");
  wrap.className = "overlay app-dialog";
  wrap.id = "app-dialog";
  wrap.innerHTML = `<div class="card dialog-card" role="alertdialog" aria-modal="true" aria-labelledby="app-dialog-title" aria-describedby="app-dialog-body">
    <h2 id="app-dialog-title">${esc(opts.title)}</h2>
    <div class="dlg-body" id="app-dialog-body">${opts.body}</div>
    <div class="dlg-keys muted">Press y to confirm · n or Esc to cancel</div>
    <div class="form-actions dlg-actions">
      <button type="button" class="btn" data-dlg="cancel">Cancel</button>
      <button type="button" class="btn danger" data-dlg="ok">${esc(opts.okLabel)}</button>
    </div>
  </div>`;
  document.body.appendChild(wrap);
  DIALOG = { onConfirm: opts.onConfirm, okLabel: opts.okLabel, busyLabel: opts.busyLabel || "Working…", busy: false };
  wrap.addEventListener("click", (ev) => { if (ev.target === wrap) closeConfirmDialog(); }); // backdrop cancels
  wrap.querySelector("[data-dlg='cancel']").addEventListener("click", closeConfirmDialog);
  wrap.querySelector("[data-dlg='ok']").addEventListener("click", runDialogConfirm);
  const keys = Array.from(wrap.querySelectorAll("button"));
  const keyHandler = (ev) => {
    if (!DIALOG) return;
    ev.stopPropagation(); // the modal owns the keyboard until it closes
    if (ev.key === "Escape" || ev.key === "n" || ev.key === "N") { ev.preventDefault(); closeConfirmDialog(); return; }
    if (ev.key === "y" || ev.key === "Y") { ev.preventDefault(); runDialogConfirm(); return; }
    if (ev.key === "Tab") {
      ev.preventDefault(); // keep focus inside the two buttons
      const i = keys.indexOf(document.activeElement);
      keys[ev.shiftKey ? (i <= 0 ? keys.length - 1 : i - 1) : (i + 1) % keys.length].focus();
    }
  };
  DIALOG.keyHandler = keyHandler;
  document.addEventListener("keydown", keyHandler, true);
  keys[0].focus(); // Cancel first: Enter is safe by default
}

function confirmDeleteProject(id) {
  const p = findProject(id);
  if (!p) return;
  const running = !!p.running;
  const body =
    `<p><strong>${esc(p.name)}</strong>${p.dir ? ` <code>${esc(p.dir)}</code>` : ""}</p>` +
    (running ? `<p class="dlg-warn">⚠ Its build is running — deleting stops the build too.</p>` : "") +
    `<p>This permanently removes the project's files and git history. It cannot be undone.</p>`;
  openConfirmDialog({
    title: `Delete “${p.name}”?`,
    body,
    okLabel: running ? "Stop build & delete" : "Delete project",
    busyLabel: running ? "Stopping…" : "Deleting…",
    onConfirm: () => performProjectDelete(id, running),
  });
}

// The race is real: a build can start between render and click, so the server
// answers 409. Turn that into a second, explicit stop-and-delete step — never
// a raw error.
function openBuildRunningDialog(id, serverMsg) {
  const p = findProject(id);
  const name = (p && p.name) || id;
  openConfirmDialog({
    title: `“${name}” is still building`,
    body: `<p>Its build started while you were deciding, so factory left the project untouched.</p>
      <p class="muted">${esc(serverMsg)}</p>
      <p>Stopping the build and deleting it removes the project's files and git history anyway.</p>`,
    okLabel: "Stop build & delete",
    busyLabel: "Stopping…",
    onConfirm: () => performProjectDelete(id, true),
  });
}

async function performProjectDelete(id, force) {
  try {
    const res = await api(`projects/${encodeURIComponent(id)}${force ? "?force=true" : ""}`, { method: "DELETE" });
    closeConfirmDialog();
    applyProjectDeleted(id, force, res);
  } catch (e) {
    if (e && e.status === 409) {
      closeConfirmDialog();
      openBuildRunningDialog(id, e.message);
      return;
    }
    closeConfirmDialog();
    toast((e && e.message) || "Couldn't delete the project", true); // state untouched
  }
}

// Success: the row leaves state, anything referring to the project is cleared,
// and an open main pane navigates back to the list — the UI must never keep
// pointing at a directory that no longer exists.
function applyProjectDeleted(id, stoppedBuild, res) {
  const p = findProject(id);
  const name = (p && p.name) || (res && res.id) || id;
  const wasOpen = S.route.view === "project" && S.route.id === id;
  S.projects = S.projects.filter((x) => x.id !== id);
  if (S.detail && (!S.detail.summary || S.detail.summary.id === id)) S.detail = null;
  if (S.chat.id === id) S.chat = { id: null, after: 0, waiting: false, active: false, exists: false };
  if (S.log.id === id) S.log = { id: null, offset: -1, follow: true };
  if (wasOpen) {
    S.openTask = null;
    S.artifacts = [];
    S.artifact = null;
    location.hash = "#/"; // hashchange → route() → sidebar + home, never the dead path
  } else {
    renderSidebar();
  }
  toast(stoppedBuild ? `Build stopped · “${name}” deleted` : `“${name}” deleted — files and git history removed`);
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

// ---------- agents (org chart editor) ----------

const ROLE_KEYS = ["moderator", "interviewer", "planner", "builder", "reviewer"];
const ROLE_DEPT = { moderator: "exec", interviewer: "spec", planner: "plan", builder: "eng", reviewer: "qa" };
const DEPTS = [
  { dept: "exec", role: "moderator", title: "You & Control Plane", desc: "Runs the pipeline, reports to you and owns the controls." },
  { dept: "spec", role: "interviewer", title: "Spec & Product", desc: "Interviews you and writes the specification." },
  { dept: "plan", role: "planner", title: "Planning & Architecture", desc: "Turns the approved spec into tasks and a design." },
  { dept: "eng", role: "builder", title: "Engineering", desc: "Writes the code and runs the tests." },
  { dept: "qa", role: "reviewer", title: "Quality & Review", desc: "Reviews the diffs and rejects fake tests." },
];
const PANEL_DEPT = { dept: "panel", title: "Roundtable", desc: "Ordered seats debating the spec, the plan and every task." };
const POOL_DEPT = { dept: "pool", title: "Unassigned", desc: "No job yet — drag a card into a department." };
const DEPT_OPTIONS = [
  ["", "— not set —"],
  ["exec", "exec — You & Control Plane"],
  ["spec", "spec — Spec & Product"],
  ["plan", "plan — Planning & Architecture"],
  ["eng", "eng — Engineering"],
  ["qa", "qa — Quality & Review"],
  ["panel", "panel — Roundtable"],
];
const DRAG_THRESHOLD = 6; // px of press-and-move before a drag starts; below it, a click opens the drawer

const AG = {
  loaded: false, bodyEl: null, keyBound: false,
  path: "", editable: true,
  base: null, cfg: null, baseMeta: null, meta: null, baseSecret: null, secret: {},
  baseSnap: "", undo: [], health: {},
  drawer: undefined, draft: null, returnFocus: null,
  drag: null, suppressCardUntil: 0,
  // Per-role model pins (config key role_models) plus their display data.
  effective: {},                                            // effective_models from GET /api/config (read-only)
  modelSrc: { state: "idle", sources: [], warnings: [] },   // GET /api/models, fetched once and cached here
  modelText: {},                                            // data-field path → true while its "Custom…" free-text box is open
  modelTyping: null,                                        // role mid-typing burst, so a burst is one undo entry
};

const clone = (o) => JSON.parse(JSON.stringify(o));
const arrText = (a) => (Array.isArray(a) ? a.join("\n") : a ? String(a) : "");
const arrSplit = (s) => String(s || "").split("\n").map((x) => x.trim()).filter(Boolean);
const baseName = (p) => { const s = String(p || ""); return s.split("/").pop().split("\\").pop(); };
const cap = (s) => (s ? s.charAt(0).toUpperCase() + s.slice(1) : s);

// Mirrors config.SecretEnv on the server, so a value the user just typed is
// masked the same way the next GET would withhold it.
function isSecretEnv(key, value) {
  const u = String(key || "").toUpperCase();
  if (u.endsWith("_ENV")) return false;
  for (const m of ["SECRET", "PASSWORD", "PASSWD", "TOKEN", "API_KEY", "PRIVATE_KEY", "CREDENTIAL"]) if (u.includes(m)) return true;
  if (u.endsWith("_KEY") || u === "KEY") return true;
  const v = String(value || "");
  return ["qaml_live_", "sk-", "sk_live_", "ghp_", "gho_", "github_pat_", "AKIA", "xoxb-", "xoxp-", "glpat-"].some((p) => v.startsWith(p));
}

// ---------- state ----------

function normaliseCfg(raw) {
  const c = clone(raw);
  delete c.effective_models; // derived by GET /api/config; never part of PUT /api/config
  if (c.agents && !Array.isArray(c.agents)) c.agents = Object.entries(c.agents).map(([name, a]) => ({ name, ...(a || {}) }));
  c.agents = (c.agents || []).map((a) => ({ ...(a || {}), name: a && a.name ? String(a.name) : "", env: (a && a.env) || {}, args: (a && a.args) || [] }));
  c.roles = { interviewer: "", planner: "", builder: "", reviewer: "", moderator: "", ...(c.roles || {}) };
  for (const k of ROLE_KEYS) c.roles[k] = c.roles[k] || "";
  // role_models is optional: an old config has no key (or null) and simply
  // means "every seat runs its agent's model". Only real pins survive here —
  // an empty or unknown override would be a validation error on save.
  const rm = c.role_models && typeof c.role_models === "object" ? c.role_models : {};
  c.role_models = {};
  for (const k of ROLE_KEYS) if (typeof rm[k] === "string" && rm[k].trim()) c.role_models[k] = rm[k];
  const rt = { ...(c.roundtable || {}) };
  rt.rounds = Number(rt.rounds) > 0 ? Math.floor(Number(rt.rounds)) : 2;
  rt.on_spec = rt.on_spec !== false;
  rt.on_plan = rt.on_plan !== false;
  rt.on_tasks = rt.on_tasks !== false;
  rt.participants = (rt.participants || []).map((p) => ({ agent: (p && p.agent) || "", persona: (p && p.persona) || "" }));
  c.roundtable = rt;
  c.limits = { ...(c.limits || {}) };
  c.test_command = c.test_command || "";
  c.notify_command = c.notify_command || "";
  c.editable = c.editable !== false;
  return c;
}

function metaFrom(cfg) {
  const m = {};
  for (const a of cfg.agents) m[a.name] = { title: "", dept: "", tier: "", notes: "", readonly: false, ...(a.meta || {}) };
  return m;
}

function secretFrom(cfg) {
  const s = {};
  for (const a of cfg.agents) {
    const m = {};
    for (const [k, v] of Object.entries(a.env || {})) if (v === null) m[k] = "keep";
    if (Object.keys(m).length) s[a.name] = m;
  }
  return s;
}

// keepUI: re-adopt while the drawer/undo stack are live (after a save).
function adoptCfg(raw, keepUI = false) {
  const drawer = AG.drawer, draft = AG.draft, undo = AG.undo, ret = AG.returnFocus;
  const c = normaliseCfg(raw);
  const eff = raw && raw.effective_models && typeof raw.effective_models === "object" ? raw.effective_models : {};
  AG.effective = { ...eff };
  AG.path = c.path || "";
  AG.editable = c.editable !== false;
  AG.cfg = c;
  AG.base = clone(c);
  AG.meta = metaFrom(c);
  AG.baseMeta = clone(AG.meta);
  AG.secret = secretFrom(c);
  AG.baseSecret = clone(AG.secret);
  AG.loaded = true;
  AG.baseSnap = snap();
  if (keepUI) {
    AG.drawer = drawer; AG.draft = draft; AG.undo = undo; AG.returnFocus = ret;
    if (typeof AG.drawer === "string" && !findAgent(AG.drawer)) AG.drawer = undefined;
  } else {
    AG.undo = []; AG.drawer = undefined; AG.draft = null; AG.returnFocus = null;
    AG.modelText = {}; AG.modelTyping = null;
  }
}

const snap = () => JSON.stringify({ c: AG.cfg, m: AG.meta, s: AG.secret });
const isDirty = () => !!AG.cfg && snap() !== AG.baseSnap;

function pushUndo(label) {
  AG.modelTyping = null; // any other mutation ends the current typing burst
  AG.undo.push({ label, cfg: clone(AG.cfg), meta: clone(AG.meta), secret: clone(AG.secret) });
  if (AG.undo.length > 60) AG.undo.shift();
}

function undoLast() {
  const e = AG.undo.pop();
  if (!e) { toast("Nothing to undo"); return; }
  AG.cfg = e.cfg; AG.meta = e.meta; AG.secret = e.secret;
  AG.modelTyping = null;
  if (typeof AG.drawer === "string" && !findAgent(AG.drawer)) { AG.drawer = undefined; AG.draft = null; }
  redrawAgents();
  toast("Undone: " + e.label);
}

const findAgent = (name) => (AG.cfg ? AG.cfg.agents.find((a) => a.name === name) : null);
const rolesHeld = (name) => (AG.cfg ? Object.entries(AG.cfg.roles).filter(([, v]) => v === name).map(([k]) => k) : []);
const panelIndex = (name) => (AG.cfg ? AG.cfg.roundtable.participants.findIndex((p) => p.agent === name) : -1);
const metaOf = (name) => AG.meta[name] || (AG.meta[name] = { title: "", dept: "", tier: "", notes: "", readonly: false });
const newSeat = (name) => ({ agent: name, persona: (AG.meta[name] && AG.meta[name].title) || name });

// ---------- field paths (data-field ↔ state) ----------
// Agent names may contain dots, so an agents.<name>.* path is matched against
// the configured names (longest first) instead of split blindly.

function matchAgentIn(list, parts) {
  for (let end = parts.length - 1; end >= 1; end--) {
    const nm = parts.slice(1, end + 1).join(".");
    const a = (list || []).find((x) => x && x.name === nm);
    if (a) return { agent: a, rest: parts.slice(end + 1) };
  }
  return null;
}

function baseGet(path) {
  const parts = path.split(".");
  if (!AG.base) return undefined;
  if (parts[0] === "agents") {
    const m = matchAgentIn(AG.base.agents, parts);
    if (!m) return undefined;
    let cur = m.agent;
    for (const p of m.rest) {
      if (cur == null) return undefined;
      cur = Array.isArray(cur) ? (/^\d+$/.test(p) ? cur[Number(p)] : undefined) : cur[p];
    }
    return cur;
  }
  if (parts[0] === "meta") {
    const key = parts[parts.length - 1];
    const m = AG.baseMeta[parts.slice(1, -1).join(".")];
    return m ? m[key] : undefined;
  }
  let cur = AG.base;
  for (const p of parts) {
    if (cur == null) return undefined;
    if (Array.isArray(cur)) {
      const byName = cur.find((x) => x && x.name === p);
      cur = byName !== undefined ? byName : /^\d+$/.test(p) ? cur[Number(p)] : undefined;
    } else cur = cur[p];
  }
  return cur;
}

function displayValue(path) {
  const v = baseGet(path);
  if (v === null || v === undefined) return "";
  if (Array.isArray(v)) return v.join("\n");
  if (typeof v === "boolean") return v ? "true" : "false";
  return String(v);
}

const initialFor = (path, current) => (path.startsWith("draft.") ? String(current ?? "") : displayValue(path));

function fieldInput(path) {
  return $$("#agents-body [data-field]").find((el) => el.dataset.field === path) || null;
}

const fldId = (path) => "f-" + String(path).replace(/[^A-Za-z0-9]+/g, "-");

function fld(path, label, value, opts = {}) {
  const id = fldId(path);
  const init = initialFor(path, value);
  return `<div class="field"><label for="${id}">${esc(label)}</label>
    <input id="${id}" type="${opts.type || "text"}"${opts.type === "number" ? ' min="0" step="1"' : ""} data-field="${esc(path)}" data-initial="${esc(init)}" value="${esc(value ?? "")}" placeholder="${esc(opts.ph || "")}" autocomplete="off" ${opts.attrs || ""} ${opts.dis || ""}>
    <div class="ferr" hidden></div>${opts.hint ? `<div class="hint">${opts.hint}</div>` : ""}</div>`;
}

function sld(path, value, options, opts = {}) {
  const id = fldId(path);
  const init = initialFor(path, value);
  return `<div class="field${opts.cls ? " " + opts.cls : ""}">${opts.nolabel ? "" : `<label for="${id}">${esc(opts.label || "")}</label>`}
    <select id="${id}" data-field="${esc(path)}" data-initial="${esc(init)}"${opts.aria ? ` aria-label="${esc(opts.aria)}"` : ""} ${opts.dis || ""}>
      ${options.map(([v, t]) => `<option value="${esc(v)}" ${String(v) === String(value) ? "selected" : ""}>${esc(t)}</option>`).join("")}
    </select>
    <div class="ferr" hidden></div>${opts.hint ? `<div class="hint">${opts.hint}</div>` : ""}</div>`;
}

function ta(path, label, value, opts = {}) {
  const id = fldId(path);
  const init = initialFor(path, value);
  return `<div class="field"><label for="${id}">${esc(label)}</label>
    <textarea id="${id}" rows="${opts.rows || 3}" data-field="${esc(path)}" data-initial="${esc(init)}" placeholder="${esc(opts.ph || "")}" ${opts.dis || ""}>${esc(value ?? "")}</textarea>
    <div class="ferr" hidden></div>${opts.hint ? `<div class="hint">${opts.hint}</div>` : ""}</div>`;
}

function chk(path, value, label, dis) {
  const id = fldId(path);
  return `<div class="field"><label class="check" for="${id}"><input id="${id}" type="checkbox" data-field="${esc(path)}" data-initial="${esc(initialFor(path, value))}" ${value ? "checked" : ""} ${dis || ""}> ${esc(label)}</label><div class="ferr" hidden></div></div>`;
}

function withRestoredFocus(el, fn) {
  const a = document.activeElement;
  const inside = a && a !== document.body && el.contains(a);
  let sel = null, caret = null;
  if (inside && a.id) sel = "#" + a.id;
  else if (inside && a.dataset && a.dataset.field) sel = `[data-field="${String(a.dataset.field).replace(/["\\]/g, "\\$&")}"]`;
  // Text fields keep their caret too: a re-render mid-edit must not jump the
  // insertion point to the end of the value.
  if (inside && /^(INPUT|TEXTAREA)$/.test(a.tagName)) {
    try { caret = [a.selectionStart, a.selectionEnd]; } catch (_) { caret = null; }
  }
  fn();
  if (sel) {
    const cands = Array.from(el.querySelectorAll(sel));
    let t = cands[0] || null;
    // Prefer the same kind of control: a revealed custom-id box must not hand
    // focus back to the select that sits in front of it.
    if (a && /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName)) t = cands.find((n) => n.tagName === a.tagName) || t;
    if (t) {
      try {
        t.focus();
        if (caret && caret[0] != null && /^(INPUT|TEXTAREA)$/.test(t.tagName)) t.setSelectionRange(caret[0], caret[1]);
      } catch (_) {}
    }
  }
}

// ---------- page ----------

async function renderAgents() {
  if (AG.drag) cleanupDrag(AG.drag);
  $("#main").innerHTML = `<h1>Agents</h1><p class="muted">Loaded from your factory config. New projects snapshot this config when they're created. Drag a card onto a department to hand over that job — or click a card to edit the agent.</p>
    <div id="agents-banner" class="oc-banner" hidden></div>
    <div id="agents-body" class="muted">Loading…</div>`;
  let cfg;
  try {
    cfg = await api("config");
  } catch (e) {
    $("#agents-body").textContent = e.message;
    return;
  }
  if (cfg.error) {
    $("#agents-body").innerHTML = `<div class="card"><p class="error-text">${esc(cfg.error)}</p>${
      Array.isArray(cfg.fields) && cfg.fields.length ? `<ul class="gaps">${cfg.fields.map((f) => `<li>${esc((f.field ? f.field + ": " : "") + (f.message || ""))}</li>`).join("")}</ul>` : ""
    }<p class="muted">Create one with <code>factory init</code> and restart the server, or pass <code>--config</code>.</p></div>`;
    return;
  }
  bindAgentsOnce();
  if (!AG.loaded || !isDirty()) adoptCfg(cfg);
  // The seat pickers need GET /api/models. Fire and forget: the editor renders
  // straight away (controls degrade to free text) and the canvas redraws when
  // the list lands. Cached in AG.modelSrc, so it is fetched once per session.
  loadModelSources();
  renderAgentsUI();
}

function renderAgentsUI() {
  const adv = $("#oc-adv");
  const advOpen = adv ? adv.open : false;
  const body = $("#agents-body");
  if (!body) return;
  body.className = "";
  body.innerHTML = `
    <div class="toolbar oc-topbar">
      <div><h2 style="margin:0">Org chart</h2><div class="muted" style="font-size:12.5px">Each department is one role in the pipeline. Drag cards between zones, or click one to edit it.</div></div>
      <div class="oc-topbar-right">
        <span class="muted mono">${esc(AG.path)}</span>
        ${AG.editable ? `<button type="button" class="btn small" data-act="new-agent">+ Add agent</button>` : `<span class="pill">read-only</span>`}
      </div>
    </div>
    <div id="oc-model-warns" class="oc-model-warns"${(AG.modelSrc.warnings || []).length ? "" : " hidden"}>${modelWarningsInner()}</div>
    <div class="oc-grid" id="oc-canvas"></div>
    <details class="card oc-adv" id="oc-adv"${advOpen ? " open" : ""}><summary>Advanced — roundtable settings, limits and commands</summary><div id="oc-adv-body"></div></details>
    <div class="card"><div class="toolbar"><h2 style="margin:0">Health</h2><button type="button" class="btn primary" data-act="doctor">Check all agents</button><span class="muted" style="margin-left:auto">Sends each agent a one-word prompt.</span></div><div id="oc-health"></div></div>
    <div id="oc-savebar" class="oc-savebar" hidden><span class="oc-savebar-msg"></span><button type="button" class="btn" data-act="revert">Revert</button><button type="button" class="btn primary" id="oc-save" data-act="save">Save</button></div>
    <div id="oc-drawer" class="oc-drawer" tabindex="-1" hidden></div>`;
  bindAgentsBody();
  redrawAgents();
}

function redrawAgents() {
  if (!AG.cfg) return;
  AG.modelTyping = null; // a full rebuild ends any typing burst on a model box
  renderCanvas();
  renderAdvanced();
  renderHealth();
  renderDrawer();
  syncBar();
}

// ---------- canvas ----------

function renderCanvas() {
  const el = $("#oc-canvas");
  if (!el || !AG.cfg) return;
  withRestoredFocus(el, () => {
    el.innerHTML = DEPTS.map(zoneHTML).join("") + panelZoneHTML() + poolZoneHTML();
  });
}

// ---------- per-role model pins (config key role_models) ----------
// A role is a seat; role_models pins the model that seat runs regardless of
// which agent fills it. Absent key = the seat inherits its agent's model.

const roleOverride = (role) => (AG.cfg && AG.cfg.role_models ? AG.cfg.role_models[role] || "" : "");

// The model a seat actually runs: its pin if it has one, otherwise its agent's
// model — which is exactly what GET /api/config reports as
// effective_models[role]. The agent is read live so a seat whose occupant (or
// whose agent's model) just changed never shows a stale value; the server's
// effective_models covers a seat that cannot be resolved locally at all.
function effectiveModelFor(role) {
  const ov = roleOverride(role);
  if (ov) return ov;
  const a = findAgent(AG.cfg.roles[role]);
  if (a) return a.model || "";
  return (AG.effective && AG.effective[role]) || "";
}

// camelStream serves a fleet: the only value it accepts is `auto`, so a pin on
// one of its seats would be stored and then silently ignored by the endpoint.
function isCamelAgent(name) {
  const a = findAgent(name);
  if (!a) return false;
  if (String(a.name || "").startsWith("camel")) return true;
  return String((a.env || {}).CAMEL_BASE_URL || "").includes("stream.camelai.com");
}

// A command agent only receives a pinned model through {{model}} in its args.
// Without the placeholder factory rejects the save with field
// role_models.<role> — detect it here so the seat can say so before Save.
function seatNeedsPlaceholder(role) {
  const a = findAgent(AG.cfg.roles[role]);
  if (!a || a.type !== "command") return false;
  return !(Array.isArray(a.args) ? a.args : []).some((x) => String(x).includes("{{model}}"));
}

// ---- sources: GET /api/models, fetched once and cached in AG.modelSrc ----

const MODEL_CUSTOM = "__custom__"; // the one select option that reveals the free-text id box

function allModelSources() {
  return AG.modelSrc && Array.isArray(AG.modelSrc.sources) ? AG.modelSrc.sources : [];
}
const pickSources = () => allModelSources().filter((s) => s && s.kind === "pick" && Array.isArray(s.models) && s.models.length);
const textModelSources = () => allModelSources().filter((s) => s && s.kind === "text");
const pickModelCount = () => pickSources().reduce((n, s) => n + s.models.length, 0);
const pickHasModel = (id) => pickSources().some((s) => s.models.includes(id));

// A dropdown is only worth showing when the fetch succeeded and it carries at
// least one model. A failed or empty response degrades every control to a
// single text input rather than an empty select that can never be chosen from.
function modelSourcesUsable() {
  return AG.modelSrc.state === "ok" && pickModelCount() > 0;
}

// Custom mode for one field path (keyed by data-field): the "Custom…" option
// was chosen, or the value in hand is an id no list offers (hand-edited
// config) — either way the free-text box belongs open.
const modelCustomActive = (path, value) => AG.modelText[path] === true || (!!value && !pickHasModel(value));

// The one model picker, shared by the agent drawer and the role seats: a
// <select> of every enumerated model grouped by its source label, plus a final
// "Custom… (type an id)" option that reveals a text input — typing is never
// the default, only the escape hatch for ids no list can enumerate (Claude,
// codex). Returns { select, input, custom, fallback }.
function modelPickerHTML(path, value, opts = {}) {
  const dis = opts.dis || "";
  const init = opts.init !== undefined ? opts.init : displayValue(path);
  const aria = esc(opts.aria || "Model");
  const id = fldId(path);
  const mkInput = (withId) =>
    `<input${withId ? ` id="${id}"` : ""} type="text" data-field="${esc(path)}" data-initial="${esc(init)}" value="${esc(value || "")}" placeholder="${esc(
      opts.ph || "type a model id"
    )}" autocomplete="off" spellcheck="false" aria-label="${aria}" ${dis}>`;
  if (!modelSourcesUsable()) return { select: mkInput(true), input: "", custom: false, fallback: true };
  // camel seats may never type an id — they cannot be pinned at all — so they
  // get the same list but no Custom… dead end, just their own value if no list
  // carries it.
  const custom = opts.noCustom ? !!value && !pickHasModel(value) : modelCustomActive(path, value);
  let html = "";
  if (opts.emptyLabel) {
    html += `<option value=""${value === "" && !custom ? " selected" : ""}>${esc(opts.emptyLabel)}</option>`;
  } else if (opts.requireValue && value === "" && !custom) {
    // openai's own model is required by schema: never offer an empty choice,
    // only a disabled placeholder while nothing is picked yet.
    html += `<option value="" disabled selected>— choose a model —</option>`;
  }
  const selVal = custom ? (opts.noCustom ? String(value) : MODEL_CUSTOM) : String(value || "");
  for (const s of pickSources()) {
    html += `<optgroup label="${esc(s.label)}">${s.models.map((m) => `<option value="${esc(m)}"${m === selVal ? " selected" : ""}>${esc(m)}</option>`).join("")}</optgroup>`;
  }
  if (custom) {
    html += opts.noCustom
      ? `<option value="${esc(String(value))}" selected>${esc(String(value))}</option>`
      : `<option value="${MODEL_CUSTOM}" selected>Custom… (type an id)</option>`;
  } else if (!opts.noCustom) {
    html += `<option value="${MODEL_CUSTOM}">Custom… (type an id)</option>`;
  }
  const selInit = custom && !opts.noCustom ? MODEL_CUSTOM : init;
  const select = `<select id="${id}" data-field="${esc(path)}" data-initial="${esc(selInit)}" aria-label="${aria}" ${dis}>${html}</select>`;
  const input = custom && !opts.noCustom ? mkInput(false) : "";
  return { select, input, custom, fallback: false };
}

// The escape hatch has to say which flag the typed id rides on.
function modelTextHintsInner() {
  const hinted = textModelSources().filter((s) => s.hint);
  if (hinted.length) {
    return `Type any id the CLI accepts: ${hinted
      .map((s) => `${esc(s.label)} <code${s.note ? ` title="${esc(s.note)}"` : ""}>${esc(s.hint)}</code>`)
      .join(" · ")}`;
  }
  return textModelSources().length ? "Type any model id the CLI accepts." : "";
}

// After a redraw, put the caret back in the custom-id box that was revealed.
function focusModelCustom(path) {
  const el = $$("#agents-body [data-field]").find((e) => e.tagName === "INPUT" && e.dataset.field === path);
  if (el) { try { el.focus(); } catch (_) {} }
}

async function loadModelSources() {
  if (AG.modelSrc.state !== "idle") return;
  AG.modelSrc = { state: "loading", sources: [], warnings: [] };
  let next;
  try {
    const res = await api("models");
    next = {
      state: "ok",
      sources: Array.isArray(res && res.sources) ? res.sources : [],
      warnings: Array.isArray(res && res.warnings) ? res.warnings : [],
    };
  } catch (e) {
    // A failed or hostile response degrades every control to free text.
    next = { state: "failed", sources: [], warnings: [] };
  }
  AG.modelSrc = next;
  if (S.route.view === "agents" && $("#oc-canvas")) {
    renderModelWarnings();
    redrawAgents();
  }
}

function retryModelSources() {
  if (AG.modelSrc.state !== "failed") return;
  AG.modelSrc = { state: "idle", sources: [], warnings: [] };
  loadModelSources();
  renderModelWarnings();
  if (S.route.view === "agents" && $("#oc-canvas")) redrawAgents();
}

function modelWarningsInner() {
  return ((AG.modelSrc && AG.modelSrc.warnings) || []).map((x) => `<div>⚠ ${esc(x)}</div>`).join("");
}

function renderModelWarnings() {
  const el = $("#oc-model-warns");
  if (!el) return;
  el.innerHTML = modelWarningsInner();
  el.hidden = !((AG.modelSrc && AG.modelSrc.warnings) || []).length;
}

function roleModelHeadInner(role) {
  const pinned = !!roleOverride(role);
  return `<span class="oc-eyebrow">Model</span><span class="oc-model-chip${pinned ? " pinned" : ""}" title="${
    pinned ? "Pinned for this seat" : "No pin: this seat runs its agent's own model"
  }">${pinned ? "pinned" : "from agent"}</span>`;
}

function roleModelClearBtn(role, camel) {
  if (camel || !AG.editable) return "";
  const agent = findAgent(AG.cfg.roles[role]);
  const own = (agent && agent.model) || "";
  const title = agent
    ? `Remove the pin — this seat falls back to ${agent.name}'s own model${own ? ` (${own})` : ""}; the agent's own model is never touched`
    : "Remove the pin — this seat falls back to its agent's model";
  return `<button type="button" class="icon-btn oc-model-x" data-act="role-model-clear" data-role="${esc(role)}" title="${esc(title)}" aria-label="${esc(title)}"${roleOverride(role) ? "" : " hidden"}>×</button>`;
}

function roleModelControlInner(role, camel) {
  const path = `role_models.${role}`;
  const ov = roleOverride(role);
  const eff = effectiveModelFor(role);
  // camel seats refuse editing outright — even on an editable config — because
  // a pin there would be stored and then ignored by the endpoint.
  const dis = AG.editable && !camel ? "" : "disabled";
  // Same dropdown the agent drawer uses — grouped by source label — plus the
  // seat's own empty choice ("inherited from agent") in front of it.
  const pick = modelPickerHTML(path, ov, {
    dis,
    aria: `Model for the ${role} seat`,
    init: displayValue(path),
    emptyLabel: eff ? `from agent — ${eff}` : "from agent (inherited)",
    noCustom: camel,
    ph: eff ? `from agent: ${eff}` : "type a model id",
  });
  return `${pick.select}${roleModelClearBtn(role, camel)}${pick.input}<div class="ferr" hidden></div>`;
}

function roleModelNotesInner(role, camel) {
  const out = [];
  const src = AG.modelSrc;
  const path = `role_models.${role}`;
  const ov = roleOverride(role);
  const agent = findAgent(AG.cfg.roles[role]);
  if (camel) {
    const camelSrc = (src.sources || []).find((s) => s && s.id === "camel");
    const note =
      (camelSrc && camelSrc.note) ||
      "Serves a fleet, so only `auto` is accepted — a pinned model is not available on a standard subscription.";
    out.push(`<div>Editing is off: ${esc(note)}</div>`);
    if (ov) out.push(`<div class="oc-warn">⚠ This seat still pins <code>${esc(ov)}</code>, which camelStream ignores — reassign the seat to another agent to remove it.</div>`);
    return out.join("");
  }
  if (src.state === "loading") out.push(`<div>Loading model list…</div>`);
  else if (src.state === "failed")
    out.push(
      `<div>Model list unavailable — type any model id.</div><div><button type="button" class="btn small ghost" data-act="models-retry">Retry</button></div>`
    );
  else if (src.state === "ok" && !modelSourcesUsable()) out.push(`<div>No models are offered for this seat — type a model id.</div>`);
  // Hints belong wherever typing is what is happening: the revealed Custom…
  // box, or the lone input shown when there is no list to pick from.
  const typingHere = !camel && (!modelSourcesUsable() || modelCustomActive(path, ov));
  if (typingHere && src.state === "ok") {
    const hints = modelTextHintsInner();
    if (hints) out.push(`<div>${hints}</div>`);
  }
  if (agent && seatNeedsPlaceholder(role)) {
    out.push(
      ov
        ? `<div class="oc-warn">⚠ <code>${esc(agent.name)}</code> is a command agent: factory only passes a pinned model through <code>{{model}}</code> in its Arguments — add the placeholder in the agent's drawer, or this save is rejected.</div>`
        : `<div>A pin on this seat needs <code>{{model}}</code> in ${esc(agent.name)}'s Arguments.</div>`
    );
  }
  if (ov && agent && agent.type === "openai") {
    out.push(
      `<div>Removing the pin only unpins this seat — <code>${esc(agent.name)}</code>'s own model (<code>${esc(agent.model || "")}</code>) is required by its config and stays.</div>`
    );
  }
  return out.join("");
}

function roleModelHTML(role) {
  const camel = isCamelAgent(AG.cfg.roles[role]);
  return `<div class="oc-model" data-role-model="${esc(role)}">
    <div class="oc-model-head">${roleModelHeadInner(role)}</div>
    <div class="oc-model-row">${roleModelControlInner(role, camel)}</div>
    <div class="oc-model-notes">${roleModelNotesInner(role, camel)}</div>
  </div>`;
}

// Update a model row in place after a keystroke: the input has to keep focus
// and caret, so only the chrome around it is rebuilt.
function refreshModelRow(role) {
  const root = $(`#oc-canvas .oc-model[data-role-model="${role}"]`);
  if (!root) return;
  const head = $(".oc-model-head", root);
  if (head) head.innerHTML = roleModelHeadInner(role);
  const notes = $(".oc-model-notes", root);
  if (notes) notes.innerHTML = roleModelNotesInner(role, isCamelAgent(AG.cfg.roles[role]));
  const clear = $("[data-act='role-model-clear']", root);
  if (clear) clear.hidden = !roleOverride(role);
}

// Write a seat's control back into the config. Removal always omits the key:
// role_models.<role> = "" is a validation error ("model is empty"), so an empty
// value must mean "no pin", never an empty override.
function setRoleModel(role, el) {
  const path = `role_models.${role}`;
  const isSelect = el.tagName === "SELECT";
  let next = String(el.value == null ? "" : el.value);
  if (!isSelect) next = next.trim();
  const cur = roleOverride(role);
  if (isSelect && next === MODEL_CUSTOM) {
    // Reveal the free-text box. No config change yet, so no undo entry.
    if (AG.modelText[path] !== true) {
      AG.modelText[path] = true;
      AG.modelTyping = null;
      redrawAgents();
      focusModelCustom(path);
    }
    return;
  }
  if (isSelect) {
    const wasCustom = AG.modelText[path] === true;
    AG.modelText[path] = false;
    if (next === cur) {
      if (wasCustom) redrawAgents(); // stepped off Custom… back onto the current value
      return;
    }
    pushUndo(`${role} model → ${next || "from agent"}`);
    if (next) AG.cfg.role_models[role] = next;
    else delete AG.cfg.role_models[role];
    redrawAgents();
    return;
  }
  // The Custom… box (or the lone input when there is no list): one undo entry
  // per burst — the first keystroke snapshots the config as it was, later
  // keystrokes ride along until the field is left (change) or anything else
  // pushes an entry.
  if (next === cur) return;
  if (AG.modelTyping !== role) {
    pushUndo(`${role} model → ${next || "from agent"}`);
    AG.modelTyping = role;
  }
  if (next) AG.cfg.role_models[role] = next;
  else delete AG.cfg.role_models[role];
  if (!next && AG.modelText[path] === true) {
    // An empty pin is not a pin: close the box and show the inherited view.
    AG.modelText[path] = false;
    redrawAgents();
    return;
  }
  refreshModelRow(role);
}

function clearRoleModel(role) {
  if (!AG.editable || !ROLE_KEYS.includes(role)) return;
  if (!roleOverride(role)) return;
  pushUndo(`clear ${role} model pin`);
  delete AG.cfg.role_models[role]; // delete the key — never write ""
  AG.modelText[`role_models.${role}`] = false;
  redrawAgents();
  toast(`Model pin removed from the ${role} seat`, false, undoLast);
  const el = fieldInput(`role_models.${role}`);
  if (el) { try { el.focus(); } catch (_) {} }
}

function zoneHTML(d) {
  const cur = AG.cfg.roles[d.role];
  const has = cur && findAgent(cur);
  const cards = has
    ? cardHTML(cur, { role: d.role })
    : `<div class="oc-empty"><span>${cur ? `⚠ <code>${esc(cur)}</code> isn't a configured agent.` : `No ${esc(d.role)} yet.`}</span><span>${
        AG.cfg.agents.length ? "Pick one in the dropdown, or drag a card in." : "Add an agent first."
      }</span></div>`;
  return `<section class="oc-zone" data-drop-kind="role" data-drop-role="${d.role}">
    <div class="oc-zone-head">
      <div class="oc-zone-main">
        <div class="oc-eyebrow">${esc(d.dept)}</div>
        <div class="oc-zone-title">${esc(d.title)}</div>
        <div class="oc-zone-desc">${esc(d.desc)}</div>
      </div>
      ${sld(`roles.${d.role}`, cur || "", [["", "— unassigned —"], ...AG.cfg.agents.map((a) => [a.name, a.name])], {
        nolabel: true, aria: `Assign the ${d.role}`, cls: "oc-assign", dis: AG.editable ? "" : "disabled",
      })}
    </div>
    ${roleModelHTML(d.role)}
    <div class="oc-cards">${cards}</div>
    <div class="oc-hint"></div>
  </section>`;
}

function panelZoneHTML() {
  const parts = AG.cfg.roundtable.participants;
  const dis = AG.editable ? "" : "disabled";
  const cands = AG.cfg.agents.filter((a) => panelIndex(a.name) < 0);
  const tools = cands.length
    ? `<div class="oc-zone-tools">
        <select id="oc-seat-cand" aria-label="Agent to seat"${dis ? " disabled" : ""}>
          <option value="">Seat an agent…</option>
          ${cands.map((a) => `<option value="${esc(a.name)}">${esc(a.name)}</option>`).join("")}
        </select>
        <button type="button" class="btn small" data-act="seat-add"${dis}>+ Seat</button>
      </div>`
    : AG.cfg.agents.length
      ? `<span class="muted" style="font-size:12.5px">every agent is seated</span>`
      : "";
  const body = parts.length
    ? parts.map((p, i) => seatHTML(p, i)).join("")
    : `<div class="oc-empty">${AG.cfg.agents.length ? "No seats yet — the moderator decides alone. Seat someone with the picker above, or drag a card here." : "No agents yet — add one first."}</div>`;
  return `<section class="oc-zone wide" data-drop-kind="panel">
    <div class="oc-zone-head">
      <div class="oc-zone-main">
        <div class="oc-eyebrow">${esc(PANEL_DEPT.dept)}</div>
        <div class="oc-zone-title">${esc(PANEL_DEPT.title)}</div>
        <div class="oc-zone-desc">${esc(PANEL_DEPT.desc)}</div>
      </div>
      ${tools}
    </div>
    <div class="oc-cards oc-seats">${body}</div>
    <div class="oc-hint"></div>
  </section>`;
}

function seatHTML(p, i) {
  const dis = AG.editable ? "" : "disabled";
  const known = findAgent(p.agent);
  const opts = [["", "(no agent)"], ...AG.cfg.agents.map((a) => [a.name, a.name])];
  if (p.agent && !known) opts.push([p.agent, p.agent + " (missing)"]);
  const inner = !p.agent
    ? `<div class="oc-empty">Empty seat — pick an agent above.</div>`
    : known
      ? cardHTML(p.agent, { seat: i })
      : `<div class="oc-empty"><span>⚠ <code>${esc(p.agent)}</code> isn't a configured agent.</span></div>`;
  return `<div class="oc-seat" data-drop-kind="seat" data-drop-index="${i}">
    <div class="oc-seat-head">
      <span class="oc-seat-no">Seat ${i + 1}</span>
      ${sld(`roundtable.participants.${i}.agent`, p.agent, opts, { nolabel: true, aria: `Agent in seat ${i + 1}`, dis })}
      <button type="button" class="icon-btn oc-seat-x" data-act="seat-del" data-i="${i}" title="Remove seat ${i + 1}" aria-label="Remove seat ${i + 1}"${dis}>×</button>
    </div>
    ${inner}
    <div class="oc-persona-wrap">
      <input class="oc-persona" type="text" data-field="${esc(`roundtable.participants.${i}.persona`)}" data-initial="${esc(displayValue(`roundtable.participants.${i}.persona`))}" value="${esc(p.persona)}" placeholder="Persona, e.g. pragmatic CTO" aria-label="Persona for seat ${i + 1}" ${dis}>
      <div class="ferr" hidden></div>
    </div>
    <div class="oc-hint"></div>
  </div>`;
}

function poolZoneHTML() {
  const list = AG.cfg.agents.filter((a) => !rolesHeld(a.name).length && panelIndex(a.name) < 0);
  const cards = list.length
    ? list.map((a) => cardHTML(a.name, {})).join("")
    : `<div class="oc-empty">${AG.cfg.agents.length ? "Every agent has a job or a seat." : "No agents yet."}${
        AG.editable ? ` <button type="button" class="btn small" data-act="new-agent">+ Add an agent</button>` : ""
      }</div>`;
  return `<section class="oc-zone wide" data-drop-kind="pool">
    <div class="oc-zone-head">
      <div class="oc-zone-main">
        <div class="oc-eyebrow">${esc(POOL_DEPT.dept)}</div>
        <div class="oc-zone-title">${esc(POOL_DEPT.title)}</div>
        <div class="oc-zone-desc">${esc(POOL_DEPT.desc)}</div>
      </div>
      ${AG.editable ? `<div class="oc-zone-tools"><button type="button" class="btn small" data-act="new-agent">+ Add agent</button></div>` : ""}
    </div>
    <div class="oc-cards">${cards}</div>
    <div class="oc-hint"></div>
  </section>`;
}

function engineLine(a) {
  if (!a) return "";
  if (a.type === "opencode") return `OpenCode Go · ${a.model || "default"}`;
  if (a.type === "openai") return a.model ? `OpenAI-compat · ${a.model}` : "OpenAI-compat";
  return baseName(a.command) || "no command set";
}

function cardHTML(name, opts = {}) {
  const a = findAgent(name);
  const m = metaOf(name);
  const held = rolesHeld(name);
  const title = m.title || (opts.role ? cap(opts.role) : name);
  // A pinned seat reads as `agent (model)` so a run that looks wrong can be
  // traced straight back to its configuration (the logs label it the same way).
  const pin = opts.role ? roleOverride(opts.role) : "";
  const shown = pin ? `${name} (${pin})` : name;
  const badges = [];
  if (opts.role) badges.push(pill(`d-${ROLE_DEPT[opts.role] || "panel"}`, opts.role));
  if (opts.seat !== undefined) held.forEach((r) => badges.push(pill(`d-${ROLE_DEPT[r] || "panel"}`, r)));
  if (panelIndex(name) >= 0 && opts.seat === undefined) badges.push(pill("d-panel", "panel"));
  if (m.dept) badges.push(`<span class="oc-dept-chip">${esc(m.dept)}</span>`);
  if (m.readonly) badges.push(pill("ro", "read-only"));
  const h = AG.health[name];
  const health = h
    ? h.checking
      ? `<span class="oc-hcard muted">checking…</span>`
      : h.ok
        ? `<span class="oc-hcard s-done" title="${esc(h.duration || "")}">✔ ${esc(h.duration || "")}</span>`
        : `<span class="oc-hcard s-blocked" title="${esc(h.error || "")}">✖ ${esc(h.error || "failed").slice(0, 80)}</span>`
    : "";
  return `<div class="oc-card" role="button" tabindex="0" data-agent="${esc(name)}" aria-label="Edit ${esc(name)}">
    <div class="oc-card-top"><span class="oc-card-title">${esc(title)}</span>${badges.join("")}</div>
    <div class="oc-card-name">${esc(shown)}</div>
    <div class="oc-card-engine">${esc(engineLine(a))}</div>
    ${health ? `<div class="oc-card-health">${health}</div>` : ""}
  </div>`;
}

// ---------- advanced + health ----------

function renderAdvanced() {
  const el = $("#oc-adv-body");
  if (!el || !AG.cfg) return;
  const dis = AG.editable ? "" : "disabled";
  const rt = AG.cfg.roundtable;
  withRestoredFocus(el, () => {
    el.innerHTML = `
      <h3 class="oc-sect">Roundtable settings</h3>
      <div class="oc-fields">
        ${fld("roundtable.rounds", "Rounds", String(rt.rounds), { type: "number", dis, hint: "Debate rounds per roundtable (spec, plan, tasks)." })}
        ${chk("roundtable.on_spec", rt.on_spec !== false, "Hold a roundtable on the spec", dis)}
        ${chk("roundtable.on_plan", rt.on_plan !== false, "…on the plan", dis)}
        ${chk("roundtable.on_tasks", rt.on_tasks !== false, "…on every task", dis)}
      </div>
      <h3 class="oc-sect">Commands</h3>
      <div class="oc-fields">
        ${fld("test_command", "Test command", AG.cfg.test_command || "", { dis, ph: "go test ./...", hint: "Run to verify a build; empty uses the pipeline default." })}
        ${fld("notify_command", "Notify command", AG.cfg.notify_command || "", { dis, ph: "notify-send …", hint: "Shell line run when a project needs your answer." })}
      </div>
      <h3 class="oc-sect">Limits</h3>
      <div class="oc-fields">
        ${Object.entries(AG.cfg.limits)
          .map(([k, v]) =>
            typeof v === "number"
              ? fld(`limits.${k}`, k.replace(/_/g, " "), String(v), { type: "number", dis })
              : fld(`limits.${k}`, k.replace(/_/g, " "), String(v ?? ""), { dis, hint: k.includes("timeout") ? "Go duration, e.g. <code>30m</code>." : "" })
          )
          .join("")}
      </div>`;
  });
}

function healthHTML(h) {
  if (!h) return `<span class="muted">—</span>`;
  if (h.checking) return `<span class="muted">checking…</span>`;
  if (h.ok) return `<span class="s-done">✔ ${esc(h.duration || "")}</span>`;
  return `<span class="s-blocked" title="${esc(h.error || "")}">✖ ${esc(h.error || "failed").slice(0, 120)}</span>`;
}

function renderHealth() {
  const el = $("#oc-health");
  if (!el || !AG.cfg) return;
  el.innerHTML = AG.cfg.agents.length
    ? AG.cfg.agents.map((a) => `<div class="oc-hrow"><span class="mono">${esc(a.name)}</span><span>${healthHTML(AG.health[a.name])}</span></div>`).join("")
    : `<p class="muted">No agents configured.</p>`;
}

async function runDoctor(btn) {
  if (btn) { btn.disabled = true; btn.textContent = "Checking…"; }
  AG.cfg.agents.forEach((a) => (AG.health[a.name] = { checking: true }));
  renderCanvas(); renderHealth();
  try {
    const res = await api("doctor", { method: "POST", body: {} });
    for (const r of res.results || []) AG.health[r.name] = { ok: !!r.ok, duration: r.duration || "", error: r.error || "" };
  } catch (e) {
    toast(e.message, true);
    for (const a of AG.cfg.agents) if (AG.health[a.name] && AG.health[a.name].checking) delete AG.health[a.name];
  }
  renderCanvas(); renderHealth();
  if (btn && document.contains(btn)) { btn.disabled = false; btn.textContent = "Check all agents"; }
}

// ---------- drawer ----------

function describePlacement(name) {
  const bits = rolesHeld(name).map((r) => {
    const d = DEPTS.find((x) => x.role === r);
    return `${d ? d.title : r} (${r})`;
  });
  const i = panelIndex(name);
  if (i >= 0) bits.push(`Roundtable seat ${i + 1}`);
  return bits.length ? "Currently: " + bits.join(" · ") : "Currently unassigned";
}

function placementHTML(name) {
  const seated = panelIndex(name);
  const held = rolesHeld(name);
  const opts = [];
  for (const d of DEPTS) if (!held.includes(d.role)) opts.push([`role:${d.role}`, `→ ${d.title} (${d.role})`]);
  AG.cfg.roundtable.participants.forEach((p, i) => {
    if (i !== seated) opts.push([`seat:${i}`, `→ Roundtable seat ${i + 1} (take this seat)`]);
  });
  if (seated < 0) opts.push(["panel", `→ Roundtable — ${AG.cfg.roundtable.participants.length ? `make them seat ${AG.cfg.roundtable.participants.length + 1}` : "give them a seat"}`]);
  if (held.length || seated >= 0) opts.push(["pool", "→ Unassigned (clear roles and seat)"]);
  return `<div class="oc-place">
    <div class="oc-eyebrow">Placement</div>
    <div class="oc-place-now">${esc(describePlacement(name))}</div>
    ${
      opts.length && AG.editable
        ? `<select id="oc-move" aria-label="Move to another place"><option value="">Move to…</option>${opts
            .map(([v, t]) => `<option value="${esc(v)}">${esc(t)}</option>`)
            .join("")}</select>`
        : ""
    }
    <div class="hint">${AG.editable ? "Drag the card on the chart, or pick a destination here." : "This config is read-only."}</div>
  </div>`;
}

function envRowsHTML(owner, env, isDraft) {
  const dis = AG.editable ? "" : "disabled";
  const p = isDraft ? "draft.env." : `agents.${owner}.env.`;
  const rows = Object.keys(env).map((k) => {
    const v = env[k];
    const masked = v === null;
    const cleared = !isDraft && (AG.secret[owner] || {})[k] === "clear";
    const shown = masked ? "" : String(v);
    const secretRow = masked || isSecretEnv(k, shown);
    const type = secretRow ? "password" : "text";
    const btnLabel = masked ? (cleared ? "Restore" : "Clear") : "Remove";
    return `<div class="oc-env-row">
      <span class="oc-env-key mono" title="${esc(k)}">${esc(k)}</span>
      <input type="${type}" data-field="${esc(p + k)}" data-initial="${esc(initialFor(p + k, shown))}" value="${esc(shown)}" placeholder="${masked ? "•••••••• (set)" : "value"}" autocomplete="off" spellcheck="false" aria-label="${esc(k)}" ${dis}>
      <button type="button" class="btn small ghost oc-env-btn" data-act="env-clear" data-k="${esc(k)}"${dis}>${btnLabel}</button>
      <div class="ferr" hidden></div>
      <div class="oc-clear-note"${cleared ? "" : " hidden"}>${esc(k)} will be removed when you save.</div>
    </div>`;
  }).join("");
  return `<h3 class="oc-sect">Environment</h3>
    <div class="oc-env">${rows || `<div class="hint">No variables.</div>`}</div>
    <button type="button" class="btn small" data-act="env-add"${dis}>+ Add variable</button>
    <div class="hint">Values marked <code>•••••••• (set)</code> are stored on disk and hidden here: leave them alone to keep them, type to replace, press Clear to remove.</div>`;
}

function typeFields(x, prefix, isDraft) {
  const dis = AG.editable ? "" : "disabled";
  const t = isDraft ? AG.draft.type : x.type;
  const argsTxt = isDraft ? String(x.args || "") : arrText(x.args);
  const env = x.env || {};
  const showEnv = t === "command" || Object.keys(env).length > 0;
  const timeout = fld(prefix + "timeout", "Timeout", x.timeout || "", { ph: "45m", dis, hint: "Go duration, e.g. <code>45m</code> or <code>90s</code>. Blank uses the default." });
  let h = "";
  if (t === "command") {
    h += fld(prefix + "command", "Command", x.command || "", { ph: "/usr/local/bin/mycli", dis, hint: "Executable that receives the prompt." });
    h += ta(prefix + "args", "Arguments", argsTxt, { rows: 3, dis, hint: "One per line. <code>{{prompt}}</code> is substituted; without it the prompt goes to stdin." });
    h += timeout;
  } else if (t === "openai") {
    h += fld(prefix + "base_url", "Base URL", x.base_url || "", { ph: "https://api.example.com/v1", dis });
    h += fld(prefix + "api_key_env", "API key env var", x.api_key_env || "", { ph: "OPENAI_API_KEY", dis, hint: "Name of the environment variable holding the key." });
    h += modelField(prefix + "model", x.model || "", "openai", dis);
    h += timeout;
  } else {
    h += modelField(prefix + "model", x.model || "", "opencode", dis);
    h += timeout;
  }
  if (!isDraft && x.readonly_args && x.readonly_args.length) {
    h += `<div class="hint">${x.readonly_args.length} read-only argument(s) come from the file and are preserved as-is.</div>`;
  }
  if (showEnv) h += envRowsHTML(isDraft ? null : x.name, env, isDraft);
  return h;
}

// The drawer's model field is the same picker the role seats use: a dropdown
// of the enumerated ids grouped by source, with one "Custom… (type an id)"
// escape hatch for ids no list can carry (Claude, codex). Typing is never the
// default — it appears only when Custom… is chosen or there is no list at all.
function modelField(path, value, type, dis) {
  const typing = modelSourcesUsable() ? modelCustomActive(path, value) : true;
  const pick = modelPickerHTML(path, value, {
    dis,
    aria: "Model",
    init: initialFor(path, value),
    // opencode's model is optional ("" = use the OpenCode default) and stays a
    // legitimate choice; openai's is required by schema, so it never offers an
    // empty option — just a placeholder until one is picked.
    emptyLabel: type === "opencode" ? "— default (none) —" : "",
    requireValue: type === "openai",
    ph: type === "openai" ? "e.g. gpt-4o-mini" : "default model id",
  });
  const base = type === "opencode" ? "“default (none)” uses the OpenCode default model." : "";
  const hints = typing ? modelTextHintsInner() : "";
  const hint = [base, hints].filter(Boolean).join("<br>");
  return `<div class="field"><label for="${fldId(path)}">Model</label>${pick.select}${pick.input}<div class="ferr" hidden></div>${
    hint ? `<div class="hint">${hint}</div>` : ""
  }</div>`;
}

function renderDrawer() {
  const d = $("#oc-drawer");
  if (!d) return;
  if (AG.drawer === undefined) { d.hidden = true; d.innerHTML = ""; return; }
  const isNew = AG.drawer === null;
  if (isNew && !AG.draft) { AG.drawer = undefined; d.hidden = true; d.innerHTML = ""; return; }
  const a = isNew ? null : findAgent(AG.drawer);
  if (!isNew && !a) { AG.drawer = undefined; AG.draft = null; d.hidden = true; d.innerHTML = ""; return; }
  const dis = AG.editable ? "" : "disabled";
  const prefix = isNew ? "draft." : `agents.${a.name}.`;
  const meta = isNew ? AG.draft.meta : metaOf(a.name);
  const metaPrefix = isNew ? "draft.meta." : `meta.${a.name}.`;
  const typeVal = isNew ? AG.draft.type : a.type;
  const head = `<div class="oc-drawer-head">
      <div>
        <div class="oc-eyebrow">${isNew ? "New agent" : "Agent"}</div>
        <div class="oc-drawer-title">${esc(isNew ? "Add an agent" : meta.title || a.name)}</div>
        ${isNew ? "" : `<div class="oc-card-name">${esc(a.name)}</div>`}
      </div>
      <button type="button" class="icon-btn" data-act="close" title="Close (Esc)" aria-label="Close">✕</button>
    </div>`;
  const connHead = typeVal === "command" ? "Command" : typeVal === "openai" ? "Endpoint" : "Engine";
  withRestoredFocus(d, () => {
    d.hidden = false;
    d.innerHTML = `${head}
      ${isNew ? "" : placementHTML(a.name)}
      <h3 class="oc-sect">Agent</h3>
      ${fld(prefix + "name", "Name", isNew ? AG.draft.name : a.name, {
        dis,
        attrs: isNew ? `pattern="[A-Za-z0-9][A-Za-z0-9._\\-]{0,63}"` : "readonly",
        hint: isNew ? "Letters, digits, dots, dashes or underscores. It becomes the key in the config." : "This is the key in the config — delete and re-add to rename.",
      })}
      ${sld(prefix + "type", typeVal, [
        ["opencode", "OpenCode Go — edits files in the project"],
        ["command", "Command — any CLI"],
        ["openai", "OpenAI-compatible API"],
      ], { label: "Type", dis })}
      <h3 class="oc-sect">${connHead}</h3>
      ${typeFields(isNew ? AG.draft : a, prefix, isNew)}
      <h3 class="oc-sect">Organization</h3>
      ${fld(metaPrefix + "title", "Title", meta.title || "", { ph: "e.g. Senior Engineer", dis, hint: "Shown on the card; also the default persona for a new roundtable seat." })}
      ${sld(metaPrefix + "dept", meta.dept || "", DEPT_OPTIONS, { label: "Department", dis })}
      ${fld(metaPrefix + "tier", "Tier", meta.tier || "", { ph: "standard", dis, hint: "Free-form label, e.g. standard or premium." })}
      ${ta(metaPrefix + "notes", "Notes", meta.notes || "", { rows: 3, ph: "What this agent is for, quirks, links…", dis })}
      ${chk(metaPrefix + "readonly", !!meta.readonly, "Read-only — never modify project files", dis)}
      <div class="form-actions">${
        isNew
          ? `<button type="button" class="btn primary" data-act="add"${dis}>Add agent</button><button type="button" class="btn ghost" data-act="close">Cancel</button>`
          : `<button type="button" class="btn danger" data-act="del"${dis}>Delete agent</button><button type="button" class="btn" data-act="new-agent"${dis}>+ Add agent</button>`
      }</div>`;
  });
}

function openDrawer(name) {
  AG.returnFocus = document.activeElement;
  AG.drawer = name;
  AG.draft = null;
  renderDrawer();
  const d = $("#oc-drawer");
  if (d) { d.scrollTop = 0; try { d.focus(); } catch (_) {} }
}

function openNewAgent() {
  if (!AG.editable) return;
  AG.returnFocus = document.activeElement;
  AG.drawer = null;
  AG.draft = { name: "", type: "opencode", model: "", timeout: "", command: "", args: "", base_url: "", api_key_env: "", env: {}, meta: { title: "", dept: "", tier: "", notes: "", readonly: false } };
  renderDrawer();
  const d = $("#oc-drawer");
  if (d) { d.scrollTop = 0; try { d.focus(); } catch (_) {} }
  const nameInput = fieldInput("draft.name");
  if (nameInput) nameInput.focus();
}

function closeDrawer() {
  const name = AG.drawer;
  AG.drawer = undefined;
  AG.draft = null;
  renderDrawer();
  const rf = AG.returnFocus;
  AG.returnFocus = null;
  let target = rf && rf !== document.body && document.contains(rf) ? rf : null;
  if (!target && typeof name === "string") {
    target = $$("#oc-canvas .oc-card").find((c) => c.dataset.agent === name) || null;
  }
  if (target) { try { target.focus(); } catch (_) {} }
}

function deleteAgent() {
  const name = AG.drawer;
  if (typeof name !== "string" || !findAgent(name)) return;
  const held = rolesHeld(name);
  const seat = panelIndex(name);
  let msg = `Delete ${name}?`;
  if (held.length) {
    msg += `\n\nIt fills role${held.length > 1 ? "s" : ""}: ${held.join(", ")}. Deleting ${held.length > 1 ? "leaves those roles empty" : "leaves that role empty"} until you assign another agent — the config will not validate while it is empty.`;
  }
  if (seat >= 0) msg += `\n\nIt holds roundtable seat ${seat + 1}, which will be removed (later seats shift up).`;
  msg += "\n\nNothing is written to disk until you press Save.";
  if (!confirm(msg)) return;
  pushUndo(`delete ${name}`);
  AG.cfg.agents = AG.cfg.agents.filter((x) => x.name !== name);
  held.forEach((r) => (AG.cfg.roles[r] = ""));
  AG.cfg.roundtable.participants = AG.cfg.roundtable.participants.filter((p) => p.agent !== name);
  delete AG.meta[name];
  delete AG.secret[name];
  AG.drawer = undefined;
  AG.draft = null;
  redrawAgents();
  toast(`Deleted ${name}`, false, undoLast);
}

function submitAddAgent() {
  const d = AG.draft;
  if (!d) return;
  const name = String(d.name || "").trim();
  const fail = (msg) => {
    const el = fieldInput("draft.name");
    if (el) showFieldError(el, msg);
  };
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(name)) return fail("Letters, digits, dots, dashes or underscores, starting with a letter or digit.");
  if (findAgent(name)) return fail(`An agent called ${name} already exists.`);
  clearFieldErrors();
  pushUndo(`add ${name}`);
  const a = { name, type: d.type };
  const wantsModel = d.type === "opencode" || d.type === "openai";
  if (wantsModel && d.model) a.model = d.model;
  if (d.type === "command" && d.command) a.command = d.command;
  if (d.type === "command" && d.args) a.args = arrSplit(d.args);
  if (d.type === "openai" && d.base_url) a.base_url = d.base_url;
  if (d.type === "openai" && d.api_key_env) a.api_key_env = d.api_key_env;
  if (d.timeout) a.timeout = d.timeout;
  if (Object.keys(d.env || {}).length) a.env = { ...d.env };
  AG.cfg.agents.push(a);
  AG.meta[name] = { ...d.meta };
  AG.secret[name] = {};
  AG.drawer = name;
  AG.draft = null;
  redrawAgents();
  toast(`${name} added — press Save to write the config`, false, undoLast);
}

function addEnvVar() {
  if (!AG.editable) return;
  const k = (prompt("Environment variable name", "") || "").trim();
  if (!k) return;
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(k)) { toast("Use letters, digits and underscores, not starting with a digit", true); return; }
  let path;
  if (AG.drawer === null) {
    if (!AG.draft) return;
    if (k in AG.draft.env) { toast(`${k} is already listed`, true); return; }
    pushUndo(`add env ${k}`);
    AG.draft.env[k] = "";
    path = `draft.env.${k}`;
  } else {
    const a = findAgent(AG.drawer);
    if (!a) return;
    if (k in a.env) { toast(`${k} is already listed`, true); return; }
    pushUndo(`add env ${k} to ${a.name}`);
    a.env[k] = "";
    path = `agents.${a.name}.env.${k}`;
  }
  renderDrawer();
  syncBar();
  const el = fieldInput(path);
  if (el) el.focus();
}

function toggleEnvClear(k) {
  if (!AG.editable) return;
  if (AG.drawer === null) {
    if (!AG.draft || !(k in AG.draft.env)) return;
    pushUndo(`remove env ${k}`);
    delete AG.draft.env[k];
    renderDrawer();
    syncBar();
    return;
  }
  const name = AG.drawer;
  const a = findAgent(name);
  if (!a || !(k in a.env)) return;
  if (a.env[k] === null) {
    const modes = (AG.secret[name] = AG.secret[name] || {});
    const clearing = modes[k] !== "clear";
    pushUndo(`${clearing ? "clear" : "keep"} env ${k}`);
    modes[k] = clearing ? "clear" : "keep";
  } else {
    pushUndo(`remove env ${k} from ${name}`);
    delete a.env[k];
    if (AG.secret[name]) delete AG.secret[name][k];
  }
  renderDrawer();
  syncBar();
  const btn = $$("#oc-drawer [data-act='env-clear']").find((b) => b.dataset.k === k);
  if (btn) { try { btn.focus(); } catch (_) {} }
}

function setEnvValue(a, key, v) {
  // A key that is withheld on this page (null) or has a pending clear decision
  // is a secret: emptying the box means "keep what is on disk", a typed value
  // replaces it — and either overrides a pending clear.
  const modes = AG.secret[a.name];
  const isSecretKey = a.env[key] === null || (modes && modes[key] !== undefined);
  if (isSecretKey) {
    const m = (AG.secret[a.name] = AG.secret[a.name] || {});
    a.env[key] = v === "" ? null : v;
    m[key] = "keep";
  } else {
    a.env[key] = v;
  }
}

function refreshEnvRow(el, a, key) {
  const row = el.closest && el.closest(".oc-env-row");
  if (!row) return;
  const btn = row.querySelector("[data-act='env-clear']");
  const note = row.querySelector(".oc-clear-note");
  const masked = a.env[key] === null;
  const cleared = ((AG.secret[a.name] || {})[key]) === "clear";
  if (btn) btn.textContent = masked ? (cleared ? "Restore" : "Clear") : "Remove";
  if (note) note.hidden = !cleared;
}

// ---------- placement mutations ----------

function planDrop(name, spec) {
  if (!AG.cfg) return { ok: false, why: "still loading" };
  const a = findAgent(name);
  if (!a) return { ok: false, why: "That agent no longer exists" };
  const q = `<code>${esc(name)}</code>`;
  if (spec.kind === "role") {
    if (!ROLE_KEYS.includes(spec.role)) return { ok: false, why: "No such role" };
    const cur = AG.cfg.roles[spec.role];
    if (cur === name) return { ok: false, why: `${name} already is the ${spec.role}` };
    if (spec.role === "builder" && a.type === "openai") return { ok: false, why: "factory forbids an openai-type builder — use an opencode or command agent" };
    const text = `Make ${name} the ${spec.role}` + (cur ? ` (replaces ${cur})` : "");
    const html = `Make ${q} the ${spec.role}` + (cur ? ` <span class="oc-hint-sub">(replaces ${esc(cur)})</span>` : "");
    return { ok: true, text, html };
  }
  if (spec.kind === "pool") {
    const held = rolesHeld(name), seat = panelIndex(name);
    if (!held.length && seat < 0) return { ok: false, why: `${name} is already unassigned` };
    return { ok: true, text: `Unassign ${name}`, html: `Unassign ${q}` };
  }
  if (spec.kind === "panel") {
    const seat = panelIndex(name);
    if (seat >= 0) return { ok: false, why: `${name} already sits at seat ${seat + 1} — drop on another seat to reorder` };
    const n = AG.cfg.roundtable.participants.length + 1;
    return {
      ok: true,
      text: `Add ${name} to the roundtable (seat ${n})`,
      html: `Add ${q} to the roundtable <span class="oc-hint-sub">(seat ${n})</span>`,
    };
  }
  if (spec.kind === "seat") {
    const i = Number(spec.index) || 0;
    const seat = panelIndex(name);
    if (seat === i) return { ok: false, why: `${name} already is in seat ${i + 1}` };
    const text = seat >= 0 ? `Move ${name} to seat ${i + 1}` : `Add ${name} to the roundtable (seat ${i + 1})`;
    const html = seat >= 0 ? `Move ${q} to <span class="oc-hint-sub">seat ${i + 1}</span>` : `Add ${q} to the roundtable <span class="oc-hint-sub">(seat ${i + 1})</span>`;
    return { ok: true, text, html };
  }
  return { ok: false, why: "Not a valid destination" };
}

function applyTarget(name, spec) {
  const plan = planDrop(name, spec);
  if (!plan.ok) { toast(plan.why, true); return; }
  pushUndo(plan.text);
  if (spec.kind === "role") {
    AG.cfg.roles[spec.role] = name;
  } else if (spec.kind === "pool") {
    rolesHeld(name).forEach((r) => (AG.cfg.roles[r] = ""));
    const i = panelIndex(name);
    if (i >= 0) AG.cfg.roundtable.participants.splice(i, 1);
  } else if (spec.kind === "panel") {
    AG.cfg.roundtable.participants.push(newSeat(name));
  } else if (spec.kind === "seat") {
    const list = AG.cfg.roundtable.participants;
    const i = Math.min(Number(spec.index) || 0, list.length);
    const j = panelIndex(name);
    const entry = j >= 0 ? list.splice(j, 1)[0] : newSeat(name);
    list.splice(Math.min(i, list.length), 0, entry);
  }
  redrawAgents();
  toast(plan.text, false, undoLast);
}

function addSeatFromPicker() {
  const sel = $("#oc-seat-cand");
  if (!sel || !sel.value) { toast("Pick an agent to seat first", true); return; }
  const name = sel.value;
  if (panelIndex(name) >= 0) { toast(`${name} already has a seat`, true); return; }
  pushUndo(`seat ${name}`);
  AG.cfg.roundtable.participants.push(newSeat(name));
  redrawAgents();
  toast(`${name} seated at seat ${AG.cfg.roundtable.participants.length}`, false, undoLast);
}

function removeSeat(i) {
  const p = AG.cfg.roundtable.participants[i];
  if (!p) return;
  pushUndo(`remove seat ${i + 1}`);
  AG.cfg.roundtable.participants.splice(i, 1);
  redrawAgents();
  toast(`Seat ${i + 1} removed${p.agent ? ` (${p.agent})` : ""}`, false, undoLast);
}

function applySeatAgent(i, name) {
  const parts = AG.cfg.roundtable.participants;
  const p = parts[i];
  if (!p || p.agent === name) return;
  if (name) {
    const j = panelIndex(name);
    if (j >= 0 && j !== i) {
      pushUndo(`swap seats ${i + 1} and ${j + 1}`);
      parts[j].agent = p.agent;
      p.agent = name;
      redrawAgents();
      return;
    }
  }
  pushUndo(`seat ${i + 1} → ${name || "empty"}`);
  p.agent = name;
  redrawAgents();
}

// ---------- save / revert / errors ----------

function envOut(name, a) {
  const out = {};
  const modes = AG.secret[name] || {};
  for (const [k, v] of Object.entries(a.env || {})) {
    // null = withheld on this page; the server inherits the value already on
    // disk. An intentionally cleared key is simply left out (that removes it).
    if (v === null && modes[k] === "clear") continue;
    out[k] = v;
  }
  return out;
}

function serializeCfg() {
  const c = clone(AG.cfg);
  delete c.path;
  delete c.editable;
  if ("notify" in c) c.notify = !!c.notify_command;
  const rounds = Math.floor(Number(c.roundtable.rounds));
  c.roundtable.rounds = rounds > 0 ? rounds : 2; // matches config.applyDefaults
  c.agents = c.agents.map((a) => {
    const o = { ...a };
    delete o.env_secret;
    o.meta = { ...(AG.meta[a.name] || metaOf(a.name)) };
    o.env = envOut(a.name, a);
    for (const k of ["model", "command", "base_url", "api_key_env", "timeout"]) if (!o[k]) delete o[k];
    if (!Array.isArray(o.args) || !o.args.length) delete o.args;
    if (!Array.isArray(o.readonly_args) || !o.readonly_args.length) delete o.readonly_args;
    if (!Object.keys(o.env).length) delete o.env;
    return o;
  });
  if (!c.test_command) delete c.test_command;
  if (!c.notify_command) delete c.notify_command;
  // role_models round-trips unchanged when nothing is pinned: with no pins
  // there is no key at all, exactly like an old config — never "" and never an
  // empty object. effective_models is derived by GET and unknown to PUT.
  delete c.effective_models;
  if (c.role_models && !Object.keys(c.role_models).length) delete c.role_models;
  return c;
}

// After a successful save the withheld/typed secrets have to be re-applied the
// way the next GET would present them: replacements are back on disk (masked
// again), cleared keys are gone.
function reconcileSecrets() {
  for (const name of Object.keys(AG.secret)) {
    const a = findAgent(name);
    const modes = AG.secret[name];
    if (!a) { delete AG.secret[name]; continue; }
    for (const k of Object.keys(modes)) {
      if (modes[k] === "clear") { delete a.env[k]; delete modes[k]; }
    }
    if (!Object.keys(modes).length) delete AG.secret[name];
  }
  for (const a of AG.cfg.agents) {
    for (const [k, v] of Object.entries(a.env || {})) {
      if (v !== null && isSecretEnv(k, v)) {
        a.env[k] = null;
        (AG.secret[a.name] = AG.secret[a.name] || {})[k] = "keep";
      }
    }
  }
}

async function saveConfig() {
  if (!AG.editable || !AG.cfg) return;
  clearFieldErrors();
  hideBanner();
  if (!isDirty()) { toast("Nothing to save"); return; }
  const btn = $("#oc-save");
  if (btn) btn.disabled = true;
  try {
    await api("config", { method: "PUT", body: { config: serializeCfg(), meta: clone(AG.meta) } });
    reconcileSecrets();
    AG.base = clone(AG.cfg);
    AG.baseMeta = clone(AG.meta);
    AG.baseSecret = clone(AG.secret);
    AG.baseSnap = snap();
    toast("Configuration saved");
    renderAgentsUI();
  } catch (e) {
    handleSaveError(e);
    const b = $("#oc-save");
    if (b) b.disabled = false;
  }
}

function revertConfig() {
  if (!isDirty()) return;
  pushUndo("revert");
  AG.cfg = clone(AG.base);
  AG.meta = clone(AG.baseMeta);
  AG.secret = clone(AG.baseSecret);
  if (typeof AG.drawer === "string" && !findAgent(AG.drawer)) { AG.drawer = undefined; AG.draft = null; }
  hideBanner();
  clearFieldErrors();
  renderAgentsUI();
  toast("Reverted to the saved configuration", false, undoLast);
}

function showFieldError(el, msg) {
  el.classList.add("bad");
  el.setAttribute("aria-invalid", "true");
  const box = el.parentElement && el.parentElement.querySelector(".ferr");
  if (box) { box.textContent = msg; box.hidden = false; }
}

function clearFieldError(el) {
  el.classList.remove("bad");
  el.removeAttribute("aria-invalid");
  const box = el.parentElement && el.parentElement.querySelector(".ferr");
  if (box) { box.hidden = true; box.textContent = ""; }
}

function clearFieldErrors() {
  $$("#agents-body .ferr").forEach((b) => { b.hidden = true; b.textContent = ""; });
  $$("#agents-body [data-field].bad").forEach((el) => {
    el.classList.remove("bad");
    el.removeAttribute("aria-invalid");
  });
}

function showBanner(summary, list) {
  const b = $("#agents-banner");
  if (!b) return;
  b.innerHTML = `<strong>Couldn't save the configuration.</strong><div class="oc-banner-sum">${esc(summary || "")}</div>` +
    (list && list.length ? `<ul>${list.map((x) => `<li>${esc(x)}</li>`).join("")}</ul>` : "");
  b.hidden = false;
}

function hideBanner() {
  const b = $("#agents-banner");
  if (b) { b.hidden = true; b.innerHTML = ""; }
}

function handleSaveError(e) {
  clearFieldErrors();
  const fields = Array.isArray(e.fields) ? e.fields : [];
  // Errors about one agent's fields are shown in that agent's drawer: open it
  // when it is shut and nothing else (a draft, another agent) would be lost.
  if (AG.drawer === undefined && !AG.draft) {
    for (const f of fields) {
      const p = f && f.field ? String(f.field) : "";
      if (!p.startsWith("agents.")) continue;
      const m = matchAgentIn(AG.cfg.agents, p.split("."));
      if (m && m.rest.length) { openDrawer(m.agent.name); break; }
    }
  }
  const unmapped = [];
  let first = null;
  for (const f of fields) {
    const path = f && f.field ? String(f.field) : "";
    const msg = (f && f.message) || "is invalid";
    const els = path
      ? $$("#agents-body [data-field]").filter((el) => el.dataset.field === path || (path.includes(".") && el.dataset.field.startsWith(path + ".")))
      : [];
    if (els.length) {
      els.forEach((el) => showFieldError(el, msg));
      if (!first) first = els[0];
    } else {
      unmapped.push(path ? `${path}: ${msg}` : msg);
    }
  }
  showBanner(e.message || "the server rejected the document", unmapped);
  toast("Save failed — your changes are kept", true);
  if (first) {
    first.scrollIntoView({ block: "center", behavior: "smooth" });
    try { first.focus(); } catch (_) {}
  }
}

// ---------- dirty tracking ----------

function markFieldDirty(el) {
  const cur = el.type === "checkbox" ? String(el.checked) : el.value;
  el.classList.toggle("dirty", cur !== (el.dataset.initial ?? ""));
}

function syncBar() {
  const bar = $("#oc-savebar");
  if (!bar || !AG.cfg) return;
  const dirty = isDirty();
  bar.hidden = !dirty || !AG.editable;
  const empty = ROLE_KEYS.filter((k) => !AG.cfg.roles[k]);
  const msg = bar.querySelector(".oc-savebar-msg");
  if (msg) {
    msg.innerHTML =
      (empty.length ? `<span class="oc-warn">⚠ ${esc(empty.join(", "))} ${empty.length > 1 ? "roles are" : "role is"} empty</span>` : "") +
      `<span class="oc-save-note">${dirty ? "Unsaved changes" : ""}</span>`;
  }
  $$("#agents-body [data-field]").forEach(markFieldDirty);
}

// ---------- input events ----------

function applyPath(path, el) {
  const parts = path.split(".");
  const val = el.type === "checkbox" ? el.checked : el.value;
  if (path === "test_command" || path === "notify_command") { AG.cfg[path] = String(val); return; }

  if (parts[0] === "draft") {
    const d = AG.draft;
    if (!d) return;
    if (parts[1] === "meta") {
      const key = parts[parts.length - 1];
      d.meta[key] = key === "readonly" ? !!val : String(val);
    } else if (parts[1] === "env") {
      d.env[parts.slice(2).join(".")] = String(val);
    } else if (parts[1] === "type") {
      if (d.type !== val) { d.type = String(val); renderDrawer(); }
    } else if (parts[1] === "args") {
      d.args = String(val);
    } else if (parts[1] === "model") {
      if (String(val) === MODEL_CUSTOM) {
        AG.modelText["draft.model"] = true;
        renderDrawer();
        focusModelCustom("draft.model");
        return;
      }
      if (el.tagName === "SELECT") { AG.modelText["draft.model"] = false; d.model = String(val); renderDrawer(); }
      else d.model = String(val);
      return;
    } else if (parts[1] !== "name") {
      d[parts[1]] = String(val);
    } else {
      d.name = String(val);
    }
    return;
  }

  if (parts[0] === "meta") {
    const key = parts[parts.length - 1];
    const m = metaOf(parts.slice(1, -1).join("."));
    m[key] = key === "readonly" ? !!val : String(val);
    return;
  }

  if (parts[0] === "roles") {
    const role = parts[1];
    if (!ROLE_KEYS.includes(role)) return;
    const next = String(val);
    if (AG.cfg.roles[role] === next) return;
    pushUndo(`${role} → ${next || "unassigned"}`);
    AG.cfg.roles[role] = next;
    redrawAgents();
    return;
  }

  if (parts[0] === "role_models") {
    const role = parts[1];
    if (!ROLE_KEYS.includes(role)) return;
    setRoleModel(role, el);
    return;
  }

  if (parts[0] === "agents") {
    const m = matchAgentIn(AG.cfg.agents, parts);
    if (!m) return;
    const a = m.agent;
    const key = m.rest[0];
    if (!key || key === "name") return;
    if (key === "env") {
      const envKey = m.rest.slice(1).join(".");
      setEnvValue(a, envKey, String(val));
      refreshEnvRow(el, a, envKey);
      renderCanvas(); // CAMEL_BASE_URL decides how the seat's model control renders
      return;
    }
    if (key === "type") {
      if (a.type !== val) { a.type = String(val); renderDrawer(); renderCanvas(); }
      return;
    }
    if (key === "args") {
      a.args = arrSplit(String(val));
      renderCanvas(); // {{model}} in args decides whether a pin is valid
      return;
    }
    if (key === "model") {
      const p = `agents.${a.name}.model`;
      if (String(val) === MODEL_CUSTOM) {
        AG.modelText[p] = true;
        renderDrawer();
        focusModelCustom(p);
        return;
      }
      if (el.tagName === "SELECT") { AG.modelText[p] = false; renderDrawer(); }
      a.model = String(val);
      renderCanvas(); // the seats this agent fills inherit this model
      return;
    }
    a[key] = String(val);
    return;
  }

  if (parts[0] === "roundtable") {
    const rt = AG.cfg.roundtable;
    if (parts[1] === "participants") {
      const i = Number(parts[2]);
      const p = rt.participants[i];
      if (!p) return;
      if (parts[3] === "agent") { applySeatAgent(i, String(val)); return; }
      p.persona = String(val);
      return;
    }
    if (parts[1] === "rounds") {
      const n = Number(val);
      rt.rounds = Number.isFinite(n) ? Math.max(0, Math.floor(n)) : 0;
      return;
    }
    if (parts[1] === "on_spec" || parts[1] === "on_plan" || parts[1] === "on_tasks") {
      rt[parts[1]] = !!val;
      return;
    }
    return;
  }

  if (parts[0] === "limits") {
    const k = parts[1];
    AG.cfg.limits[k] = typeof AG.base.limits[k] === "number" ? (Number.isFinite(Number(val)) ? Number(val) : 0) : String(val);
  }
}

function handleMove(el) {
  const v = el.value;
  el.value = "";
  if (!v) return;
  const name = typeof AG.drawer === "string" ? AG.drawer : null;
  if (!name) return;
  let spec = null;
  if (v.startsWith("role:") && ROLE_KEYS.includes(v.slice(5))) spec = { kind: "role", role: v.slice(5) };
  else if (v === "pool") spec = { kind: "pool" };
  else if (v === "panel") spec = { kind: "panel" };
  else if (v.startsWith("seat:")) spec = { kind: "seat", index: Number(v.slice(5)) };
  if (spec) applyTarget(name, spec);
}

function onFieldEvent(ev) {
  const el = ev.target;
  if (!el || !el.dataset) return;
  if (el.id === "oc-move") { handleMove(el); return; }
  const path = el.dataset.field;
  if (!path || !AG.cfg) return;
  if (!AG.editable) return;
  clearFieldError(el);
  applyPath(path, el);
  if (ev.type === "change") AG.modelTyping = null; // leaving a model box ends its undo burst
  markFieldDirty(el);
  syncBar();
}

function handleAct(el) {
  const act = el.dataset.act;
  if (act === "close") return closeDrawer();
  // Reading the model list is not an edit, so read-only configs may retry it.
  if (!AG.editable && act !== "doctor" && act !== "models-retry") { toast("This config is read-only", true); return; }
  if (act === "save") return saveConfig();
  if (act === "revert") return revertConfig();
  if (act === "new-agent") return openNewAgent();
  if (act === "add") return submitAddAgent();
  if (act === "del") return deleteAgent();
  if (act === "doctor") return runDoctor(el);
  if (act === "seat-add") return addSeatFromPicker();
  if (act === "seat-del") return removeSeat(Number(el.dataset.i));
  if (act === "env-add") return addEnvVar();
  if (act === "env-clear") return toggleEnvClear(el.dataset.k);
  if (act === "role-model-clear") return clearRoleModel(el.dataset.role);
  if (act === "models-retry") return retryModelSources();
}

function onBodyClick(ev) {
  const actEl = ev.target.closest && ev.target.closest("[data-act]");
  if (actEl) { handleAct(actEl); return; }
  const card = ev.target.closest && ev.target.closest(".oc-card");
  if (card && card.dataset.agent) {
    if (Date.now() < AG.suppressCardUntil) return; // the click that ends a drag must not open the drawer
    openDrawer(card.dataset.agent);
  }
}

function onBodyKeydown(ev) {
  const card = ev.target.closest && ev.target.closest(".oc-card");
  if (!card || !card.dataset.agent) return;
  if (ev.key !== "Enter" && ev.key !== " ") return;
  ev.preventDefault();
  if (Date.now() < AG.suppressCardUntil) return;
  openDrawer(card.dataset.agent);
}

// ---------- drag and drop (Pointer Events) ----------

function blockScroll(ev) {
  if (AG.drag && AG.drag.active) ev.preventDefault();
}

function specOfTarget(el) {
  const kind = el.dataset.dropKind;
  if (kind === "seat") return { kind, index: Number(el.dataset.dropIndex) };
  if (kind === "role") return { kind, role: el.dataset.dropRole };
  return { kind };
}

function onBodyPointerDown(ev) {
  if (ev.pointerType === "mouse" && ev.button !== 0) return;
  const card = ev.target.closest && ev.target.closest(".oc-card");
  if (!card || !card.dataset.agent) return;
  if (AG.drag) {
    if (!document.contains(AG.drag.card)) cleanupDrag(AG.drag); // stale drag from a re-render
    else return;
  }
  if (!AG.editable) return;
  const drag = { name: card.dataset.agent, card, pid: ev.pointerId, x0: ev.clientX, y0: ev.clientY, active: false, targets: [], hit: null, cleanup: null, ghost: null };
  AG.drag = drag;
  const onMove = (e) => cardMove(e, drag);
  const onUp = (e) => cardUp(e, drag, false);
  const onCancel = (e) => cardUp(e, drag, true);
  drag.cleanup = () => {
    window.removeEventListener("pointermove", onMove);
    window.removeEventListener("pointerup", onUp);
    window.removeEventListener("pointercancel", onCancel);
    try { card.releasePointerCapture(drag.pid); } catch (_) {}
  };
  window.addEventListener("pointermove", onMove);
  window.addEventListener("pointerup", onUp);
  window.addEventListener("pointercancel", onCancel);
  try { card.setPointerCapture(ev.pointerId); } catch (_) {}
}

function cardMove(ev, drag) {
  if (ev.pointerId !== drag.pid || AG.drag !== drag) return;
  const dx = ev.clientX - drag.x0, dy = ev.clientY - drag.y0;
  if (!drag.active) {
    if (Math.hypot(dx, dy) < DRAG_THRESHOLD) return;
    startDrag(drag, ev);
  }
  if (ev.cancelable) ev.preventDefault();
  positionGhost(drag, ev.clientX, ev.clientY);
  updateTargets(drag, ev.clientX, ev.clientY);
}

function startDrag(drag, ev) {
  drag.active = true;
  const canvas = $("#oc-canvas");
  drag.card.classList.add("drag-src", "dragging");
  if (canvas) canvas.classList.add("dragging");
  const r = drag.card.getBoundingClientRect();
  drag.offX = ev.clientX - r.left;
  drag.offY = ev.clientY - r.top;
  const ghost = drag.card.cloneNode(true);
  ghost.classList.remove("drag-src", "dragging");
  ghost.classList.add("oc-ghost");
  ghost.style.width = r.width + "px";
  document.body.appendChild(ghost);
  drag.ghost = ghost;
  drag.prevUserSelect = document.body.style.userSelect;
  document.body.style.userSelect = "none";
  window.addEventListener("touchmove", blockScroll, { passive: false });
  positionGhost(drag, ev.clientX, ev.clientY);
  // Seats first: a seat nested in the panel zone wins the hit test.
  const targets = canvas ? [...$$(".oc-seat", canvas), ...$$(".oc-zone", canvas)] : [];
  for (const el of targets) {
    const spec = specOfTarget(el);
    const t = { el, spec, plan: planDrop(drag.name, spec) };
    drag.targets.push(t);
    el.classList.add(t.plan.ok ? "drop-ok" : "drop-no");
    const hint = $(".oc-hint", el);
    if (hint && t.plan.ok) { hint.innerHTML = t.plan.html; hint.classList.add("show"); }
  }
}

function positionGhost(drag, x, y) {
  if (!drag.ghost) return;
  drag.ghost.style.transform = `translate(${x - drag.offX}px, ${y - drag.offY}px) scale(1.03)`;
}

function hitTarget(drag, x, y) {
  const inside = (el) => {
    const r = el.getBoundingClientRect();
    return x >= r.left && x <= r.right && y >= r.top && y <= r.bottom;
  };
  for (const t of drag.targets) if (t.spec.kind === "seat" && inside(t.el)) return t;
  for (const t of drag.targets) if (t.spec.kind !== "seat" && inside(t.el)) return t;
  return null;
}

function updateTargets(drag, x, y) {
  const hit = hitTarget(drag, x, y);
  if (hit === drag.hit) return;
  if (drag.hit) {
    drag.hit.el.classList.remove("drop-active");
    if (!drag.hit.plan.ok) {
      const h = $(".oc-hint", drag.hit.el);
      if (h) { h.classList.remove("show", "bad"); h.textContent = ""; }
    }
  }
  drag.hit = hit;
  if (!hit) return;
  hit.el.classList.add("drop-active");
  if (!hit.plan.ok) {
    const h = $(".oc-hint", hit.el);
    if (h) { h.textContent = hit.plan.why; h.classList.add("bad", "show"); }
  }
}

function cardUp(ev, drag, cancelled) {
  if (AG.drag !== drag) return;
  if (ev && ev.pointerId !== undefined && ev.pointerId !== drag.pid) return;
  const wasActive = drag.active;
  let spec = null, why = null;
  if (wasActive && !cancelled && drag.hit) {
    if (drag.hit.plan.ok) spec = drag.hit.spec;
    else why = drag.hit.plan.why;
  }
  const name = drag.name;
  cleanupDrag(drag);
  if (wasActive) {
    AG.suppressCardUntil = Date.now() + 450;
    if (spec) applyTarget(name, spec);
    else if (why) toast(why, true);
  }
}

function cleanupDrag(drag) {
  if (!drag) return;
  if (drag.cleanup) drag.cleanup();
  if (drag.ghost) drag.ghost.remove();
  drag.card.classList.remove("drag-src", "dragging");
  const canvas = $("#oc-canvas");
  if (canvas) {
    canvas.classList.remove("dragging");
    for (const t of drag.targets || []) {
      t.el.classList.remove("drop-ok", "drop-no", "drop-active");
      const h = $(".oc-hint", t.el);
      if (h) { h.classList.remove("show", "bad"); h.textContent = ""; }
    }
  }
  window.removeEventListener("touchmove", blockScroll);
  document.body.style.userSelect = drag.prevUserSelect || "";
  if (AG.drag === drag) AG.drag = null;
}

// ---------- binding ----------

function bindAgentsBody() {
  const body = $("#agents-body");
  if (!body || AG.bodyEl === body) return;
  AG.bodyEl = body;
  body.addEventListener("input", onFieldEvent);
  body.addEventListener("change", onFieldEvent);
  body.addEventListener("click", onBodyClick);
  body.addEventListener("keydown", onBodyKeydown);
  body.addEventListener("pointerdown", onBodyPointerDown);
}

function bindAgentsOnce() {
  if (AG.keyBound) return;
  AG.keyBound = true;
  document.addEventListener("keydown", (ev) => {
    if (S.route.view !== "agents") return;
    if (ev.key === "Escape") {
      if (AG.drag && AG.drag.active) { cleanupDrag(AG.drag); AG.suppressCardUntil = Date.now() + 450; return; }
      if (AG.drawer !== undefined) closeDrawer();
      return;
    }
    if ((ev.metaKey || ev.ctrlKey) && !ev.shiftKey && (ev.key === "z" || ev.key === "Z")) {
      const t = document.activeElement;
      if (t && /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName)) return; // native undo inside fields
      if (!AG.undo.length) return;
      ev.preventDefault();
      undoLast();
    }
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
    <form id="composer" class="composer"></form></div>`;
  S.chat.after = 0;
  renderComposer([]);
  pollChat();
}

// pendingQuestions finds the trailing run of batch questions — the ones the
// interview posted together and has not been answered yet. They carry an
// "[i/n]" prefix, which is how a single approval question (no prefix) stays
// on the one-line path.
function pendingQuestions(log) {
  const out = [];
  for (let i = log.children.length - 1; i >= 0; i--) {
    const el = log.children[i];
    if (!el.classList.contains("msg")) continue;
    if (el.classList.contains("user")) break;
    if (el.classList.contains("agent") && /^\[\d+\/\d+\]/.test(el.textContent)) {
      out.unshift(el.textContent);
      continue;
    }
    break;
  }
  return out;
}

// renderComposer swaps between the one-line answer and the batch form. It
// rebuilds only when the shape changes, so typed text is never thrown away
// while the interview is still waiting.
function renderComposer(questions) {
  const form = $("#composer");
  if (!form) return;
  const batch = questions.length > 1;
  const key = batch ? `batch:${questions.length}` : "single";
  if (form.dataset.key === key) return;
  form.dataset.key = key;
  if (batch) {
    form.innerHTML =
      questions.map((q, i) =>
        `<label class="q-label" for="ans-${i}">${esc(q)}</label>` +
        `<textarea class="ans" id="ans-${i}" rows="1" data-i="${i}"></textarea>`
      ).join("") +
      `<div class="composer-actions">
         <button id="chat-send" class="btn primary" type="submit">Send answers</button>
         <button id="chat-skip" class="btn small" type="button">Skip to the spec</button>
         <span class="muted">one answer per question · blank = no preference</span>
       </div>`;
  } else {
    form.innerHTML = `<textarea id="chat-input" rows="2" placeholder="Type your answer…  (Ctrl/⌘+Enter to send)"></textarea>
      <button id="chat-send" class="btn primary" type="submit">Send</button>`;
  }
  wireComposer(batch);
}

function wireComposer(batch) {
  const form = $("#composer");
  form.onsubmit = async (ev) => {
    ev.preventDefault();
    if (batch) {
      await sendAnswers($$("textarea.ans", form).map((t) => t.value));
    } else {
      await sendAnswer($("#chat-input").value);
    }
  };
  const skip = $("#chat-skip");
  if (skip) {
    skip.onclick = async () => {
      const n = $$("textarea.ans", form).length;
      if (!n) return;
      // /done in the first slot: the interview stops there.
      await sendAnswers(["/done", ...Array(Math.max(0, n - 1)).fill("")]);
    };
  }
  const input = $("#chat-input");
  if (!input) return;
  const autosize = () => {
    input.style.height = "auto";
    input.style.height = Math.min(200, input.scrollHeight) + "px";
    $("#chat-send").textContent = input.value.trim() ? "Send" : "Send (no preference)";
  };
  input.addEventListener("input", autosize);
  input.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter" && (ev.ctrlKey || ev.metaKey)) {
      ev.preventDefault();
      form.requestSubmit();
    }
  });
  autosize();
}

// quickReply routes a one-click shortcut through whichever composer is open:
// in a batch the shortcut becomes the first slot (the interview stops there).
function quickReply(value) {
  const form = $("#composer");
  if (form && (form.dataset.key || "").startsWith("batch")) {
    const n = $$("textarea.ans", form).length;
    sendAnswers([value, ...Array(Math.max(0, n - 1)).fill("")]);
    return;
  }
  sendAnswer(value);
}

async function sendAnswer(text) {
  const input = $("#chat-input");
  const btn = $("#chat-send");
  if (btn) btn.disabled = true;
  try {
    await api(`projects/${encodeURIComponent(S.route.id)}/chat`, { method: "POST", body: { text } });
    if (input) input.value = "";
    S.chat.waiting = false;
    if (btn && input) btn.textContent = "Send (no preference)";
  } catch (e) {
    toast(e.message, true);
  }
  await pollChat();
}

// sendAnswers submits the whole interview round in one request — the reason
// an eight-question round now costs one interaction instead of eight.
async function sendAnswers(answers) {
  const btn = $("#chat-send");
  if (btn) btn.disabled = true;
  try {
    await api(`projects/${encodeURIComponent(S.route.id)}/chat`, { method: "POST", body: { answers } });
    S.chat.waiting = false;
    S.chat.pending = 0;
    renderComposer([]); // batch → single; rebuilds and wires itself once
  } catch (e) {
    toast(e.message, true);
    if (btn) btn.disabled = false;
    return;
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
        btn.addEventListener("click", () => quickReply(b.value));
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
  S.chat.pending = res.pending || 0;

  // Move the composer between the one-line answer and the batch form as the
  // interview switches between a single question and a round of them.
  if (res.waiting && S.chat.pending > 1) renderComposer(pendingQuestions(log));
  else renderComposer([]);

  const sendBtn = $("#chat-send");
  if (sendBtn) sendBtn.disabled = !res.waiting;
  const oneInput = $("#chat-input");
  if (oneInput) oneInput.disabled = !res.waiting;
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
  if (res.waiting && window.innerWidth > 860) {
    // Focus whatever the composer currently offers — the single input, or the
    // first field of a batch — but never steal focus while the user types.
    const target = $("#chat-input") || $("textarea.ans", $("#composer"));
    const ae = document.activeElement;
    if (target && (!ae || ae === document.body)) target.focus();
  }
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
