// Command factory takes a project from idea to tested, accepted software
// using your own coding agents. See README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dylan-demolder/factory-/internal/agent"
	"github.com/dylan-demolder/factory-/internal/config"
	"github.com/dylan-demolder/factory-/internal/pipeline"
	"github.com/dylan-demolder/factory-/internal/proc"
	"github.com/dylan-demolder/factory-/internal/state"
	"github.com/dylan-demolder/factory-/internal/ui"
)

const usage = `factory — submit a project, spec it together, then let your agents build it.

Usage:
  factory init                         write factory.json + CAMEL adapter to the current directory
  factory doctor                       check every configured agent responds
  factory new <name> [flags]           create a project, run the spec interview, then hand off
      --dir PATH        project directory (default ./<name>; may be an existing repo)
      --idea TEXT       the idea (otherwise you are asked)
      --idea-file FILE  read the idea from a file
      --no-run          don't offer to start the build afterwards
  factory spec [dir]                   re-open the spec interview for a project
  factory run [dir] [--detach]         run (or resume) the autonomous build
  factory status [dir]                 show progress
  factory logs [dir]                   print the run log
  factory stop [dir]                   stop a detached run (resume later with run)

Global flag: --config PATH (default: $FACTORY_CONFIG, <dir>/.factory/config.json,
./factory.json, ~/.config/factory/factory.json)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	args := os.Args[2:]
	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit(args)
	case "doctor":
		err = cmdDoctor(ctx, args)
	case "new":
		err = cmdNew(ctx, args)
	case "spec":
		err = cmdSpec(ctx, args)
	case "run":
		err = cmdRun(ctx, args)
	case "status":
		err = cmdStatus(args)
	case "logs":
		err = cmdLogs(args)
	case "stop":
		err = cmdStop(args)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		err = fmt.Errorf("unknown command %q\n\n%s", os.Args[1], usage)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "\nfactory: interrupted — progress is saved; resume with `factory run`")
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "factory:", err)
		os.Exit(1)
	}
}

// parse handles flags placed before or after positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func projectDir(pos []string) (string, error) {
	dir := "."
	if len(pos) > 0 {
		dir = pos[0]
	}
	return filepath.Abs(dir)
}

type project struct {
	cfg     *config.Config
	cfgPath string
	store   *state.Store
	p       *state.Project
}

func openProject(dir, cfgFlag string) (*project, error) {
	store := &state.Store{Root: dir}
	p, err := store.Load()
	if err != nil {
		return nil, err
	}
	cfg, path, err := config.Resolve(cfgFlag, dir)
	if err != nil {
		return nil, err
	}
	return &project{cfg: cfg, cfgPath: path, store: store, p: p}, nil
}

func (pr *project) engine(out io.Writer) (*pipeline.Engine, error) {
	agents, err := agent.NewAll(pr.cfg)
	if err != nil {
		return nil, err
	}
	return pipeline.New(pr.cfg, agents, pr.store, pr.p, out), nil
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite existing files")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	files := map[string]string{
		"factory.json":            config.ExampleJSON,
		"adapters/camel_agent.py": config.CamelAdapter,
	}
	for path, content := range files {
		if _, err := os.Stat(path); err == nil && !*force {
			fmt.Printf("exists, skipped: %s (use --force to overwrite)\n", path)
			continue
		}
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", path)
	}
	fmt.Println("\nNext: edit factory.json (models, CAMEL endpoints), then run `factory doctor`.")
	return nil
}

func cmdDoctor(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	cfgFlag := fs.String("config", "", "config file")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, path, err := config.Resolve(*cfgFlag, "")
	if err != nil {
		return err
	}
	fmt.Println("config:", path)
	agents, err := agent.NewAll(cfg)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("git"); err != nil {
		fmt.Println("✖ git not found on PATH (required)")
	}
	dir, _ := os.MkdirTemp("", "factory-doctor-")
	defer os.RemoveAll(dir)
	failed := 0
	for name, a := range agents {
		start := time.Now()
		out, err := a.Run(ctx, agent.Request{Stage: "doctor", Prompt: "Reply with exactly the word OK and nothing else.", Dir: dir, ReadOnly: true})
		if err != nil {
			failed++
			fmt.Printf("✖ %-12s %v\n", name, err)
			continue
		}
		fmt.Printf("✔ %-12s %s  %q\n", name, time.Since(start).Round(100*time.Millisecond), agent.Tail(out, 60))
	}
	fmt.Printf("roles: interviewer=%s planner=%s builder=%s reviewer=%s moderator=%s\n",
		cfg.Roles.Interviewer, cfg.Roles.Planner, cfg.Roles.Builder, cfg.Roles.Reviewer, cfg.Roles.Moderator)
	if failed > 0 {
		return fmt.Errorf("%d agent(s) failed", failed)
	}
	return nil
}

func cmdNew(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	cfgFlag := fs.String("config", "", "config file")
	dirFlag := fs.String("dir", "", "project directory")
	idea := fs.String("idea", "", "project idea")
	ideaFile := fs.String("idea-file", "", "file containing the project idea")
	noRun := fs.Bool("no-run", false, "don't offer to start the build")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: factory new <name> [--dir PATH] [--idea TEXT | --idea-file FILE]")
	}
	name := pos[0]
	dir := *dirFlag
	if dir == "" {
		dir = name
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}
	if *ideaFile != "" {
		data, err := os.ReadFile(*ideaFile)
		if err != nil {
			return err
		}
		*idea = string(data)
	}
	cfg, cfgPath, err := config.Resolve(*cfgFlag, "")
	if err != nil {
		return err
	}
	store := &state.Store{Root: dir}
	if store.Exists() {
		return fmt.Errorf("%s is already a factory project; use `factory spec %s` or `factory run %s`", dir, dir, dir)
	}
	if err := os.MkdirAll(store.Dir(), 0o755); err != nil {
		return err
	}
	// Snapshot the config so detached and resumed runs use the same setup.
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	if err := store.Write("config.json", strings.ReplaceAll(string(raw), "{{config_dir}}", cfg.Dir)); err != nil {
		return err
	}
	p := &state.Project{Name: name, Idea: strings.TrimSpace(*idea), Phase: state.PhaseSpec, Created: time.Now().UTC()}
	if err := store.Save(p); err != nil {
		return err
	}
	pr := &project{cfg: cfg, cfgPath: store.Path("config.json"), store: store, p: p}
	fmt.Printf("Created project %s in %s\n", name, dir)
	return specAndMaybeRun(ctx, pr, !*noRun)
}

func cmdSpec(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("spec", flag.ExitOnError)
	cfgFlag := fs.String("config", "", "config file")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	dir, err := projectDir(pos)
	if err != nil {
		return err
	}
	pr, err := openProject(dir, *cfgFlag)
	if err != nil {
		return err
	}
	if pid := pr.store.Pid(); proc.Alive(pid) {
		return fmt.Errorf("a build is running (pid %d); `factory stop` it first", pid)
	}
	if pr.p.Phase != state.PhaseSpec {
		fmt.Printf("Project is in phase %q. Re-opening the spec discards the current plan and task progress (code and git history are kept).\n", pr.p.Phase)
		if !ui.New(os.Stdin, os.Stdout).Confirm("Continue?", false) {
			return nil
		}
		pr.p.Phase = state.PhaseSpec
		pr.p.Tasks = nil
		pr.p.AcceptanceRound = 0
		pr.p.Outcome = ""
	}
	return specAndMaybeRun(ctx, pr, true)
}

func specAndMaybeRun(ctx context.Context, pr *project, offerRun bool) error {
	e, err := pr.engine(os.Stdout)
	if err != nil {
		return err
	}
	prompter := ui.New(os.Stdin, os.Stdout)
	e.UI = prompter
	if err := e.Spec(ctx); err != nil {
		return err
	}
	fmt.Printf("\nSpec approved and committed: %s\n", filepath.Join(pr.store.Root, "SPEC.md"))
	if offerRun && prompter.Confirm("Start the autonomous build now, in the background?", true) {
		return detach(pr)
	}
	fmt.Printf("Start it later with: factory run %s --detach\n", pr.store.Root)
	return nil
}

func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgFlag := fs.String("config", "", "config file")
	detachFlag := fs.Bool("detach", false, "run in the background and return immediately")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	dir, err := projectDir(pos)
	if err != nil {
		return err
	}
	pr, err := openProject(dir, *cfgFlag)
	if err != nil {
		return err
	}
	if pid := pr.store.Pid(); pid != os.Getpid() && proc.Alive(pid) {
		return fmt.Errorf("already running (pid %d)", pid)
	}
	if *detachFlag {
		return detach(pr)
	}
	if err := pr.store.WritePid(os.Getpid()); err != nil {
		return err
	}
	defer pr.store.ClearPid()
	e, err := pr.engine(os.Stdout)
	if err != nil {
		return err
	}
	return e.Run(ctx)
}

func detach(pr *project) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := pr.store.Path("run.log")
	logf, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "run", pr.store.Root, "--config", pr.cfgPath)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Dir = pr.store.Root
	proc.Detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	if err := pr.store.WritePid(pid); err != nil {
		return err
	}
	cmd.Process.Release()
	fmt.Printf("Build running in the background (pid %d).\n  progress: factory status %s\n  log:      tail -f %s\n", pid, pr.store.Root, logPath)
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	dir, err := projectDir(pos)
	if err != nil {
		return err
	}
	store := &state.Store{Root: dir}
	p, err := store.Load()
	if err != nil {
		return err
	}
	running := "not running"
	if pid := store.Pid(); proc.Alive(pid) {
		running = fmt.Sprintf("running, pid %d", pid)
	}
	done, blocked, total := p.Counts()
	fmt.Printf("%s  —  phase: %s (%s)\n", p.Name, p.Phase, running)
	if p.Outcome != "" {
		fmt.Printf("outcome: %s\n", p.Outcome)
	}
	if total > 0 {
		fmt.Printf("tasks: %d/%d done, %d blocked · acceptance round %d · tests: %s\n\n", done, total, blocked, p.AcceptanceRound, p.TestCommand)
	}
	icons := map[string]string{state.TaskDone: "✔", state.TaskActive: "▶", state.TaskBlocked: "✖", state.TaskPending: "·"}
	for _, t := range p.Tasks {
		fmt.Printf("  %s %-6s %-60s attempts %d\n", icons[t.Status], t.ID, trunc(t.Title, 60), t.Attempts)
	}
	if len(p.Spec.UseCases) > 0 {
		fmt.Println("\nuse cases:")
		for _, u := range p.Spec.UseCases {
			v := u.Verdict
			if v == "" {
				v = "not judged yet"
			}
			fmt.Printf("  %-5s %-55s %s\n", u.ID, trunc(u.Goal, 55), v)
		}
	}
	if data, err := os.ReadFile(store.Path("run.log")); err == nil {
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) > 8 {
			lines = lines[len(lines)-8:]
		}
		fmt.Println("\nrecent log:")
		for _, l := range lines {
			fmt.Println("  " + l)
		}
	}
	return nil
}

func cmdLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	dir, err := projectDir(pos)
	if err != nil {
		return err
	}
	f, err := os.Open((&state.Store{Root: dir}).Path("run.log"))
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(os.Stdout, f)
	return err
}

func cmdStop(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	dir, err := projectDir(pos)
	if err != nil {
		return err
	}
	store := &state.Store{Root: dir}
	pid := store.Pid()
	if !proc.Alive(pid) {
		store.ClearPid()
		return errors.New("no build is running")
	}
	if err := proc.Terminate(pid); err != nil {
		return err
	}
	fmt.Printf("Sent stop to pid %d; progress is saved. Resume with: factory run %s --detach\n", pid, dir)
	return nil
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
