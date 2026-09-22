# factory

A small, fast replacement for Paperclip-style agent orchestration. You submit a project, spec it together with your agents, then walk away. factory drives it to completion with tests, design roundtables and acceptance checks against real use cases.

It's a single Go binary with no dependencies, no server and no database. All state is plain files in `<project>/.factory/`, so you can kill it at any point and pick up where it left off.

```
 you ──► interview ──► draft spec ──► spec roundtable ──► open questions ──► approve
                                                                           │
   ┌───────────────────────────── autonomous from here ────────────────────┘
   ▼
 plan ──► plan roundtable ──► for each task:
                                design roundtable → build → run tests → QA review
                                  ▲                                  │
                                  └──── feedback (up to N attempts) ◄┘   pass → git commit
   ▼
 acceptance: hands-on trial of every use case → full test run → acceptance roundtable
   │  gaps? → fix tasks → back to build (up to N rounds)
   ▼
 REPORT.md + notification
```

## What each stage does

| Stage | Who | What happens |
|---|---|---|
| Interview | interviewer | Asks up to 3 rounds of targeted questions, each with a proposed default. An empty answer means "you decide". `/done` skips ahead. |
| Spec | interviewer + roundtable | Writes `SPEC.md` with genuine use cases (actor, scenario, observable success) and testable features. The roundtable pushes back on missing or unrealistic use cases, scope and testability. You approve it or ask for changes. |
| Plan | planner + roundtable | Produces ordered tasks: a setup task (skeleton, README, green test suite), feature slices with acceptance criteria, then one end-to-end test task per use case. |
| Design roundtable | all participants → moderator | Before each task, the panel debates approach, edge cases and the required tests. The moderator writes a brief for the builder. |
| Build | builder (opencode) | Implements the task, writes real tests, and runs the suite until it's green. |
| Verify | factory + reviewer | factory runs your test command itself. If it passes, a strict reviewer checks the diff: real tests, criteria met, nothing faked. A failure sends concrete feedback to the next attempt. |
| Blocked | factory | After `max_task_attempts`, the work is saved as a patch and the tree is reset, so a stuck task can't poison later ones. Tasks that depend on it are skipped. |
| Trial | builder | Uses the finished software as a real user would for each use case: runs the CLI, hits the server, and so on. It reports what actually happened, and any files it touched are discarded. |
| Acceptance | roundtable → moderator | Judges each use case from the viewpoint of its actual users, using the trial, tests and repo as evidence. Gaps become fix tasks, and the loop repeats. |
| Report | factory | Writes `REPORT.md`, commits it, and runs your `notify_command`. |

Every roundtable transcript, brief, builder summary, test log and review is kept under `.factory/`, so you can see why each decision was made.

## Install

```sh
go install github.com/dylan-demolder/factory-/cmd/factory@latest
# or from a clone:
go build -o factory ./cmd/factory
```

Requires `git`. Linux and macOS are fully supported. On Windows, use WSL.

## Set up your agents (opencode Go + two CAMEL agents)

```sh
mkdir ~/factory && cd ~/factory
factory init        # writes factory.json and adapters/camel_agent.py
```

Then edit `factory.json`:

- **opencode** is the builder (the only role that edits files). Leave `model` empty to use your opencode default, or set a model from `opencode models`, for example one of your OpenCode Go models. For read-only roles (roundtables, reviews), factory runs opencode with `--agent plan`. Before planning, factory writes an `opencode.json` into the project that allows edits and bash without prompts, because nobody is there to approve them. An existing `opencode.json` is left alone, so if you keep your own, make sure it doesn't prompt for permission.
- **camel-a / camel-b** run through `adapters/camel_agent.py`, which reads the prompt on stdin and prints a CAMEL `ChatAgent` reply. `pip install camel-ai`, then set `CAMEL_PLATFORM`, `CAMEL_MODEL`, `CAMEL_BASE_URL` and the API key variable in each agent's `env` block. If your CAMEL agents already run as a service, replace them with either:
  - an `"openai"` agent: `{"type":"openai","base_url":"http://host:port/v1","model":"...","api_key_env":"MY_KEY"}`
  - or any CLI: `{"type":"command","command":"curl","args":["-s","-d","@-","http://host/agent"]}`. The prompt goes to stdin unless an argument contains `{{prompt}}` or `{{prompt_file}}`.

```sh
factory doctor      # pings every agent and prints the role mapping
```

The default roles are: camel-a interviews, plans and moderates; opencode builds and trials; camel-b reviews. All three sit on every roundtable with different personas: engineer, product/user advocate, and QA lead. Rearrange them freely.

## Use it

```sh
factory new todo-cli                     # interview → spec → approve → "start now?" → runs in background
factory new api --idea-file idea.md      # seed the idea from a file
factory new feature-x --dir ~/code/app   # works on an existing repo too

factory status todo-cli                  # tasks, use-case verdicts, recent log
tail -f todo-cli/.factory/run.log        # or: factory logs todo-cli
factory stop todo-cli                    # pause; progress is saved
factory run todo-cli --detach            # resume
factory spec todo-cli                    # reopen the spec (resets the plan, keeps code and history)
```

To get pinged when it finishes or gets stuck, set for example:

```json
"notify_command": "curl -s -d \"$FACTORY_PROJECT: $FACTORY_STATUS — $FACTORY_MESSAGE\" ntfy.sh/my-topic"
```

The command runs with `FACTORY_PROJECT`, `FACTORY_STATUS` (`complete`, `finished-with-issues`, `blocked`, `error`), `FACTORY_MESSAGE` and `FACTORY_DIR` set.

## Configuration reference

```jsonc
{
  "agents": {
    "<name>": {
      "type": "opencode | command | openai",
      "model": "",                 // opencode: --model; command: {{model}}; openai: model
      "command": "", "args": [],   // command type; {{prompt}} {{prompt_file}} {{model}} {{dir}} {{config_dir}}
      "readonly_args": [],         // appended for read-only calls (opencode default: --agent plan)
      "env": {},                   // extra env; $VARS are expanded
      "base_url": "", "api_key_env": "",   // openai type
      "timeout": "30m"
    }
  },
  "roles": { "interviewer": "", "planner": "", "builder": "", "reviewer": "", "moderator": "" },
  "roundtable": {
    "rounds": 2,                   // 1 = independent takes only; 2+ = panelists respond to each other
    "on_spec": true, "on_plan": true, "on_tasks": true,
    "participants": [ { "agent": "", "persona": "" } ]
  },
  "limits": {
    "max_interview_rounds": 3, "max_task_attempts": 4, "max_acceptance_rounds": 3,
    "agent_retries": 2, "agent_timeout": "30m", "test_timeout": "15m"
  },
  "test_command": "",              // override the planner's choice
  "notify_command": ""
}
```

Command agents also receive `FACTORY_STAGE` (e.g. `interview`, `build`, `review`, `roundtable-task-T3`) and `FACTORY_READONLY`, so a wrapper script can route stages to different models.

Config lookup order: `--config`, `$FACTORY_CONFIG`, `<project>/.factory/config.json` (a snapshot taken by `factory new`), `./factory.json`, `~/.config/factory/factory.json`.

## Tips for unattended runs

- Answer the interview properly. It's the only input the agents get. Say exactly what "done" means, what's out of scope, and which external services or credentials the product needs. The agents can't get new credentials once you've left.
- Each `on_*` roundtable adds 1 + participants × rounds calls. For a quick prototype, set `on_tasks: false` or `rounds: 1`.
- A blocked task isn't lost: its attempts, feedback and patch are in `.factory/tasks/`.

## Development

```sh
go test -race ./...
```

The pipeline tests use scripted fake agents to drive complete runs: interview, a spec that fails to parse and gets repaired, a transient agent failure, tests that fail and are retried, a reviewer rejection, a blocked task with dependency cascading, acceptance gaps leading to fix tasks, and resuming after a crash.
