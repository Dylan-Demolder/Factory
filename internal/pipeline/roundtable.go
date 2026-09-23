package pipeline

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dylan-demolder/factory/internal/agent"
	"github.com/dylan-demolder/factory/internal/config"
)

// Roundtable runs a structured multi-agent discussion:
//
//	round 1: every participant gives an independent take (in parallel)
//	round 2+: each sees the others' latest positions and responds
//	finally: the moderator synthesises the result in the requested format
//
// The full transcript is saved to .factory/roundtables/. It returns the
// moderator's output.
//
// moderator names a pipeline *role*, not an agent, so that seat's model
// override (role_models) applies to the synthesis.
func (e *Engine) Roundtable(ctx context.Context, slug, topic, material, moderator, synthesis string) (string, error) {
	parts := e.Cfg.Roundtable.Participants
	rounds := e.Cfg.Roundtable.Rounds

	// Seats fan out in parallel. A cap keeps that fan-out inside whatever the
	// provider actually allows concurrently — without it, a panel larger than
	// the caller's stream allowance queues against itself (or gets throttled)
	// purely because factory asked everyone the same question at once.
	var sem chan struct{}
	capped := false
	if limit := e.Cfg.Limits.MaxParallelParticipants; limit > 0 && limit < len(parts) {
		sem = make(chan struct{}, limit)
		capped = true
	}
	var log strings.Builder
	fmt.Fprintf(&log, "# Roundtable: %s\n\n_%s_\n\n## Topic\n%s\n\n", slug, time.Now().Format(time.RFC1123), topic)

	latest := make([]string, len(parts))
	if len(parts) > 0 {
		if capped {
			e.logf("  roundtable %q: %d participants, %d rounds, %d at a time", slug, len(parts), rounds, cap(sem))
		} else {
			e.logf("  roundtable %q: %d participants, %d rounds", slug, len(parts), rounds)
		}
	}
	for r := 1; r <= rounds && len(parts) > 0; r++ {
		prev := append([]string(nil), latest...)
		var wg sync.WaitGroup
		var mu sync.Mutex
		errs := 0
		for i, pt := range parts {
			wg.Add(1)
			go func(i int, pt config.Participant) {
				defer wg.Done()
				if sem != nil {
					sem <- struct{}{}
					defer func() { <-sem }()
				}
				prompt := participantPrompt(pt, i, parts, topic, material, r, prev)
				out, err := e.call(ctx, pt.Agent, agent.Request{
					Stage:    "roundtable-" + slug,
					System:   "You are a member of a product team roundtable. Your perspective: " + pt.Persona,
					Prompt:   prompt,
					ReadOnly: true,
				})
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					errs++
					latest[i] = fmt.Sprintf("(unavailable this round: %v)", err)
					return
				}
				latest[i] = out
			}(i, pt)
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if errs == len(parts) {
			e.logf("  roundtable %q: every participant failed in round %d; moderator continues alone", slug, r)
		}
		fmt.Fprintf(&log, "## Round %d\n\n", r)
		for i, pt := range parts {
			fmt.Fprintf(&log, "### %s — %s\n\n%s\n\n", seatName(i, pt), pt.Persona, latest[i])
		}
	}

	var discussion strings.Builder
	for i, pt := range parts {
		fmt.Fprintf(&discussion, "### %s (%s)\n%s\n\n", seatName(i, pt), pt.Persona, latest[i])
	}
	if len(parts) == 0 {
		discussion.WriteString("(no panel configured — use your own judgement)\n")
	}
	final, err := e.callRole(ctx, moderator, agent.Request{
		Stage:    "roundtable-" + slug + "-synthesis",
		System:   "You moderate a product team roundtable and turn the discussion into a decision.",
		Prompt:   fmt.Sprintf("## Topic\n%s\n\n## Material\n%s\n\n## Final positions of the panel\n%s\n## Your job\n%s", topic, material, discussion.String(), synthesis),
		ReadOnly: true,
	})
	if err != nil {
		return "", fmt.Errorf("roundtable %s: moderator: %w", slug, err)
	}
	fmt.Fprintf(&log, "## Moderator synthesis (%s)\n\n%s\n", e.roleLabel(moderator), final)
	e.saveTranscript(slug, log.String())
	return final, nil
}

func seatName(i int, pt config.Participant) string {
	return fmt.Sprintf("Seat %d (%s)", i+1, pt.Agent)
}

func participantPrompt(pt config.Participant, idx int, all []config.Participant, topic, material string, round int, prev []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Topic\n%s\n\n## Material\n%s\n\n", topic, material)
	if round == 1 {
		b.WriteString("## Your turn\nGive your independent assessment from your perspective. Be specific and concrete, prioritise what matters most, and disagree with the material where warranted. Do not modify any files. Keep it under 400 words.")
		return b.String()
	}
	b.WriteString("## Your previous position\n" + prev[idx] + "\n\n## The other panelists said\n")
	for i, p := range prev {
		if i != idx {
			fmt.Fprintf(&b, "### %s (%s)\n%s\n\n", seatName(i, all[i]), all[i].Persona, p)
		}
	}
	b.WriteString("## Your turn\nRespond to the others: where you agree, where you disagree and why, what is still missing. Then state your updated position as a short prioritised list. Do not modify any files. Keep it under 350 words.")
	return b.String()
}

func (e *Engine) saveTranscript(slug, content string) {
	entries, _ := os.ReadDir(e.Store.Path("roundtables"))
	name := fmt.Sprintf("roundtables/%03d-%s.md", len(entries)+1, sanitize(slug))
	if err := e.Store.Write(name, content); err != nil {
		e.logf("  could not save transcript: %v", err)
	}
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '-'
	}, s)
}
