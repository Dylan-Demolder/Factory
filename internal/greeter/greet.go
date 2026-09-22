// Package greeter builds the greeting strings printed by the greeter CLI.
package greeter

import "strings"

// Defaults used when the caller doesn't say otherwise.
const (
	// DefaultSalutation is the word before the comma.
	DefaultSalutation = "Hello"
	// DefaultName is who gets greeted when nobody was named.
	DefaultName = "World"
)

// Options tune the wording of a greeting.
type Options struct {
	// Salutation is the word before the comma, e.g. "Hello" or "Hi".
	// Blank (or whitespace) means DefaultSalutation.
	Salutation string
	// Shout upper-cases the whole greeting.
	Shout bool
}

// Greet returns a greeting such as "Hello, Ada!".
//
// A name that is blank or only whitespace greets DefaultName instead, so an
// empty argument still says something friendly. Names are trimmed, but their
// case is left alone unless Shout is set.
func Greet(name string, opts Options) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultName
	}
	salutation := strings.TrimSpace(opts.Salutation)
	if salutation == "" {
		salutation = DefaultSalutation
	}
	greeting := salutation + ", " + name + "!"
	if opts.Shout {
		greeting = strings.ToUpper(greeting)
	}
	return greeting
}
