// Package extract pulls structured data out of free-form LLM responses.
package extract

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var fenceRe = regexp.MustCompile("(?s)```([A-Za-z0-9_-]*)[ \t]*\r?\n(.*?)```")

// JSON finds a JSON value in text and decodes it into v. It prefers fenced
// ```json blocks (last one first, since models usually reason before
// answering), then any fenced block, then bare top-level objects/arrays.
func JSON(text string, v any) error {
	var firstErr error
	for _, c := range Candidates(text) {
		err := json.Unmarshal([]byte(c), v)
		if err == nil {
			return nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		return errors.New("no JSON found in response")
	}
	return fmt.Errorf("invalid JSON in response: %w", firstErr)
}

// Candidates returns possible JSON payloads in priority order.
func Candidates(text string) []string {
	var jsonFences, otherFences []string
	for _, m := range fenceRe.FindAllStringSubmatch(text, -1) {
		body := strings.TrimSpace(m[2])
		if strings.EqualFold(m[1], "json") {
			jsonFences = append(jsonFences, body)
		} else {
			otherFences = append(otherFences, body)
		}
	}
	var out []string
	for i := len(jsonFences) - 1; i >= 0; i-- {
		out = append(out, jsonFences[i])
	}
	for i := len(otherFences) - 1; i >= 0; i-- {
		out = append(out, otherFences[i])
	}
	bare := balanced(text)
	for i := len(bare) - 1; i >= 0; i-- {
		out = append(out, bare[i])
	}
	return out
}

// BeforeLastJSONFence returns the text preceding the last ```json fence, or
// the whole text if there is none. Used to split "markdown + json" replies.
func BeforeLastJSONFence(text string) string {
	idx := -1
	for _, loc := range fenceRe.FindAllStringSubmatchIndex(text, -1) {
		if strings.EqualFold(text[loc[2]:loc[3]], "json") {
			idx = loc[0]
		}
	}
	if idx < 0 {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(text[:idx])
}

// balanced finds top-level {...} and [...] spans, respecting JSON strings.
func balanced(text string) []string {
	var out []string
	for i := 0; i < len(text); i++ {
		if text[i] != '{' && text[i] != '[' {
			continue
		}
		if end := matchEnd(text, i); end > i {
			out = append(out, text[i:end+1])
			i = end
		}
	}
	return out
}

func matchEnd(text string, start int) int {
	var stack []byte
	inStr, esc := false, false
	for i := start; i < len(text); i++ {
		c := text[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) == 0 || stack[len(stack)-1] != c {
				return -1
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return i
			}
		}
	}
	return -1
}
