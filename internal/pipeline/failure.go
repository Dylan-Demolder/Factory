package pipeline

import (
	"strconv"
	"strings"

	"github.com/dylan-demolder/factory/internal/state"
)

// failureSignature reduces a failure to the one line that explains it, so the
// same mistake can be recognised across attempts.
//
// This exists because of a real run: attempt 1 failed with
// `cannot use os.Open ... as func(string) (io.ReadCloser, error)`, attempt 2
// fixed it, and attempt 3 reintroduced exactly the same line. Treated as
// unrelated failures the builder was handed the same impossible brief again
// and again; seen as a repeat, it is a regression worth naming.
func failureSignature(output string) string {
	markers := []string{
		"cannot ", "undefined:", "--- fail", "fail\t", "panic:",
		"error:", "expected", "no such",
	}
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		lower := strings.ToLower(l)
		for _, m := range markers {
			if strings.Contains(lower, m) {
				return normalizeSig(l)
			}
		}
	}
	// Nothing recognisable: fall back to the first line of substance.
	for _, line := range lines {
		if l := strings.TrimSpace(line); l != "" {
			return normalizeSig(l)
		}
	}
	return ""
}

// normalizeSig lower-cases, strips digits and clamps length. Digits go
// because paths and line numbers vary between attempts while the underlying
// mistake does not: "./run.go:17" vs "./run.go:42" is the same error.
func normalizeSig(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}

// mostRepeated returns the signature that has occurred most often and how
// many times, ignoring signatures that never repeated.
func mostRepeated(failures []state.Failure) (string, int) {
	counts := map[string]int{}
	order := []string{}
	for _, f := range failures {
		if f.Sig == "" {
			continue
		}
		if _, seen := counts[f.Sig]; !seen {
			order = append(order, f.Sig)
		}
		counts[f.Sig]++
	}
	best, n := "", 0
	for _, sig := range order {
		if counts[sig] > n {
			best, n = sig, counts[sig]
		}
	}
	return best, n
}

// failureHistory renders a task's failures for the re-brief material, so the
// panel sees where the previous brief led rather than re-deriving it.
func failureHistory(t *state.Task) string {
	var b strings.Builder
	for _, f := range t.Failures {
		b.WriteString("- attempt ")
		b.WriteString(strconv.Itoa(f.Attempt))
		b.WriteString(": `")
		b.WriteString(f.Sig)
		b.WriteString("`\n")
	}
	return b.String()
}

// repeatNote is prepended to feedback when the same signature has already
// occurred, naming the attempts so the builder can see it is regressing.
func repeatNote(failures []state.Failure, sig string) string {
	count, at := 0, []int{}
	for _, f := range failures {
		if f.Sig == sig {
			count++
			at = append(at, f.Attempt)
		}
	}
	if count < 2 {
		return ""
	}
	positions := make([]string, len(at))
	for i, a := range at {
		positions[i] = strconv.Itoa(a)
	}
	return "**This is the same failure as before — it has now happened " +
		strconv.Itoa(count) + " times (attempts " + strings.Join(positions, ", ") +
		").** A repeat means the previous fix did not hold or was reverted: " +
		"re-read the error, find the root cause, and do not re-apply the " +
		"approach that produced it.\n\n"
}
