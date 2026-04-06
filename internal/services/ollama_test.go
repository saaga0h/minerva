package services

import (
	"testing"

	"github.com/sirupsen/logrus"
)

func newTestOllama() *Ollama {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)
	o := &Ollama{logger: log}
	return o
}

// extractJSON is the critical LLM output scrubber. These tests cover the exact
// patterns we've seen cause parse failures in production.

func TestExtractJSON_CleanJSON(t *testing.T) {
	o := newTestOllama()
	in := `{"domain":"physics","type":"discovery","topic":"black holes"}`
	got := o.extractJSON(in)
	if got != in {
		t.Fatalf("expected unchanged JSON, got: %s", got)
	}
}

func TestExtractJSON_JSONWithLeadingText(t *testing.T) {
	o := newTestOllama()
	in := `Here is your JSON: {"domain":"physics","topic":"test"}`
	got := o.extractJSON(in)
	want := `{"domain":"physics","topic":"test"}`
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

func TestExtractJSON_JSONWithTrailingText(t *testing.T) {
	o := newTestOllama()
	in := `{"domain":"physics"} That's the classification.`
	got := o.extractJSON(in)
	want := `{"domain":"physics"}`
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

func TestExtractJSON_StripsBackslashUnderscore(t *testing.T) {
	o := newTestOllama()
	// LLMs sometimes escape underscores: black\_holes → black_holes
	in := `{"topic":"black\_holes and dark\_matter"}`
	got := o.extractJSON(in)
	want := `{"topic":"black_holes and dark_matter"}`
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

func TestExtractJSON_StripsBackslashHyphen(t *testing.T) {
	o := newTestOllama()
	in := `{"topic":"non\-linear dynamics"}`
	got := o.extractJSON(in)
	want := `{"topic":"non-linear dynamics"}`
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

func TestExtractJSON_StripsOverEscapedAlphanumeric(t *testing.T) {
	o := newTestOllama()
	// LLMs occasionally emit \p, \s, \n-style escapes inside strings
	in := `{"topic":"\physics \stuff"}`
	got := o.extractJSON(in)
	want := `{"topic":"physics stuff"}`
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

func TestExtractJSON_NestedBraces(t *testing.T) {
	o := newTestOllama()
	in := `{"outer":{"inner":"value"}}`
	got := o.extractJSON(in)
	if got != in {
		t.Fatalf("expected unchanged JSON, got: %s", got)
	}
}

func TestExtractJSON_EmptyInput(t *testing.T) {
	o := newTestOllama()
	got := o.extractJSON("")
	// Should return the input unchanged rather than panic.
	if got != "" {
		t.Fatalf("expected empty string, got: %s", got)
	}
}

func TestExtractJSON_NoBraces(t *testing.T) {
	o := newTestOllama()
	// No JSON at all — should return input unchanged without panic.
	got := o.extractJSON("plain text response")
	if got != "plain text response" {
		t.Fatalf("expected original text, got: %s", got)
	}
}

func TestExtractJSON_MultipleObjects_ReturnsFirst(t *testing.T) {
	o := newTestOllama()
	in := `{"first":"yes"} {"second":"no"}`
	got := o.extractJSON(in)
	want := `{"first":"yes"}`
	if got != want {
		t.Fatalf("expected first object only, got: %s", got)
	}
}

// --- ChatFn type sanity ---

func TestChatFn_IsCallable(t *testing.T) {
	called := false
	fn := ChatFn(func(prompt string) (string, error) {
		called = true
		return `{"domain":"test"}`, nil
	})
	resp, err := fn("hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("ChatFn was not called")
	}
	if resp != `{"domain":"test"}` {
		t.Fatalf("unexpected response: %s", resp)
	}
}
