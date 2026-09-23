package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// Roundtable participants fan out together, and a provider that allows only a
// couple of concurrent requests needs the cap to be configurable.
func TestMaxParallelParticipants(t *testing.T) {
	base := `{"agents":{"a":{"type":"opencode"}},"limits":%s}`

	cases := map[string]struct {
		in   string
		want int
	}{
		"absent (all at once)": {in: `{}`, want: 0},
		"explicit cap":         {in: `{"max_parallel_participants": 2}`, want: 2},
		"larger than a panel":  {in: `{"max_parallel_participants": 10}`, want: 10},
		"negative is clamped":  {in: `{"max_parallel_participants": -5}`, want: 0},
	}
	for name, c := range cases {
		cfg, err := Parse([]byte(strings.Replace(base, "%s", c.in, 1)), "")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got := cfg.Limits.MaxParallelParticipants; got != c.want {
			t.Errorf("%s: got %d, want %d", name, got, c.want)
		}
	}
}

// A cap must survive a round trip, or editing and saving would silently drop it.
func TestMaxParallelParticipantsRoundTrips(t *testing.T) {
	in := `{"agents":{"a":{"type":"opencode"}},"limits":{"max_interview_rounds":3,"max_task_attempts":4,"max_acceptance_rounds":3,"agent_retries":2,"agent_timeout":"30m","test_timeout":"15m","max_parallel_participants":2}}`
	cfg, err := Parse([]byte(in), "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg2, err := Parse(out, "")
	if err != nil {
		t.Fatalf("re-parsing our own output failed: %v\n%s", err, out)
	}
	if cfg2.Limits.MaxParallelParticipants != 2 {
		t.Errorf("cap after round trip = %d, want 2", cfg2.Limits.MaxParallelParticipants)
	}
}
