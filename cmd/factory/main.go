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

	"github.com/dylan-demolder/factory/internal/agent"
	"github.com/dylan-demolder/factory/internal/app"
	"github.com/dylan-demolder/factory/internal/config"
	"github.com/dylan-demolder/factory/internal/proc"
	"github.com/dylan-demolder/factory/internal/state"
	"github.com/dylan-demolder/factory/internal/tui"
	"github.com/dylan-demolder/factory/internal/ui"
)

const usage = `factory — submit a project, spec it together, then let your agents build it.

Usage:
  factory                               open the interactive workspace (on a terminal)
  factory tui [flags]                   the same, as an explicit command
      --workspace DIR   where projects live (default ~/factory-projects)
      --config PATH     config to use for new projects
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
  factory rm <name> [--yes]            delete a project (confirms first; stops its build)
  factory serve [flags]                web interface for creating, speccing and controlling projects
      --addr HOST:PORT  listen address (default 127.0.0.1:7700)
      --workspace DIR   where projects live (default ~/factory-projects)
      --token TOKEN     access token (default $FACTORY_TOKEN, else generated and saved)
      --base-path P     serve under a URL prefix, e.g. /factory, behind a reverse proxy
      --allow-origin O  allow cross-origin API calls from O (repeatable)
      --frame-ancestors S  who may embed the UI in an iframe (CSP; default 'self')
      --tls-cert F --tls-key F  serve HTTPS directly

Global flag: --config PATH (default: $FACTORY_CONFIG, <dir>/.factory/config.json,
./factory.json, ~/.config/factory/factory.json)
`

func main() {
	// Agent credentials live beside factory.json, which the systemd unit
	// loads as its EnvironmentFile. Reading it here too means a plain shell
	// behaves like the service. Variables already set are left alone, so an
	// explicit export wins and PATH is never replaced.
	config.LoadEnv()

	if len(os.Args) < 2 {
		// No arguments on a terminal opens the workspace, the way opencode,
		// hermes and codex do. Anything piped or scripted gets usage instead.
		if isTerminal(os.Stdout) {
			if err := runTUI(nil); err != nil {
				fmt.Fprintln(os.Stderr, "factory:", err)
				os.Exit(1)
			}
			return
		}
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
	case "rm":
		err = cmdRm(args)
	case "serve":
		err = cmdServe(ctx, args)
	case "tui":
		err = runTUI(args)
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

// defaultWorkspace mirrors `factory serve`'s default so the terminal and the
// browser look at the same projects.
func defaultWorkspace() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "factory-projects"
	}
	return filepath.Join(home, "factory-projects")
}

// runTUI starts the interactive workspace.
func runTUI(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	workspace := fs.String("workspace", envOr("FACTORY_WORKSPACE", defaultWorkspace()), "directory holding projects")
	cfgFlag := fs.String("config", os.Getenv("FACTORY_CONFIG"), "config file for new projects")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	// Builds are detached from this process; they need the path of the binary
	// that launched them so `factory run` can be started in the background.
	if exe, err := os.Executable(); err == nil {
		tui.SetExe(exe)
	}
	return tui.Run(*workspace, *cfgFlag, version)
}

// isTerminal reports whether f is a character device — i.e. a person, and not
// a pipe or a CI log, is on the other end.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func projectDir(pos []string) (string, error) {
	dir := "."
	if len(pos) > 0 {
		dir = pos[0]
	}
	return filepath.Abs(dir)
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
	pr, err := app.Create(dir, name, *idea, cfg, cfgPath)
	if err != nil {
		return err
	}
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
	pr, err := app.Open(dir, *cfgFlag)
	if err != nil {
		return err
	}
	if pid, ok := pr.Running(); ok {
		return fmt.Errorf("a build is running (pid %d); `factory stop` it first", pid)
	}
	if pr.P.Phase != state.PhaseSpec {
		fmt.Printf("Project is in phase %q. Re-opening the spec discards the current plan and task progress (code and git history are kept).\n", pr.P.Phase)
		if !ui.New(os.Stdin, os.Stdout).Confirm("Continue?", false) {
			return nil
		}
		if err := pr.ResetForSpec(); err != nil {
			return err
		}
	}
	return specAndMaybeRun(ctx, pr, true)
}

func specAndMaybeRun(ctx context.Context, pr *app.Project, offerRun bool) error {
	e, err := pr.Engine(os.Stdout)
	if err != nil {
		return err
	}
	prompter := ui.New(os.Stdin, os.Stdout)
	e.UI = prompter
	if err := e.Spec(ctx); err != nil {
		return err
	}
	fmt.Printf("\nSpec approved and committed: %s\n", filepath.Join(pr.Store.Root, "SPEC.md"))
	if offerRun && prompter.Confirm("Start the autonomous build now, in the background?", true) {
		return detach(pr)
	}
	fmt.Printf("Start it later with: factory run %s --detach\n", pr.Store.Root)
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
	pr, err := app.Open(dir, *cfgFlag)
	if err != nil {
		return err
	}
	if pid, ok := pr.Running(); ok && pid != os.Getpid() {
		return fmt.Errorf("already running (pid %d)", pid)
	}
	if *detachFlag {
		return detach(pr)
	}
	if err := pr.Store.WritePid(os.Getpid()); err != nil {
		return err
	}
	defer pr.Store.ClearPid()
	e, err := pr.Engine(os.Stdout)
	if err != nil {
		return err
	}
	return e.Run(ctx)
}

func detach(pr *app.Project) error {
	pid, err := pr.StartRun("")
	if err != nil {
		return err
	}
	fmt.Printf("Build running in the background (pid %d).\n  progress: factory status %s\n  log:      tail -f %s\n", pid, pr.Store.Root, pr.Store.Path("run.log"))
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
	pr := &app.Project{Store: &state.Store{Root: dir}}
	pid, _ := pr.Running()
	if err := pr.Stop(); err != nil {
		return err
	}
	fmt.Printf("Sent stop to pid %d; progress is saved. Resume with: factory run %s --detach\n", pid, dir)
	return nil
}

func cmdRm(args []string) error {
	fs := flag.NewFlagSet("rm", flag.ExitOnError)
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return errors.New("usage: factory rm <name|path> [--yes]")
	}
	dir, err := projectDir(pos)
	if err != nil {
		return err
	}
	pr, err := app.Open(dir, "")
	if err != nil {
		return fmt.Errorf("%s is not a factory project", dir)
	}
	_, running := pr.Running()

	// Deleting is irreversible, so say exactly what will happen before doing
	// it: the path, and that a live build will be stopped as part of it.
	what := fmt.Sprintf("Delete project %q (%s)", pr.P.Name, dir)
	if running {
		what += " and stop its running build"
	}
	if !*yes {
		if !ui.New(os.Stdin, os.Stderr).Confirm(what+"?", false) {
			return errors.New("aborted — nothing deleted")
		}
	}
	if running {
		if err := pr.StopAndWait(10 * time.Second); err != nil {
			return err
		}
	}
	if err := app.Delete(dir); err != nil {
		return err
	}
	fmt.Printf("deleted %s\n", dir)
	return nil
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
