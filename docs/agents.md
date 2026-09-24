# Agents and models

factory ships **no default agents and no default models**. `factory init`
writes an empty config on purpose: which services you use, on which models,
and who reviews whom, is the one decision this tool must not make for you.

This guide is the cookbook. For the full field list see
[Configuration reference](../README.md#configuration-reference).

- [The three adapter types](#the-three-adapter-types)
- [Adding an agent](#adding-an-agent)
- [Seating agents into roles](#seating-agents-into-roles)
- [Pin a model to a seat](#pin-a-model-to-a-seat)
- [Cookbook](#cookbook)
  - [Coding CLIs](#coding-clis)
  - [OpenAI-compatible APIs](#openai-compatible-apis)
  - [Local models](#local-models)
  - [OpenRouter, and everything behind one key](#openrouter-and-everything-behind-one-key)
  - [Your own service](#your-own-service)
- [Roundtable panels](#roundtable-panels)
- [Verifying it works](#verifying-it-works)
- [Troubleshooting](#troubleshooting)

---

## The three adapter types

Every agent is one of three kinds. Picking one is the whole decision:

| Type | Talks to | Can edit files? | Use it for |
|---|---|---|---|
| `opencode` | the `opencode` CLI | **yes** | The builder, and any seat that should read the repo |
| `command` | any executable on your `PATH` | yes, if the tool can | Coding CLIs (`claude`, `codex`, `aider`, `gemini`), wrappers, your own scripts |
| `openai` | an HTTP `POST /chat/completions` | **no** | Reviewers, interviewers, roundtable seats — anything that only reads and talks |

**The builder must be `opencode` or `command`.** factory validates this and
refuses an `openai` builder with an explanation: an endpoint that can only
return text cannot run your tests or write a file. The other four seats take
any type.

`command` prompts go to **stdin** unless one of your `args` contains
`{{prompt}}`. That single detail is the difference between "works" and "my
agent printed its help text instead of answering".

---

## Adding an agent

From a shell — no browser needed, so it fits in a script or a dotfiles repo:

```sh
factory agent add <name> --type <opencode|command|openai> [flags]
factory agent list                     # what you have, and who sits where
factory agent list --json              # the same, for scripts
```

The flags map one-to-one onto the config file:

```sh
factory agent add claude \
  --type command \
  --command claude \
  --arg -p --arg '{{prompt}}' \
  --title "Senior engineer" --dept "Delivery"
```

| Flag | Field | Notes |
|---|---|---|
| `--type` | `type` | required |
| `--model` | `model` | `opencode` / `openai` |
| `--command` | `command` | required for `command` |
| `--arg` | `args` | repeatable; use `{{prompt}}` to place the prompt |
| `--readonly-arg` | `readonly_args` | repeatable; appended on read-only seats |
| `--base-url` | `base_url` | required for `openai` |
| `--api-key-env` | `api_key_env` | **name of** an env var, never the key itself |
| `--env K=V` | `env` | repeatable |
| `--timeout` | `timeout` | e.g. `30m` |
| `--role` | `roles` | seat to fill; repeatable |
| `--title` `--dept` | `org.json` | display metadata only |

Your **first** agent fills every empty seat, so one command takes you from an
empty config to a working one. Later agents claim nothing unless you pass
`--role`.

Every write goes through the same validated, atomic save as both GUI editors,
under a shared lock — a CLI add and a drag in the org chart cannot interleave
into a torn file. A rejected agent leaves the config exactly as it was.

Or edit `factory.json` directly; it is a plain file and both editors will
still open it.

---

## Seating agents into roles

Five seats run a project:

| Seat | Runs | Wants |
|---|---|---|
| `interviewer` | the spec interview | good questions, product judgement |
| `planner` | the task breakdown | ordering and scope |
| `builder` | implementation + tests | file access, hands-on |
| `reviewer` | diff and test review | strictness, independence from the builder |
| `moderator` | every roundtable synthesis | synthesis over verbosity |

Nothing says one agent holds one seat, or that seats share a model. The
common shapes:

```sh
# One model builds, a different one reviews it — the independence matters.
factory agent add oc   --type opencode --role builder
factory agent add qa   --type command --command claude --arg -p --arg '{{prompt}}' --role reviewer

# One agent, several seats.
factory agent add solo --type opencode --role interviewer --role planner --role moderator
```

A reviewer that is the same model as the builder tends to approve what the
builder would have written. Two is the minimum worth running.

---

## Pin a model to a seat

Sometimes the *seat* should pick the model regardless of who fills it — a
frontier model for review, a cheap one for the interview:

```jsonc
"roles":       { "reviewer": "claude", "builder": "oc" },
"role_models": { "reviewer": "claude-sonnet-4-6" }
```

- An entry overrides **only its own seat**. Other seats on the same agent
  keep that agent's model.
- How it is applied depends on the type: `opencode` → `--model`, `openai` →
  `model`, `command` → substituted into `{{model}}`.
- **A `command` agent only receives it if your `args` contain `{{model}}`.**
  Validation rejects an override that would otherwise be accepted and
  silently ignored — this is the most common setup mistake.

Both editors offer a picker per seat fed by `GET /api/models`, which reports
three kinds of source:

- **live lists** — `opencode models`, plus every `openai` agent's own
  `GET /models` endpoint, so you see the ids your provider actually serves;
- **fixed values** — camelStream only accepts `auto`;
- **free text with the flag spelled out** — for CLIs with no list command,
  it tells you what to type instead of guessing at a dropdown.

---

## Cookbook

All of these are complete, working fragments. Merge the `agents` block into
your `factory.json` (or reach the same result with `factory agent add`), then
export the key and run `factory doctor`.

### Coding CLIs

Give the CLI your prompt as an argument — without `{{prompt}}` it would be
sent to stdin, which most of these read as an interactive session.

**Claude Code**

```jsonc
"claude": {
  "type": "command",
  "command": "claude",
  "args": ["-p", "{{prompt}}"],
  "timeout": "30m"
}
```

```sh
factory agent add claude --type command --command claude \
  --arg -p --arg '{{prompt}}' --role reviewer
```

**ChatGPT / codex**

```jsonc
"codex": {
  "type": "command",
  "command": "codex",
  "args": ["exec", "-m", "{{model}}", "{{prompt}}"],
  "timeout": "30m"
}
```

**Gemini CLI**

```jsonc
"gemini": {
  "type": "command",
  "command": "gemini",
  "args": ["--model", "{{model}}", "-p", "{{prompt}}"],
  "timeout": "30m"
}
```

**aider** — accepts litellm model names, so one entry reaches many providers:

```jsonc
"aider": {
  "type": "command",
  "command": "aider",
  "args": ["--model", "{{model}}", "--message", "{{prompt}}"],
  "readonly_args": ["--yes-always"],
  "timeout": "30m"
}
```

**llm** — Simon Willison's multipurpose CLI:

```jsonc
"llm": {
  "type": "command",
  "command": "llm",
  "args": ["-m", "{{model}}", "{{prompt}}"],
  "timeout": "20m"
}
```

> A CLI with no list command is offered as **free text in the picker**, with
> the flag it expects. Type the id your tool accepts.

### OpenAI-compatible APIs

Anything exposing `POST {base}/chat/completions` works with no extra
software. factory reads the key from the env var you name — **the name, never
the value** — and `GET /api/config` will not return it to a browser.

```jsonc
"openai-primary": {
  "type": "openai",
  "base_url": "https://api.openai.com/v1",
  "model": "gpt-4o-mini",
  "api_key_env": "OPENAI_API_KEY",
  "timeout": "10m"
}
```

```sh
export OPENAI_API_KEY=sk-…
factory agent add gpt --type openai \
  --base-url https://api.openai.com/v1 \
  --model gpt-4o-mini \
  --api-key-env OPENAI_API_KEY \
  --role interviewer --role moderator
```

The same block works for any provider that speaks the format — **Groq,
Together, Fireworks, DeepSeek, Mistral, xAI** and most gateways. Change
`base_url`, `model` and the env var name:

```jsonc
"groq": {
  "type": "openai",
  "base_url": "https://api.groq.com/openai/v1",
  "model": "llama-3.3-70b-versatile",
  "api_key_env": "GROQ_API_KEY"
}
```

Model ids here are **examples, not recommendations** — providers rename and
retire models constantly. The picker lists what your endpoint currently
serves (`GET /models`), which is the reliable source.

### Local models

**Ollama** — its OpenAI-compatible endpoint means no special handling:

```jsonc
"ollama": {
  "type": "openai",
  "base_url": "http://127.0.0.1:11434/v1",
  "model": "qwen3:8b",
  "timeout": "30m"
}
```

No `api_key_env`: local endpoints do not need one, and factory only sends an
`Authorization` header when you name a variable that is set.

**LM Studio** and **vLLM** are the same shape, on their own ports
(`1234`, `8000` by default).

Local models are `openai` type, so they **cannot be the builder** — that is
correct, since a small local model writing and running your tests is usually
the wrong trade. Use one for interviews, moderation and roundtable seats, and
let a hosted model (or `opencode`) build.

To build from a local model, use a CLI that can reach it, as a `command`
agent:

```jsonc
"local-builder": {
  "type": "command",
  "command": "aider",
  "args": ["--model", "ollama/llama3", "--message", "{{prompt}}"],
  "timeout": "45m"
}
```

### OpenRouter, and everything behind one key

One endpoint, hundreds of models — useful when you want to change models
without editing agent definitions:

```jsonc
"openrouter": {
  "type": "openai",
  "base_url": "https://openrouter.ai/api/v1",
  "model": "anthropic/claude-sonnet-4.5",
  "api_key_env": "OPENROUTER_API_KEY"
}
```

With `{{model}}` in a `command` agent's args (or a `role_models` entry on an
`openai` agent), you then switch models **per seat** without touching the
agent.

### Your own service

Anything that speaks HTTP. Two options:

**Native `openai` type**, if you implement `/chat/completions`:

```jsonc
"mine": { "type": "openai", "base_url": "http://127.0.0.1:8080/v1", "model": "default" }
```

**A `command` wrapper**, if you would rather own the client. It gets the
prompt on stdin and prints the reply; two environment variables tell it how
it is being used:

```jsonc
"mine": {
  "type": "command",
  "command": "/usr/local/bin/my-agent",
  "args": ["{{prompt_file}}"],
  "env": { "MY_AGENT_ENDPOINT": "http://127.0.0.1:8080" },
  "readonly_args": ["--read-only"],
  "timeout": "10m"
}
```

| Variable | Meaning |
|---|---|
| `FACTORY_STAGE` | why it is being called: `interview`, `spec-draft`, `spec-revise`, `plan`, `build`, `review`, `trial`, `acceptance-verdict`, `roundtable-<slug>`, `roundtable-<slug>-synthesis`, `doctor` |
| `FACTORY_READONLY` | `"true"` on reviewers, interviewers and roundtable seats — offer read/grep/list only |

Use `FACTORY_STAGE` to route one wrapper to different models per phase — a
cheap model for roundtables, an expensive one for review.

Placeholders available in `command`/`args`:

| Placeholder | Expands to |
|---|---|
| `{{prompt}}` | the prompt, inline as an argument (≤ 96 KB) |
| `{{prompt_file}}` | a temp file holding the prompt — use this for long prompts |
| `{{model}}` | the seat's model, from `role_models` — **required** for a pin to work |
| `{{dir}}` | the project directory |
| `{{config_dir}}` | the directory holding `factory.json` |

`$VARS` in `env` values are expanded. Prompts past ~96 KB are written to a
file automatically, because Linux caps a single argument at 128 KiB.

---

## Roundtable panels

Every roundtable gets a panel of named perspectives. This is **not** the same
as the five seats: a seat runs a pipeline stage, a panelist argues a point of
view.

```jsonc
"roundtable": {
  "rounds": 2,
  "on_spec": true, "on_plan": true, "on_tasks": true,
  "participants": [
    { "agent": "oc",        "persona": "Senior engineer. Care about correctness, simplicity and what is actually feasible to build and test." },
    { "agent": "ollama",    "persona": "Product owner and end-user advocate. Care about whether real users finish the job, and about ruthless scope control." },
    { "agent": "openai-rep","persona": "QA and test lead. Care about edge cases, failure modes, and tests that prove behaviour rather than merely pass." }
  ]
}
```

- Each participant names an **agent**, not a seat — so `role_models` does not
  apply to them.
- `rounds: 1` is independent takes only; `2+` adds a rebuttal round.
- Cost is `seats × rounds + 1` per table. An empty panel is fine: the
  moderator is told to use its own judgement, and you pay one call.

---

## Verifying it works

```sh
factory doctor
```

It resolves the config, builds every agent, and asks each one — concurrently,
in a scratch directory, in read-only mode — for exactly `OK`. It also shows
which agent fills which seat, and names any seat left empty.

```
config: ~/.config/factory/factory.json
✔ oc            1.2s  "OK"
✔ claude        3.4s  "OK"
✖ groq          agent groq: HTTP 401
roles: interviewer=gpt planner=gpt builder=oc reviewer=claude moderator=gpt
```

An empty seat is a config that validates but cannot run a project — `factory
agent list` prints those at the bottom, and `doctor` shows every seat in its
`roles:` line.

---

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `agent X: command is required` | A `command` agent with no `--command`. Add it. |
| `role builder: agent X is type openai and cannot edit files` | Use an `opencode` or `command` agent as the builder. |
| `agent X: base_url and model are required` | An `openai` agent missing its endpoint or default model. |
| `no config found; run factory init` | You are not in the config directory and no `--config`/`$FACTORY_CONFIG`. |
| `no agents configured yet` | factory ships none on purpose. Add yours. |
| An agent answers nothing, exits fast | Run its command by hand in that directory. Check `PATH` and the key's env var being **exported** in the environment factory runs in (systemd needs `Environment=`/`EnvironmentFile=`). |
| An agent hangs for the whole timeout | Usually stuck on the network. Watch its process: if CPU time is not advancing, kill it — factory records the error and retries. |
| A model pin appears to do nothing | On a `command` agent, `args` must contain `{{model}}`. Validation rejects the config if not. |
| `role_models` rejected | Only the five known seats can be pinned, and the value cannot be blank. |
| Roundtable seats queue behind each other | Participants run in parallel. Cap them with `limits.max_parallel_participants` to your provider's concurrency limit. |
| A picker shows no models | Expected for CLIs without a list command — it becomes a free-text field with the flag to type. An `openai` agent warns if its `/models` endpoint was unreachable. |
