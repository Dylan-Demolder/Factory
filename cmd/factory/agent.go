package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/dylan-demolder/factory/internal/config"
)

// `factory agent` is the scriptable half of the org chart. Both editors can
// already add and seat an agent; this exists so a fleet can be set up from a
// shell, a dotfiles repo or a provisioning script without a browser.
//
// It writes through config.AddAgent, which goes through the same config.Save
// as the editors — validated, atomic, and under the shared lock — so a CLI
// add and a drag in the web UI cannot interleave.

const agentUsage = `factory agent — add and inspect the agents that fill factory's seats

Usage:
  factory agent list [--config PATH] [--json]
      NAME      TYPE      MODEL/COMMAND          SEATS
      oc        opencode  opencode-go/glm-5.3    builder, interviewer, …

  factory agent add <name> --type KIND [flags]
      --type KIND            opencode | command | openai        (required)
      --model M              model id for opencode/openai
      --command CMD          executable, for type=command       (required for command)
      --arg A                argument; repeat for each. Use {{prompt}} to place
                             the prompt, {{model}} for a per-seat model override.
                             Without {{prompt}} the prompt goes to stdin.
      --readonly-arg A       argument appended on read-only seats; repeatable
      --base-url URL         endpoint root, for type=openai (e.g. https://api.openai.com/v1)
      --api-key-env NAME     name of the env var holding the key, for type=openai
      --env K=V              extra environment variable; repeatable
      --timeout D            per-call timeout, e.g. 30m
      --role ROLE            seat to fill with this agent; repeatable
                             (interviewer, planner, builder, reviewer, moderator)
      --title S              job title, shown in the org chart
      --dept S               department, shown in the org chart
      --config PATH          config file to edit (default: resolved as usual)

A first agent fills every empty seat, so one command is enough to go from an
empty config to a working one. Later agents claim nothing unless you pass
--role. factory ships no default agents: you choose the services and models.

Examples:
  # opencode as the builder (and, being alone, everything else)
  factory agent add oc --type opencode --model opencode-go/glm-5.3 --role builder

  # Claude Code as reviewer and interviewer
  factory agent add claude --type command --command claude --arg -p --arg {{prompt}} \
    --role reviewer --role interviewer

  # any OpenAI-compatible endpoint, for the seats that only read
  factory agent add groq --type openai --base-url https://api.groq.com/openai/v1 \
    --model llama-3.3-70b-versatile --api-key-env GROQ_API_KEY --role moderator

Run ` + "`factory doctor`" + ` afterwards to check every agent answers.
Full guide: docs/agents.md`

// strList collects a repeatable string flag.
type strList []string

func (s *strList) String() string { return strings.Join(*s, ", ") }
func (s *strList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// pairList collects repeatable KEY=VALUE flags.
type pairList map[string]string

func (p *pairList) String() string {
	if p == nil {
		return ""
	}
	var keys []string
	for k := range *p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		out = append(out, k+"="+(*p)[k])
	}
	return strings.Join(out, ", ")
}

func (p *pairList) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || strings.TrimSpace(k) == "" {
		return fmt.Errorf("--env wants KEY=VALUE, got %q", v)
	}
	if !isEnvName(k) {
		return fmt.Errorf("%q is not a valid environment variable name", k)
	}
	if *p == nil {
		*p = pairList{}
	}
	(*p)[k] = val
	return nil
}

// isEnvName accepts the POSIX shape: a letter or underscore, then letters,
// digits or underscores. Matches the rule config applies to an env file.
func isEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func cmdAgent(args []string) error {
	if len(args) == 0 {
		return errors.New(agentUsage)
	}
	switch args[0] {
	case "add":
		return agentAdd(args[1:])
	case "list", "ls":
		return agentList(args[1:])
	case "help", "-h", "--help":
		fmt.Println(agentUsage)
		return nil
	}
	return fmt.Errorf("unknown `agent` subcommand %q (want add or list)\n\n%s", args[0], agentUsage)
}

// agentAdd writes one agent into factory.json.
func agentAdd(args []string) error {
	fs := flag.NewFlagSet("agent add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, agentUsage) }

	var (
		typeFlag    = fs.String("type", "", "agent kind: opencode, command or openai")
		modelFlag   = fs.String("model", "", "model id (opencode/openai)")
		commandFlag = fs.String("command", "", "executable (type=command)")
		baseURL     = fs.String("base-url", "", "endpoint root (type=openai)")
		apiKeyEnv   = fs.String("api-key-env", "", "env var holding the API key (type=openai)")
		timeoutFlag = fs.String("timeout", "", "per-call timeout, e.g. 30m")
		cfgFlag     = fs.String("config", "", "config file to edit")
		titleFlag   = fs.String("title", "", "job title for the org chart")
		deptFlag    = fs.String("dept", "", "department for the org chart")
		argsList    strList
		roArgs      strList
		roles       strList
		envs        pairList
	)
	fs.Var(&argsList, "arg", "argument for a command agent; repeat (use {{prompt}} to place the prompt)")
	fs.Var(&roArgs, "readonly-arg", "argument appended on read-only seats; repeat")
	fs.Var(&roles, "role", "seat to fill with this agent; repeat")
	fs.Var(&envs, "env", "environment variable as KEY=VALUE; repeat")

	pos, err := parse(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println(agentUsage)
			return nil
		}
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: factory agent add <name> --type KIND [flags]\n\n" + agentUsage)
	}

	path, err := config.ResolvePath(*cfgFlag, "")
	if err != nil {
		return err
	}

	spec := config.AgentSpec{
		Name:         pos[0],
		Type:         *typeFlag,
		Model:        *modelFlag,
		Command:      *commandFlag,
		Args:         argsList,
		ReadOnlyArgs: roArgs,
		BaseURL:      *baseURL,
		APIKeyEnv:    *apiKeyEnv,
		Env:          envs,
		Timeout:      *timeoutFlag,
		Roles:        roles,
		Meta:         config.Meta{Title: *titleFlag, Dept: *deptFlag},
	}
	if err := config.AddAgent(path, spec); err != nil {
		return err
	}

	// Show the result the same way list does, so a script can chain the two.
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Printf("added agent %q to %s\n", spec.Name, path)
		return nil
	}
	fmt.Printf("added agent %q to %s\n", spec.Name, path)
	seats := seatsOf(cfg, spec.Name)
	if len(seats) == 0 {
		fmt.Println("  seats: (none — assign one in the org chart, or re-run with --role)")
	} else {
		fmt.Printf("  seats: %s\n", strings.Join(seats, ", "))
	}
	fmt.Println("\nNext: `factory doctor` to check every agent answers.")
	return nil
}

// seatsOf lists the seats an agent holds, in pipeline order.
func seatsOf(c *config.Config, name string) []string {
	var out []string
	for _, r := range config.RoleNames {
		if c.Role(r) == name {
			out = append(out, r)
		}
	}
	return out
}

// agentList prints every agent and the seats it fills.
func agentList(args []string) error {
	fs := flag.NewFlagSet("agent list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, agentUsage) }
	asJSON := fs.Bool("json", false, "print JSON")
	cfgFlag := fs.String("config", "", "config file")
	if _, err := parse(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println(agentUsage)
			return nil
		}
		return err
	}

	path, err := config.ResolvePath(*cfgFlag, "")
	if err != nil {
		return err
	}
	c, err := config.Load(path)
	if err != nil {
		return err
	}
	if !c.Configured() {
		fmt.Printf("config: %s\nno agents configured yet — factory ships none on purpose.\n\n  factory agent add oc --type opencode\n\nSee docs/agents.md for ready-to-paste setups.\n", path)
		return nil
	}

	names := make([]string, 0, len(c.Agents))
	for n := range c.Agents {
		names = append(names, n)
	}
	sort.Strings(names)

	if *asJSON {
		type row struct {
			Name    string   `json:"name"`
			Type    string   `json:"type"`
			Model   string   `json:"model,omitempty"`
			Detail  string   `json:"detail,omitempty"`
			Seats   []string `json:"seats"`
			Timeout string   `json:"timeout,omitempty"`
		}
		out := make([]row, 0, len(names))
		for _, n := range names {
			a := c.Agents[n]
			d := row{Name: n, Type: a.Type, Model: a.Model, Seats: seatsOf(c, n)}
			switch a.Type {
			case "command":
				d.Detail = a.Command
			case "openai":
				d.Detail = a.BaseURL
			}
			if a.Timeout.Duration > 0 {
				d.Timeout = a.Timeout.String()
			}
			out = append(out, d)
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"path": path, "agents": out})
	}

	fmt.Printf("config: %s\n", path)
	fmt.Printf("%-14s %-9s %-34s %s\n", "NAME", "TYPE", "MODEL / COMMAND", "SEATS")
	for _, n := range names {
		a := c.Agents[n]
		detail := a.Model
		switch a.Type {
		case "command":
			detail = a.Command
			if a.Model != "" {
				detail = fmt.Sprintf("%s (%s)", a.Command, a.Model)
			}
		case "openai":
			detail = a.BaseURL
		}
		if detail == "" {
			detail = "—"
		}
		seats := seatsOf(c, n)
		seatStr := strings.Join(seats, ", ")
		if seatStr == "" {
			seatStr = "(no seat)"
		}
		fmt.Printf("%-14s %-9s %-34s %s\n", n, a.Type, detail, seatStr)
	}
	// Any seat left empty means a project would fail the moment it is made.
	var empty []string
	for _, r := range config.RoleNames {
		if c.Role(r) == "" {
			empty = append(empty, r)
		}
	}
	if len(empty) > 0 {
		fmt.Printf("\nempty seat(s): %s — assign with `factory agent add … --role`, or the org chart\n",
			strings.Join(empty, ", "))
	}
	return nil
}
