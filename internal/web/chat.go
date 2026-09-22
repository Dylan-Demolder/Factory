package web

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Msg is one line of the spec interview conversation shown in the browser.
type Msg struct {
	ID    int       `json:"id"`
	Role  string    `json:"role"` // agent | user | system | error
	Text  string    `json:"text"`
	Quick []Quick   `json:"quick,omitempty"`
	Time  time.Time `json:"time"`
}

// Quick is a one-click answer offered with a question.
type Quick struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

var quickLabels = map[string]string{
	"/done": "Skip to the spec",
	"y":     "Approve spec ✓",
}

// Chat implements ui.Asker over HTTP: Ask blocks until the browser posts an
// answer (or the session is closed). It also works as an io.Writer, so
// engine log lines show up in the conversation.
type Chat struct {
	mu       sync.Mutex
	msgs     []Msg
	waiting  bool
	answers  chan string
	closed   chan struct{}
	closeOne sync.Once
}

func NewChat() *Chat {
	return &Chat{answers: make(chan string), closed: make(chan struct{})}
}

var ErrNotWaiting = errors.New("nothing is waiting for an answer right now")

func (c *Chat) add(role, text string, quick []Quick) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, Msg{ID: len(c.msgs) + 1, Role: role, Text: text, Quick: quick, Time: time.Now().UTC()})
}

func (c *Chat) Say(format string, args ...any) {
	text := strings.TrimSpace(fmt.Sprintf(format, args...))
	if text != "" {
		c.add("agent", text, nil)
	}
}

// System adds a status line.
func (c *Chat) System(text string) { c.add("system", text, nil) }

// Error adds an error line.
func (c *Chat) Error(text string) { c.add("error", text, nil) }

func (c *Chat) Ask(question string, quick ...string) (string, error) {
	var qs []Quick
	for _, v := range quick {
		if label, ok := quickLabels[v]; ok {
			qs = append(qs, Quick{Label: label, Value: v})
		}
	}
	c.mu.Lock()
	c.msgs = append(c.msgs, Msg{ID: len(c.msgs) + 1, Role: "agent", Text: question, Quick: qs, Time: time.Now().UTC()})
	c.waiting = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.waiting = false
		c.mu.Unlock()
	}()
	select {
	case a := <-c.answers:
		return a, nil
	case <-c.closed:
		return "", io.EOF
	}
}

// Answer delivers the browser's reply to a pending Ask.
func (c *Chat) Answer(text string) error {
	c.mu.Lock()
	waiting := c.waiting
	c.mu.Unlock()
	if !waiting {
		return ErrNotWaiting
	}
	shown := text
	if strings.TrimSpace(shown) == "" {
		shown = "(no preference)"
	}
	// Record the answer first so it precedes whatever the engine says next.
	c.add("user", shown, nil)
	select {
	case c.answers <- text:
		return nil
	case <-c.closed:
		c.System("answer not delivered: the interview has ended")
		return errors.New("the interview has ended")
	case <-time.After(2 * time.Second):
		c.System("answer not delivered")
		return ErrNotWaiting
	}
}

// Write turns engine log output into system lines.
func (c *Chat) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimSpace(string(p)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			c.System(line)
		}
	}
	return len(p), nil
}

// Close ends the conversation; a pending Ask returns io.EOF.
func (c *Chat) Close() { c.closeOne.Do(func() { close(c.closed) }) }

// Since returns messages with ID > after and whether an answer is awaited.
func (c *Chat) Since(after int) ([]Msg, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if after < 0 {
		after = 0
	}
	if after > len(c.msgs) {
		after = len(c.msgs)
	}
	return append([]Msg(nil), c.msgs[after:]...), c.waiting
}
