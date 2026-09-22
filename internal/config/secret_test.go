package config

import "testing"

func TestSecretEnvByKeyName(t *testing.T) {
	secrets := []string{"CAMEL_API_KEY", "OPENAI_API_KEY", "MY_TOKEN", "DB_PASSWORD",
		"GITHUB_PAT_SECRET", "AWS_SECRET_ACCESS_KEY", "SOME_CREDENTIAL", "PRIVATE_KEY"}
	for _, k := range secrets {
		if !SecretEnv(k, "plain-value") {
			t.Errorf("SecretEnv(%q) = false, want true", k)
		}
	}
	safe := []string{"CAMEL_BASE_URL", "CAMEL_MODEL", "CAMEL_PLATFORM", "PATH", "CAMEL_API_KEY_ENV"}
	for _, k := range safe {
		if SecretEnv(k, "plain-value") {
			t.Errorf("SecretEnv(%q) = true, want false", k)
		}
	}
}

// A credential pasted under an unremarkable key name must still be withheld.
func TestSecretEnvByValue(t *testing.T) {
	cases := map[string]bool{
		"SOME_RANDOM_KEY":    true, // the value below is a live-looking key
		"HARMLESS":           false,
		"MODEL_NAME":         false,
		"WHAT_IS_THIS_THING": true,
	}
	values := map[string]string{
		"SOME_RANDOM_KEY":    "qaml_live_O7bin_iXYmcHQEmlIzqkkN0z5",
		"HARMLESS":           "openai-compatible-model",
		"MODEL_NAME":         "auto",
		"WHAT_IS_THIS_THING": "sk-proj-abc123",
	}
	for key, want := range cases {
		if got := SecretEnv(key, values[key]); got != want {
			t.Errorf("SecretEnv(%q, %q) = %v, want %v", key, values[key], got, want)
		}
	}
}

func TestFieldsReturnsPerFieldErrors(t *testing.T) {
	_, err := Parse([]byte(`{"agents":{"a":{"type":"opencode"}},"roles":{"reviewer":"ghost"}}`), "")
	if err == nil {
		t.Fatal("expected an error")
	}
	fields := Fields(err)
	if len(fields) != 1 {
		t.Fatalf("Fields = %d, want 1: %+v", len(fields), fields)
	}
	if fields[0].Field != "roles.reviewer" || fields[0].Msg == "" {
		t.Fatalf("fields = %+v", fields[0])
	}
}

// The message must still read exactly as it did before fields were introduced;
// callers and tests match on this text.
func TestParseErrorMessageUnchanged(t *testing.T) {
	_, err := Parse([]byte(`{"agents":{"a":{"type":"banana"}},"roles":{"builder":"a","interviewer":"a","planner":"a","reviewer":"a","moderator":"a"}}`), "")
	if err == nil {
		t.Fatal("expected an error")
	}
	want := `agent "a": unknown type "banana" (want opencode, command or openai)`
	if Fields(err)[0].Msg != want {
		t.Fatalf("msg = %q, want %q", Fields(err)[0].Msg, want)
	}
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestUnknownFieldIsReported(t *testing.T) {
	_, err := Parse([]byte(`{"agents":{"a":{"type":"opencode"}},"typo":1}`), "")
	if err == nil {
		t.Fatal("expected an error")
	}
	f := Fields(err)
	if len(f) != 1 || f[0].Field != "typo" {
		t.Fatalf("fields = %+v", f)
	}
}
