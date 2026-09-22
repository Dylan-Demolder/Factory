// Package ui handles the small amount of terminal interaction factory needs
// during the spec interview.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

type Prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func New(in io.Reader, out io.Writer) *Prompter {
	return &Prompter{in: bufio.NewReader(in), out: out}
}

func (p *Prompter) Say(format string, args ...any) {
	fmt.Fprintf(p.out, format+"\n", args...)
}

// Ask prints a question and reads a multi-line answer terminated by an empty
// line. An immediately empty line yields "". io.EOF is returned when input
// ends (along with whatever was read).
//
// If the first line equals one of quick (case-insensitive), it is returned
// immediately without waiting for the terminating empty line.
func (p *Prompter) Ask(question string, quick ...string) (string, error) {
	fmt.Fprintf(p.out, "\n%s\n> ", question)
	var lines []string
	for {
		line, err := p.in.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if len(lines) == 0 {
			for _, q := range quick {
				if strings.EqualFold(strings.TrimSpace(line), q) {
					return strings.TrimSpace(line), err
				}
			}
		}
		if line == "" && (err == nil || len(lines) > 0) {
			return strings.Join(lines, "\n"), err
		}
		if line != "" {
			lines = append(lines, line)
		}
		if err != nil {
			return strings.Join(lines, "\n"), err
		}
		fmt.Fprint(p.out, "  ")
	}
}

// Line reads a single line.
func (p *Prompter) Line(prompt string) (string, error) {
	fmt.Fprint(p.out, prompt)
	line, err := p.in.ReadString('\n')
	return strings.TrimSpace(line), err
}

// Confirm asks a yes/no question.
func (p *Prompter) Confirm(question string, def bool) bool {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	ans, err := p.Line(fmt.Sprintf("%s %s ", question, hint))
	if ans == "" {
		return def && err == nil
	}
	ans = strings.ToLower(ans)
	return ans == "y" || ans == "yes"
}
