package greeter

import "testing"

func TestGreet(t *testing.T) {
	tests := []struct {
		desc string
		name string
		opts Options
		want string
	}{
		{"no name greets the world", "", Options{}, "Hello, World!"},
		{"whitespace name greets the world", "   \t ", Options{}, "Hello, World!"},
		{"single name", "Ada", Options{}, "Hello, Ada!"},
		{"name is trimmed", "  Ada Lovelace  ", Options{}, "Hello, Ada Lovelace!"},
		{"case is left alone", "aDa", Options{}, "Hello, aDa!"},
		{"non-ascii name", "Åsa", Options{}, "Hello, Åsa!"},
		{"shout upper-cases everything", "Ada", Options{Shout: true}, "HELLO, ADA!"},
		{"shout with no name", "", Options{Shout: true}, "HELLO, WORLD!"},
		{"shout keeps non-ascii capitals", "åsa", Options{Shout: true}, "HELLO, ÅSA!"},
		{"custom salutation", "Ada", Options{Salutation: "Hi"}, "Hi, Ada!"},
		{"blank salutation falls back to the default", "Ada", Options{Salutation: "  "}, "Hello, Ada!"},
		{"custom salutation is trimmed", "Ada", Options{Salutation: " Howdy "}, "Howdy, Ada!"},
		{"shout with a custom salutation", "Ada", Options{Salutation: "Hi", Shout: true}, "HI, ADA!"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if got := Greet(tc.name, tc.opts); got != tc.want {
				t.Errorf("Greet(%q, %+v) = %q, want %q", tc.name, tc.opts, got, tc.want)
			}
		})
	}
}
