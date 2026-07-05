package main

import (
	"strings"
	"testing"
	"time"
)

// normalizeToolResultText moved to internal/agent; see
// TestNormalizeToolResultText there.

func TestBuildMarkdownExport(t *testing.T) {
	exportedAt := time.Date(2026, 5, 5, 9, 30, 0, 0, time.UTC)
	conversation := []exportMessage{
		{role: "user", content: "Find suspicious logins"},
		{role: "assistant", content: "No suspicious logins found."},
		{role: "system", content: "Conversation Memory:\n(empty)"},
	}

	got := buildMarkdownExport(conversation, exportedAt)

	wantParts := []string{
		"# Elastic Security Investigation Export",
		"*Exported on: Tue, 05 May 2026 09:30:00 UTC*",
		"**You:**\nFind suspicious logins",
		"**Assistant:**\nNo suspicious logins found.",
		"**System:**\nConversation Memory:\n(empty)",
	}

	for _, part := range wantParts {
		if !strings.Contains(got, part) {
			t.Fatalf("export missing %q\nfull export:\n%s", part, got)
		}
	}
}

func TestExportFilename(t *testing.T) {
	got := exportFilename(time.Date(2026, 5, 5, 9, 30, 45, 0, time.UTC))
	want := "investigation-export-2026-05-05T09-30-45.md"
	if got != want {
		t.Fatalf("filename = %q, want %q", got, want)
	}
}

func TestNormalizeMarkdownForTerminal(t *testing.T) {
	input := "### Key Observations\nNormal line\n  ## Recommendation\n- bullet"
	got := normalizeMarkdownForTerminal(input)
	want := "Key Observations\nNormal line\n  Recommendation\n- bullet"
	if got != want {
		t.Fatalf("normalized markdown = %q, want %q", got, want)
	}
}
