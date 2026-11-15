//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import "testing"

func TestDetectPromptPlaceholder(t *testing.T) {
	tests := []struct {
		name      string
		prompt    string
		wantMatch string
	}{
		{
			name:      "uppercase bracket placeholder",
			prompt:    "Draft a strategic acquisition pitch email for Isoform targeting [COMPANY_NAME].",
			wantMatch: "[COMPANY_NAME]",
		},
		{
			name:      "lowercase bracket placeholder",
			prompt:    "Summarize [topic] and deliver a report.",
			wantMatch: "[topic]",
		},
		{
			name:      "mixed-case bracket placeholder",
			prompt:    "Send email to [RecipientName] about the proposal.",
			wantMatch: "[RecipientName]",
		},
		{
			name:      "brace template",
			prompt:    "Generate a pitch for {target} and email it to {user_email}.",
			wantMatch: "{target}",
		},
		{
			name:      "the target phrase",
			prompt:    "Research the target company and prepare a dossier.",
			wantMatch: "the target",
		},
		{
			name:      "the chosen phrase",
			prompt:    "Draft a pitch for the chosen partner based on their public profile.",
			wantMatch: "the chosen",
		},
		{
			name:      "The Target title-cased at sentence start",
			prompt:    "The target must be evaluated against Isoform's criteria.",
			wantMatch: "The target",
		},
		{
			name:      "concrete entity, no placeholder",
			prompt:    "Draft a strategic acquisition pitch email for Isoform targeting Zoom.",
			wantMatch: "",
		},
		{
			name:      "self-contained task with no entity",
			prompt:    "Summarize yesterday's important emails into a daily digest.",
			wantMatch: "",
		},
		{
			name:      "empty prompt",
			prompt:    "",
			wantMatch: "",
		},
		{
			name:      "bracketed number is not a placeholder",
			prompt:    "Review changes from [123] yesterday.",
			wantMatch: "",
		},
		{
			name:      "prose use of 'target' without 'the' is fine",
			prompt:    "Identify acquisition targets from the public market and rank them.",
			wantMatch: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectPromptPlaceholder(tt.prompt)
			if got != tt.wantMatch {
				t.Errorf("detectPromptPlaceholder(%q) = %q, want %q", tt.prompt, got, tt.wantMatch)
			}
		})
	}
}
