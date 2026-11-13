package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/proactivevoice"
	"yansu-agent/agent"
)

const proactiveVoiceTimeout = 15 * time.Second

var proactiveVoiceAttemptMu sync.Mutex

// Each failure carries a closed category and a count, never evidence, model
// output, addresses, source identifiers or provider error strings.
func logProactiveVoiceFallback(kind string) {
	log.Printf("[proactive-voice] kind=%s count=1", kind)
}

func (a *App) runProactiveVoice(ctx context.Context, job proactivevoice.Job) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, errors.New("late_result")
	}
	if text, ok := proactiveVoicePresence(job); ok {
		defer a.beginPresence(text)()
	}
	a.mu.Lock()
	runner := a.runner
	a.mu.Unlock()
	// Only the configured router guarantees a backend_route callback before
	// any provider sees this job. A direct runner could disclose the evidence
	// before a callback-only check discovered that the route was unsupported.
	router, managed := runner.(*agent.RoutingAgentRunner)
	if !managed || router == nil {
		return nil, errors.New("unavailable")
	}
	encoded, err := json.Marshal(job)
	if err != nil || len(encoded) > proactiveVoiceJobLimit {
		return nil, errors.New("job_limit")
	}
	ctx, cancel := context.WithTimeout(ctx, proactiveVoiceTimeout)
	defer cancel()
	input := &agent.TaskInput{Prompt: string(encoded), SystemText: proactivevoice.SystemPrompt,
		WorkDir: os.TempDir(), Model: a.lightTaskModel(), Ephemeral: true, NoTools: true,
		DenyExternalWrites: true, BackendHint: agent.BackendHintEphemeral,
		Priority: agent.PriorityBackground, SuppressTelemetry: true}
	var output strings.Builder
	routes := 0
	_, err = router.ExecuteTask(ctx, input, func(event *agent.StreamEvent) error {
		if event == nil {
			return errors.New("missing_route")
		}
		if event.Type == "backend_route" {
			routes++
			if routes > 1 {
				return errors.New("retry_disabled")
			}
			switch event.Backend {
			case string(agent.BackendClaudeCode), string(agent.BackendCodex):
			default:
				// An unavailable assistant uses the template. The native loop
				// has a different system wrapper and recovery policy, so it
				// cannot substitute another provider for this bounded job.
				return errors.New("assistant_unavailable")
			}
			return nil
		}
		if routes != 1 {
			return errors.New("missing_route")
		}
		if event.Type == "tool_use" || event.Type == "action_started" {
			return errors.New("unexpected_action")
		}
		if event.Type == "assistant" {
			if output.Len()+len(event.Content) > 32*1024 {
				return errors.New("output_limit")
			}
			output.WriteString(event.Content)
		}
		return nil
	})
	if err != nil {
		return nil, errors.New("model_error")
	}
	if ctx.Err() != nil {
		return nil, errors.New("late_result")
	}
	if routes != 1 {
		return nil, errors.New("missing_route")
	}
	return []byte(output.String()), nil
}

// voiceMainChatCopy only changes the durable chat presentation. The caller's
// engagementText and Moment still drive every doorbell and SMS template.
func (a *App) voiceMainChatCopy(m engagement.Moment, item *mainChatProactive) bool {
	if !proactivevoice.Supported(string(m.Kind)) {
		return true
	}
	a.mu.Lock()
	available := a.runner != nil
	a.mu.Unlock()
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()
	if a.engagement != nil {
		now = a.engagement.now()
	}
	lang := a.currentUILanguage()
	// Producer params and notification text can predate the current source.
	// Start every locale from source-free catalog copy; only validated output
	// may replace it. A safe current source never authorizes older details.
	resetProactiveVoiceToGeneric(item, lang)
	// Outside the hourly batch an activity-driven card (a digest child) keeps
	// its catalog copy: no source read and no model call.
	if !a.activityBatchAllowsVoice(m.Kind, now) {
		logProactiveVoiceFallback("deferred_to_batch")
		return true
	}
	if !m.ExpiresAt.IsZero() && !now.Before(m.ExpiresAt) {
		logProactiveVoiceFallback("late_result")
		resetProactiveVoiceToGeneric(item, lang)
		if m.Kind == engagement.KindWaitingOnReply {
			appendProactiveVoiceCaution(item, lang, proactiveVoiceMailFlag(m.Params["subject"], ""))
		}
		return true
	}
	source, reason := a.loadProactiveVoiceSource(ctx, m, now, lang)
	if reason != "" {
		logProactiveVoiceFallback(reason)
		resetProactiveVoiceToGeneric(item, lang)
		if m.Kind == engagement.KindWaitingOnReply {
			appendProactiveVoiceCaution(item, lang, proactiveVoiceMailFlag(m.Params["subject"], ""))
		}
		return reason != "unsafe_content"
	}
	job, addresses, reason := assembleProactiveVoiceJob(string(m.Kind), lang, true, now, source)
	if reason == "unsafe_content" {
		logProactiveVoiceFallback(reason)
		return false
	}
	if reason != "" {
		logProactiveVoiceFallback(reason)
		appendProactiveVoiceCaution(item, lang, job.MailFlag)
		return true
	}
	if !available {
		logProactiveVoiceFallback("model_unavailable")
		appendProactiveVoiceCaution(item, lang, job.MailFlag)
		return true
	}
	if !a.claimProactiveVoiceAttempt(ctx, m, now) {
		logProactiveVoiceFallback("already_attempted")
		appendProactiveVoiceCaution(item, lang, job.MailFlag)
		return true
	}
	raw, err := a.runProactiveVoice(ctx, job)
	// A failed or late provider call cannot bypass source revocation either.
	// Re-read before selecting model output or the detailed template fallback.
	fresh, freshReason := a.loadProactiveVoiceSource(ctx, m, now, lang)
	freshJob, freshAddresses, assemblyReason := assembleProactiveVoiceJob(string(m.Kind), lang, true, now, fresh)
	freshJob.MomentID = job.MomentID
	if freshReason == "unsafe_content" || assemblyReason == "unsafe_content" {
		logProactiveVoiceFallback("unsafe_content")
		return false
	}
	if freshReason != "" || assemblyReason != "" || !reflect.DeepEqual(job, freshJob) || !reflect.DeepEqual(addresses, freshAddresses) || !reflect.DeepEqual(source.Provenance, fresh.Provenance) {
		logProactiveVoiceFallback("source_changed")
		resetProactiveVoiceToGeneric(item, lang)
		appendProactiveVoiceCaution(item, lang, job.MailFlag)
		return true
	}
	if !m.ExpiresAt.IsZero() && !time.Now().Before(m.ExpiresAt) {
		logProactiveVoiceFallback("late_result")
		resetProactiveVoiceToGeneric(item, lang)
		appendProactiveVoiceCaution(item, lang, job.MailFlag)
		return true
	}
	if err != nil {
		logProactiveVoiceFallback("model_error")
		appendProactiveVoiceCaution(item, lang, job.MailFlag)
		return true
	}
	result, rejection := proactivevoice.Validate(job, raw, fresh.ContactNames)
	if rejection != "" {
		logProactiveVoiceFallback(string(rejection))
		appendProactiveVoiceCaution(item, lang, job.MailFlag)
		return true
	}
	if result.Abstain {
		logProactiveVoiceFallback("abstain")
		appendProactiveVoiceCaution(item, lang, job.MailFlag)
		return true
	}
	if !applyProactiveVoiceResult(item, job, result, addresses) {
		logProactiveVoiceFallback("draft_address")
		appendProactiveVoiceCaution(item, lang, job.MailFlag)
		return true
	}
	populateMainChatSendDraft(item, m, fresh)
	appendProactiveVoiceCaution(item, lang, job.MailFlag)
	// Normalize additive chat controls before binding the stored presentation.
	populateMainChatProactiveCopy(item, nil)
	if !a.saveProactiveVoiceReceipt(ctx, m, now, job, addresses, fresh.Provenance, item) {
		logProactiveVoiceFallback("provenance_unavailable")
		withdrawProactiveVoice(item, lang)
	}
	return true
}

// Claim before provider disclosure, including across a restart after a failed
// chat append. A failed write also uses the template. Never retry a voice job.
func (a *App) claimProactiveVoiceAttempt(ctx context.Context, m engagement.Moment, now time.Time) bool {
	if a.store == nil {
		return false
	}
	key := "proactive_voice_attempt:" + engagement.HashPart(engagementReportKey(m, now))
	proactiveVoiceAttemptMu.Lock()
	defer proactiveVoiceAttemptMu.Unlock()
	previous, err := a.store.GetUIState(ctx, key)
	if err != nil || previous != "" {
		return false
	}
	return a.store.SetUIState(ctx, key, proactivevoice.PromptVersion) == nil
}

func applyProactiveVoiceResult(item *mainChatProactive, job proactivevoice.Job, result proactivevoice.Output, addresses map[string]string) bool {
	var draft *mainChatProactiveDraft
	if result.Draft != nil {
		address := addresses[result.Draft.To]
		if result.Draft.To != job.DraftTo || address == "" {
			return false
		}
		draft = &mainChatProactiveDraft{To: address, Subject: result.Draft.Subject, Text: result.Draft.Text, Communication: communicationDraftProvenance(job, result)}
	}
	item.Headline, item.Body = result.Headline, result.Body
	item.VoiceVersion = proactivevoice.PromptVersion
	item.DraftContent = draft
	item.Draft = draft != nil
	item.ActionIDs = append([]string{}, result.Actions...)
	copy, _ := engagement.Copy(engagement.Kind(job.Kind), job.Locale)
	item.ActionLabels = map[string]string{}
	for _, id := range result.Actions {
		item.ActionLabels[id] = copy.Actions[id]
	}
	if item.Localized == nil {
		item.Localized = map[string]mainChatProactiveCopy{}
	}
	item.Localized[job.Locale] = mainChatProactiveCopy{Headline: item.Headline, Body: item.Body, ActionLabels: item.ActionLabels}
	return true
}

func resetProactiveVoiceToGeneric(item *mainChatProactive, language string) {
	generic := func(lang string) string {
		catalog, _ := engagement.Copy(engagement.Kind(item.Kind), lang)
		return catalog.PushBody
	}
	for lang, copy := range item.Localized {
		copy.Headline, copy.Body = generic(lang), generic(lang)
		item.Localized[lang] = copy
	}
	item.Headline, item.Body = generic(language), generic(language)
}

func appendProactiveVoiceCaution(item *mainChatProactive, lang, flag string) {
	if flag == "" || flag == "none" {
		return
	}
	item.Body = strings.TrimSpace(item.Body) + " " + proactivevoice.CautionLine(lang)
	for language, copy := range item.Localized {
		copy.Body = strings.TrimSpace(copy.Body) + " " + proactivevoice.CautionLine(language)
		item.Localized[language] = copy
	}
}
