package conv

import (
	"strings"
	"testing"

	"github.com/zendev-sh/goai/provider"
)

func textMsg(role provider.Role, text string) provider.Message {
	return provider.Message{
		Role:    role,
		Content: []provider.Part{{Type: provider.PartText, Text: text}},
	}
}

func TestPrune(t *testing.T) {
	mk := func(n int) []provider.Message {
		h := make([]provider.Message, n)
		for i := range h {
			h[i] = textMsg(provider.RoleUser, string(rune('a'+i)))
		}
		return h
	}

	t.Run("under limit is unchanged", func(t *testing.T) {
		in := mk(3)
		out := Prune(in, 5)
		if len(out) != 3 {
			t.Fatalf("got %d messages, want 3", len(out))
		}
	})

	t.Run("over limit keeps trailing window", func(t *testing.T) {
		in := mk(20)
		out := Prune(in, MaxMessages)
		if len(out) != MaxMessages {
			t.Fatalf("got %d messages, want %d", len(out), MaxMessages)
		}
		// Must keep the most recent messages, not the oldest.
		if got := out[len(out)-1].Content[0].Text; got != in[len(in)-1].Content[0].Text {
			t.Fatalf("last message = %q, want %q", got, in[len(in)-1].Content[0].Text)
		}
	})

	t.Run("non-positive max is a no-op", func(t *testing.T) {
		in := mk(4)
		if out := Prune(in, 0); len(out) != 4 {
			t.Fatalf("max=0 got %d, want 4", len(out))
		}
	})

	t.Run("does not mutate input", func(t *testing.T) {
		in := mk(10)
		_ = Prune(in, 3)
		if len(in) != 10 {
			t.Fatalf("input mutated: len = %d, want 10", len(in))
		}
	})
}

func TestRenderText(t *testing.T) {
	history := []provider.Message{
		textMsg(provider.RoleUser, "list alerts"),
		textMsg(provider.RoleAssistant, "here they are"),
		// tool-only message should not appear in the transcript
		{Role: provider.RoleAssistant, Content: []provider.Part{{Type: provider.PartToolCall, ToolName: "search"}}},
	}
	out := RenderText(history)
	if !strings.Contains(out, "Human: list alerts") {
		t.Errorf("missing human turn:\n%s", out)
	}
	if !strings.Contains(out, "AI: here they are") {
		t.Errorf("missing AI turn:\n%s", out)
	}
	if strings.Contains(out, "search") {
		t.Errorf("tool call leaked into transcript:\n%s", out)
	}
}

func TestSummarizeForLog(t *testing.T) {
	history := []provider.Message{textMsg(provider.RoleUser, "hello")}
	out := SummarizeForLog(history)
	// Valid JSON-ish summary containing role and part metadata.
	for _, want := range []string{`"role":"user"`, `"type":"text"`, `"chars":5`} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}
