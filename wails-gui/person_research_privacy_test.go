package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"wails-gui/internal/pii"
	"wails-gui/storage"
	"yansu-agent/agent"
)

func TestPersonResearchRedactsSynthesisPrompt(t *testing.T) {
	seed := researchSeed{Names: "Person " + activityPrivacySecret, Handles: "- Slack: " + activityPrivacySecret,
		Recent: "Message " + activityPrivacySecret, Stats: activityPrivacySecret, Extra: activityPrivacySecret}
	prompt := personResearchPrompt(seed, "English")
	if strings.Contains(prompt, activityPrivacySecret) || !strings.Contains(prompt, "[REDACTED:api_key]") {
		t.Errorf("synthesis leaks local data: %s", prompt)
	}
}

func TestPersonResearchSeedFiltersSensitiveAppsAndRedactsStoredMessages(t *testing.T) {
	isolateActivityPrivacyTest(t)
	pii.AddUserSensitiveApp("com.tinyspeck.slackmacgap", "Private localized Slack")
	app := newBindingsTestApp(t)
	p, err := app.store.CreatePrincipal(t.Context(), storage.PrincipalRow{Kind: storage.PrincipalKindContact, DisplayName: "Person " + activityPrivacySecret})
	if err != nil {
		t.Fatal(err)
	}
	for i, source := range []struct{ app, text string }{
		{"slack", "private-app-message"},
		{"com.1password.desktop", "password-app-message"},
		{"discord", "public-message " + activityPrivacySecret},
	} {
		if _, err := app.store.BindAppHandle(t.Context(), p.ID, source.app, source.text, source.text); err != nil {
			t.Fatal(err)
		}
		if err := app.store.BumpPrincipalSighting(t.Context(), p.ID, source.app, "", int64(1000+i), source.text); err != nil {
			t.Fatal(err)
		}
	}
	seed := app.buildResearchSeed(t.Context(), p, activityPrivacySecret)
	got := fmt.Sprintf("%+v", seed)
	for _, secret := range []string{activityPrivacySecret, "private-app-message", "password-app-message", "Slack", "1password"} {
		if strings.Contains(got, secret) {
			t.Errorf("research seed contains forbidden source %q: %s", secret, got)
		}
	}
	if !strings.Contains(seed.Recent, "public-message [REDACTED:api_key]") || !strings.Contains(seed.Stats, "Discord: 1 messages") {
		t.Fatalf("allowed evidence must survive redaction: %+v", seed)
	}
}

type modelPrivacyTaskRecorder struct {
	memoryTranslationFakeRunner
	inputs []agent.TaskInput
}

func (r *modelPrivacyTaskRecorder) ExecuteTask(ctx context.Context, input *agent.TaskInput, callback agent.StreamCallback) (string, error) {
	r.inputs = append(r.inputs, *input)
	return r.memoryTranslationFakeRunner.ExecuteTask(ctx, input, callback)
}

func TestPersonResearchModelCallsRedactAndUseBackgroundPriority(t *testing.T) {
	isolateActivityPrivacyTest(t)
	runner := &modelPrivacyTaskRecorder{memoryTranslationFakeRunner: memoryTranslationFakeRunner{
		response: "Person " + activityPrivacySecret, ready: true,
	}}
	app := newBindingsTestApp(t)
	app.runner = runner
	p, err := app.store.CreatePrincipal(t.Context(), storage.PrincipalRow{Kind: storage.PrincipalKindContact, DisplayName: "Person"})
	if err != nil {
		t.Fatal(err)
	}
	runner.response = "<summary>Known person</summary><dossier>A known person.</dossier>"
	app.runPersonResearch(p, activityPrivacySecret)
	if len(runner.inputs) != 1 {
		t.Fatalf("model calls = %d, want one synthesis", len(runner.inputs))
	}
	for _, input := range runner.inputs {
		if strings.Contains(input.Prompt, activityPrivacySecret) || !strings.Contains(input.Prompt, "[REDACTED:api_key]") {
			t.Errorf("model input leaked local signals: %s", input.Prompt)
		}
		if input.Priority != agent.PriorityBackground {
			t.Errorf("model priority = %q, want background", input.Priority)
		}
	}
}
