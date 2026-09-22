// Command greeter prints friendly greetings for people.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/dylan-demolder/factory/internal/greeter"
)

const usage = `greeter — greet people

Usage:
    greeter [flags] [name ...]

With no names it greets the world; otherwise every name gets its own line of
greeting. Flags must come before the names.

Flags:
    -s, --shout              shout the greeting in capitals
    -g, --salutation WORD    the word before the comma (default "Hello")
    -h, --help               show this help and exit

Exit status is 0 on success, 2 when a flag is wrong.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the CLI and returns the process exit status. stdout and
// stderr are parameters so the tests can drive the real argument parsing,
// help text and error handling without spawning a process.
func run(args []string, stdout, stderr io.Writer) int {
	opts := greeter.Options{}
	var help bool

	fs := flag.NewFlagSet("greeter", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	fs.BoolVar(&opts.Shout, "shout", false, "shout the greeting")
	fs.BoolVar(&opts.Shout, "s", false, "shout the greeting")
	fs.StringVar(&opts.Salutation, "salutation", "", "the word before the comma")
	fs.StringVar(&opts.Salutation, "g", "", "the word before the comma")
	fs.BoolVar(&help, "help", false, "show this help and exit")
	fs.BoolVar(&help, "h", false, "show this help and exit")

	if err := fs.Parse(args); err != nil {
		// flag has already written the complaint and the usage to stderr.
		return 2
	}
	if help {
		fmt.Fprint(stdout, usage)
		return 0
	}

	names := fs.Args()
	if len(names) == 0 {
		names = []string{""} // greet the world
	}
	for _, name := range names {
		fmt.Fprintln(stdout, greeter.Greet(name, opts))
	}
	return 0
}
