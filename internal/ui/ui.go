// Package ui handles the small amount of terminal interaction factory needs
// during the spec interview.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Asker is what the spec interview needs from its human: the terminal
// Prompter and the web interface's chat both implement it.
type Asker interface {
	Say(format string, args ...any)
	// Ask returns the human's answer. If the answer equals one of quick
	// (case-insensitive) it is a shortcut such as "y" or "/done".
	Ask(question string, quick ...string) (string, error)
	// AskMany presents every question of a round at once and returns one
	// answer per question, in order ("" when unanswered).
	//
	// The interview used to ask sequentially: eight questions meant eight
	// separate interactions, each one a turn the human had to take before the
	// next appeared — long enough that a project sat half-answered for hours.
	// One round should cost one interaction.
	AskMany(questions []string, quick ...string) ([]string, error)
}

// Prompter is the terminal Asker.
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
	fmt.Fprintf(p.out, "\n%s\n(end your answer with an empty line)\n> ", question)
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

// AskMany prints every question of a round together and reads one line per
// question, so the whole round costs one block of input instead of N
// round-trips. A quick shortcut ("/done") on any line is returned in that
// slot, which is what lets the caller stop early.
func (p *Prompter) AskMany(questions []string, quick ...string) ([]string, error) {
	if len(questions) == 0 {
		return nil, nil
	}
	fmt.Fprint(p.out, "\n")
	for i, q := range questions {
		fmt.Fprintf(p.out, "[%d/%d] %s\n", i+1, len(questions), q)
	}
	fmt.Fprintf(p.out, "\nOne line per question — blank for no preference, /done to skip to the spec\n")
	answers := make([]string, len(questions))
	for i := range questions {
		fmt.Fprintf(p.out, "%d> ", i+1)
		line, err := p.in.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		for _, q := range quick {
			if strings.EqualFold(strings.TrimSpace(line), q) {
				answers[i] = strings.TrimSpace(line)
				return answers, err
			}
		}
		answers[i] = strings.TrimSpace(line)
		if err != nil {
			// EOF or a read error: keep the answers collected so far and
			// leave the rest empty rather than blocking forever.
			return answers, err
		}
	}
	return answers, nil
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
