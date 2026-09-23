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
	pending  int // questions awaiting answers in the current batch
	answers  chan string
	batch    chan []string
	closed   chan struct{}
	closeOne sync.Once
}

func NewChat() *Chat {
	return &Chat{
		answers: make(chan string),
		batch:   make(chan []string),
		closed:  make(chan struct{}),
	}
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

// AskMany posts every question of the round at once and blocks until the
// browser submits one answer per question.
//
// The interview used to post one question, wait for its answer, then post the
// next — eight questions meant eight turns, and a project could sit half
// answered for hours because someone had to come back seven more times.
func (c *Chat) AskMany(questions []string, quick ...string) ([]string, error) {
	if len(questions) == 0 {
		return nil, nil
	}
	var qs []Quick
	for _, v := range quick {
		if label, ok := quickLabels[v]; ok {
			qs = append(qs, Quick{Label: label, Value: v})
		}
	}
	c.mu.Lock()
	for i, q := range questions {
		c.msgs = append(c.msgs, Msg{
			ID: len(c.msgs) + 1, Role: "agent",
			Text:  fmt.Sprintf("[%d/%d] %s", i+1, len(questions), q),
			Quick: qs, Time: time.Now().UTC(),
		})
	}
	c.waiting = true
	c.pending = len(questions)
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.waiting, c.pending = false, 0
		c.mu.Unlock()
	}()

	select {
	case ans := <-c.batch:
		c.mu.Lock()
		for i, a := range ans {
			shown := a
			if strings.TrimSpace(shown) == "" {
				shown = "(no preference)"
			}
			c.msgs = append(c.msgs, Msg{ID: len(c.msgs) + 1, Role: "user",
				Text: fmt.Sprintf("[%d/%d] %s", i+1, len(ans), shown), Time: time.Now().UTC()})
		}
		c.mu.Unlock()
		return ans, nil
	case <-c.closed:
		return make([]string, len(questions)), io.EOF
	}
}

// AnswerBatch delivers the browser's whole form for a pending AskMany.
func (c *Chat) AnswerBatch(answers []string) error {
	c.mu.Lock()
	if !c.waiting || c.pending == 0 {
		c.mu.Unlock()
		return ErrNotWaiting
	}
	if len(answers) != c.pending {
		got, want := len(answers), c.pending
		c.mu.Unlock()
		return fmt.Errorf("need %d answers, got %d", want, got)
	}
	c.waiting, c.pending = false, 0
	c.mu.Unlock()

	select {
	case c.batch <- answers:
		return nil
	case <-c.closed:
		return errors.New("the interview has ended")
	case <-time.After(2 * time.Second):
		return ErrNotWaiting
	}
}

// Pending reports how many answers the current batch expects, so the browser
// knows to render a form rather than a single input.
func (c *Chat) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending
}

// Answer delivers the browser's reply to a pending Ask.
func (c *Chat) Answer(text string) error {
	c.mu.Lock()
	waiting, pending := c.waiting, c.pending
	c.mu.Unlock()
	if !waiting {
		return ErrNotWaiting
	}
	// A batch interview wants the whole form: accept a single-line reply when
	// there is exactly one question (so curl examples keep working), and
	// otherwise say precisely what to send.
	if pending > 1 {
		return fmt.Errorf("the interview is waiting for %d answers — post {\"answers\":[…]}", pending)
	}
	if pending == 1 {
		return c.AnswerBatch([]string{text})
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
