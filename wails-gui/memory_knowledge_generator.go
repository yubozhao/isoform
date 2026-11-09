package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wails-gui/internal/pii"
	"yansu-agent/agent"
)

// ── Knowledge generation from conversations (parallel to memory) ────

// generateAndSaveKnowledge runs knowledge extraction and saves the result.
// Called as a goroutine when triage determines worthKnowledge=true.
//
// traceID carries the upstream agentloop correlation id when the run is part
// of a user-initiated chat chain. Pass "" for background connector scans —
// runKnowledgeGeneration then suppresses agentloop.* telemetry.
func (mc *MemoryGenerator) generateAndSaveKnowledge(
	ctx context.Context,
	source memorySource,
	sourceID, summary, traceID string,
) {
	_ = mc.generateAndSaveKnowledgeDetailed(
		ctx,
		source,
		sourceID,
		summary,
		traceID,
	)
}

func (mc *MemoryGenerator) generateAndSaveKnowledgeDetailed(
	ctx context.Context,
	source memorySource,
	sourceID, summary, traceID string,
) []memoryPipelineMaterialization {
	knowledge, err := mc.runKnowledgeGeneration(ctx, summary, traceID)
	if err != nil {
		log.Printf("[memory] knowledge generation failed for %s:%s: %v", source, sourceID, err)
		return nil
	}

	if len(knowledge.KnowledgePoints) == 0 {
		log.Printf("[memory] knowledge generation returned no points for %s:%s, skipping", source, sourceID)
		return nil
	}

	action := knowledge.Action
	if action == "" {
		action = "create"
	}

	logDecision := func(entryID, decision, reason string) {
		logTriageEntry(TriageLogEntry{Source: "knowledge", ID: sourceID, EntryID: entryID,
			Decision: decision, Reason: reason, Title: knowledge.Title, Description: knowledge.Description})
	}

	switch action {
	case "skip":
		log.Printf("[memory] knowledge generation returned skip for %s:%s — duplicate", source, sourceID)
		logDecision("", "dedup_skip", fmt.Sprintf("title=%q", knowledge.Title))
	case "update":
		if knowledge.UpdateID != "" {
			entryID, detailedAction, err := mc.updateKnowledgeFileDetailed(
				source,
				sourceID,
				knowledge,
			)
			if err != nil {
				return nil
			}
			mc.app.linkInitiativeArtifact(string(source), sourceID, "knowledge", entryID)
			logDecision(entryID, "dedup_update", fmt.Sprintf("updateId=%s points=%d title=%q", entryID, len(knowledge.KnowledgePoints), knowledge.Title))
			return []memoryPipelineMaterialization{{
				ArtifactKind: BackgroundArtifactKnowledge,
				ArtifactID:   entryID,
				Action:       detailedAction,
				Title:        knowledge.Title,
			}}
		} else {
			entryID := mc.saveKnowledgeFile(source, sourceID, knowledge)
			mc.app.linkInitiativeArtifact(string(source), sourceID, "knowledge", entryID)
			logDecision(entryID, "saved", fmt.Sprintf("points=%d title=%q (update without id)", len(knowledge.KnowledgePoints), knowledge.Title))
			if entryID != "" {
				return []memoryPipelineMaterialization{{
					ArtifactKind: BackgroundArtifactKnowledge,
					ArtifactID:   entryID,
					Action:       BackgroundMaterializationCreated,
					Title:        knowledge.Title,
				}}
			}
		}
	default: // "create"
		entryID := mc.saveKnowledgeFile(source, sourceID, knowledge)
		mc.app.linkInitiativeArtifact(string(source), sourceID, "knowledge", entryID)
		logDecision(entryID, "saved", fmt.Sprintf("points=%d title=%q", len(knowledge.KnowledgePoints), knowledge.Title))
		if mc.app.ctx != nil && entryID != "" {
			mc.app.emitBrowserEvent("insight:saved", map[string]string{
				"kind":   "knowledge",
				"id":     entryID,
				"title":  knowledge.Title,
				"source": string(source),
			})
		}
		if entryID != "" {
			return []memoryPipelineMaterialization{{
				ArtifactKind: BackgroundArtifactKnowledge,
				ArtifactID:   entryID,
				Action:       BackgroundMaterializationCreated,
				Title:        knowledge.Title,
			}}
		}
	}

	log.Printf("[memory] %s %d knowledge points for %s:%s", action, len(knowledge.KnowledgePoints), source, sourceID)
	return nil
}

// runKnowledgeGeneration uses a stronger model to extract reusable knowledge.
func (mc *MemoryGenerator) runKnowledgeGeneration(ctx context.Context, summary, traceID string) (*knowledgeGenerationResponse, error) {
	mc.app.mu.Lock()
	runner := mc.app.runner
	mc.app.mu.Unlock()

	if runner == nil {
		return nil, fmt.Errorf("runner not initialized")
	}

	// Load existing knowledge for dedup context
	provenance := newKnowledgeGenerationProvenance()
	existingKnowledge := mc.app.loadModelDedupSummaryWithCapture(nil, "", 100_000, 0, MemorySourceKnowledge, provenance.addNote)
	var existingSection string
	if existingKnowledge != "" {
		existingSection = fmt.Sprintf(`
## Existing knowledge entries (IMPORTANT — check for duplicates!)

The following knowledge entries already exist. Before creating a new entry:
1. If the new data is essentially the SAME as an existing entry (same topic, same insights), return action="skip".
2. If the new data UPDATES or EXPANDS an existing entry (same topic but new/changed details), return action="update" with the updateId set to the existing entry's id.
3. Only return action="create" if this is genuinely NEW information not covered by any existing entry.

%s`, existingKnowledge)
	}

	languageInstruction := responseLanguageInstruction(mc.app.currentUILanguageName())
	prompt := fmt.Sprintf(`%s

Extract reusable knowledge from this conversation. If none exists, return an empty array — that is the expected outcome for most conversations.

Conversation data:
---
%s
---
%s
Respond with ONLY a JSON object (no markdown, no code fences):
{
  "action": "create|update|skip",
  "kind": "decision|how_to|fix|gotcha|reference",
  "updateId": "existing-entry-id-if-updating",
  "title": "concise title summarizing the knowledge",
  "description": "brief context about what led to this knowledge",
  "knowledgePoints": ["knowledge point 1", "knowledge point 2"]
}

## kind field rules:

Decision rationale -> decision; process knowledge -> how_to; failure patterns and technical insights pairing an error with its workaround -> fix; environment/infra quirks and vendor/API gotchas -> gotcha; people/org, domain/business and other technical facts -> reference. Always return one valid kind. For updates keep the existing kind unless the content warrants a different valid kind.

## action field rules:

- "skip": The data duplicates existing knowledge with no new information. Return empty knowledgePoints.
- "update": The data relates to an existing knowledge entry but has new or updated details. Set updateId to the id of the entry to update (from the [id=xxx] tag in existing knowledge). Return the COMPLETE updated knowledgePoints (merged old + new).
- "create": The data contains genuinely NEW knowledge not covered by any existing entry.

## What is knowledge?

Knowledge is NOT a diary, NOT a memory, NOT a summary of what happened. It is a reusable, atomic, valuable insight that someone can APPLY in a future situation.

Think of knowledge as a reference card: when someone encounters a specific problem in the future, they search their knowledge base, find this card, and it directly helps them solve or avoid the problem.

## Knowledge categories

Extract insights from ANY of these categories:

1. **Technical insights** — specific error+workaround, API behavior, tool configuration, code pattern
   ✅ "golangci-lint v1.55+ requires Go 1.21; running on Go 1.20 produces a misleading 'no Go files' error"
   ✅ "Slack mrkdwn uses *bold* not **bold**, and links are <url|text> not [text](url)"

2. **People & org knowledge** — who owns what, who is the expert, team responsibilities
   ✅ "Alice owns the billing service; Jake is the go-to for Kubernetes issues"
   ✅ "Design reviews happen Thursdays with the APAC team"

3. **Process knowledge** — deploy procedures, approval workflows, release steps — tribal knowledge
   ✅ "Hotfixes require a Slack post in #incidents before merging"
   ✅ "To deploy to staging, merge to release/* branch, not main"

4. **Domain/business knowledge** — business rules, constraints, metric definitions
   ✅ "Enterprise customers on Legacy plan can't use SSO — licensing constraint, not technical"
   ✅ "ARR numbers are reported on trailing 4-week basis, not calendar month"

5. **Failure patterns** — what went wrong, root cause, fix — distilled post-mortems
   ✅ "Stripe webhook retry storm was caused by 502 during deploy — need zero-downtime for /webhook route"

6. **Environment/infra quirks** — CI limits, shared resources, platform behaviors
   ✅ "CI runner has 4GB RAM, Go tests with -race on full suite will OOM — split into packages"
   ✅ "Production Redis is shared between sessions and job queue — FLUSHDB is catastrophic"

7. **Vendor/API gotchas** — undocumented limits, misleading errors, doc vs reality
   ✅ "Slack message limit is ~3000 chars, not 4000 as some docs say"

8. **Decision rationale** — WHY a technology or approach was chosen over alternatives
   ✅ "Chose Asynq over Temporal: job patterns are simple fan-out, Temporal's operational overhead wasn't justified for <10 job types"

## Three mandatory properties

1. **Reusable** — The insight helps anyone in a similar situation, not just in this specific conversation's context.

2. **Atomic** — One point = one discrete insight. Each stands alone as a self-contained fact.

3. **Valuable** — Reading this changes what someone DOES. It reveals something non-obvious or hard-won.

## The litmus test

For each candidate, ask: "Would someone searching for this topic be glad they found it?"

- YES → keep it.
- NO → it's a diary entry or a truism. Discard.

## Output rules

- Default: return empty knowledgePoints []. This is correct for most conversations.
- Each point MUST contain at least one specific detail (name, version, command, rule, constraint, or code pattern).
- If you cannot point to a specific detail from the conversation to justify the point, discard it.
- One genuine insight is better than five mediocre observations.
- NEVER pad output with generic advice, summaries, or truisms.

CRITICAL: Do NOT ask the user any questions. Respond immediately with the JSON object.`, languageInstruction, summary, existingSection)

	var result strings.Builder

	prompt, _ = pii.RedactText(prompt)
	log.Printf("[memory] knowledge generation ExecuteTask starting")
	// Chat-tier model: automatic background extraction; see runGeneration.
	_, err := runner.ExecuteTask(ctx, &agent.TaskInput{
		Priority:          agent.PriorityBackground,
		AutoApproveTools:  true,
		Prompt:            prompt,
		Model:             claudeCodeModelConfig().ChatModel,
		PlanMode:          true,
		Ephemeral:         true,
		NoTools:           true,
		NoSystem:          true,
		TraceID:           traceID,
		SuppressTelemetry: traceID == "",
	}, func(event *agent.StreamEvent) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		switch event.Type {
		case "assistant":
			result.WriteString(event.Content)
		case "error":
			log.Printf("[memory] knowledge generation stream error: %s", event.Error)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("execute knowledge generation: %w", err)
	}

	raw := strings.TrimSpace(result.String())
	raw = stripCodeFences(raw)

	var resp knowledgeGenerationResponse
	if err := unmarshalJSONBestEffort(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse knowledge generation response: %w", err)
	}

	resp.provenance = provenance
	return &resp, nil
}

// ── Knowledge file persistence ──────────────────────────────────────

// saveKnowledgeFile persists extracted knowledge to ~/.yansu-agent/knowledge/.
// saveKnowledgeFile writes a knowledge entry to disk and returns its ID
// (filename without the .md extension), or an empty string on failure.
func (mc *MemoryGenerator) saveKnowledgeFile(source memorySource, sourceID string, knowledge *knowledgeGenerationResponse) string {
	dir, err := knowledgeDir()
	if err != nil {
		log.Printf("[memory] failed to get knowledge dir: %v", err)
		return ""
	}

	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%s", source, sourceID)))
	entryID := fmt.Sprintf("%s-%x", source, hash[:8])
	filename := entryID + ".md"

	filePath := filepath.Join(dir, filename)
	if existing, ok := relearnKnowHowWithEvidence(dir, knowledge.Title, entryID, KnowHowMoment{At: time.Now().UnixMilli()}, knowledge.KnowledgePoints, string(source), nil, knowledge.provenance); ok {
		log.Printf("[memory] same lesson as %s; added the moment", existing)
		mc.app.markMemoryDirty("knowledge-relearned")
		return existing
	}
	unlock := memoryFileLocks.Acquire("knowledge:" + entryID)
	defer unlock()
	if err := snapshotKnowledgeFile(dir, entryID); err != nil {
		log.Printf("[memory] failed to snapshot knowledge file %s: %v", filePath, err)
		return ""
	}
	content := buildOKFKnowledgeMarkdown(entryID, knowledge.Title, knowledge.Description, time.Now(), string(source), nil, nil, knowledge.KnowledgePoints, normalizeKnowHowKind(knowledge.Kind))
	content = mc.app.normalizeGeneratedMarkdownToCurrentUILanguage("knowledge", content)
	previous, readErr := os.ReadFile(filePath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return ""
	}
	content = knowledgeProvenance(content, string(previous), knowledgeProvenanceUnion([]string{string(source)}, knowledge.provenance.sources), knowledge.provenance.apps, knowledge.provenance.complete)
	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		log.Printf("[memory] failed to write knowledge file %s: %v", filePath, err)
		return ""
	}

	log.Printf("[memory] saved knowledge to %s", filePath)
	return entryID
}

// updateKnowledgeFile updates an existing knowledge file identified by knowledge.UpdateID.
// It preserves existing metadata (Source, Apps, Activity Session) from the original file
// to avoid data loss when a memory-pipeline update targets an activity-created entry.
func (mc *MemoryGenerator) updateKnowledgeFile(source memorySource, sourceID string, knowledge *knowledgeGenerationResponse) {
	_, _, _ = mc.updateKnowledgeFileDetailed(source, sourceID, knowledge)
}

func (mc *MemoryGenerator) updateKnowledgeFileDetailed(
	source memorySource,
	sourceID string,
	knowledge *knowledgeGenerationResponse,
) (string, string, error) {
	dir, err := knowledgeDir()
	if err != nil {
		log.Printf("[memory] failed to get knowledge dir: %v", err)
		return "", "", err
	}

	if strings.Contains(knowledge.UpdateID, "..") || strings.ContainsAny(knowledge.UpdateID, "/\\") {
		log.Printf("[memory] invalid knowledge updateID %q, falling back to create", knowledge.UpdateID)
		entryID := mc.saveKnowledgeFile(source, sourceID, knowledge)
		if entryID == "" {
			return "", "", errors.New("save fallback knowledge file")
		}
		return entryID, BackgroundMaterializationCreated, nil
	}

	filePath := filepath.Join(dir, knowledge.UpdateID+".md")

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		log.Printf("[memory] knowledge updateID %q not found, falling back to create", knowledge.UpdateID)
		entryID := mc.saveKnowledgeFile(source, sourceID, knowledge)
		if entryID == "" {
			return "", "", errors.New("save fallback knowledge file")
		}
		return entryID, BackgroundMaterializationCreated, nil
	}

	unlock := memoryFileLocks.Acquire("knowledge:" + knowledge.UpdateID)
	defer unlock()

	existingData, readErr := os.ReadFile(filePath)
	if readErr != nil {
		return "", "", readErr
	}
	existingMeta := parseKnowledgeMetadata(filePath)
	sourceValue := existingMeta["Source"]
	apps := splitCommaList(existingMeta["Apps"])
	sessionIDs := parseMemoryFrontmatter(string(existingData)).ActivitySessionIDs
	if doc, ok := parseOKFDocument(string(existingData)); ok {
		if v := okfString(doc.Frontmatter, "source"); v != "" {
			sourceValue = v
		}
		apps = okfStringSlice(doc.Frontmatter, "apps")
		sessionIDs = okfStringSlice(doc.Frontmatter, "activity_sessions")
	}
	if err := snapshotKnowledgeFile(dir, knowledge.UpdateID); err != nil {
		log.Printf("[memory] failed to snapshot knowledge file %s: %v", filePath, err)
		return "", "", err
	}
	content := buildOKFKnowledgeMarkdown(knowledge.UpdateID, knowledge.Title, knowledge.Description, time.Now(), sourceValue, apps, sessionIDs, knowledge.KnowledgePoints, updatedKnowHowKind(string(existingData), knowledge.Kind))
	content = mc.app.normalizeGeneratedMarkdownToCurrentUILanguage("knowledge", content)
	content = preserveKnowHowMoments(content, string(existingData))
	content = knowledgeProvenance(content, string(existingData), knowledgeProvenanceUnion([]string{string(source)}, knowledge.provenance.sources), knowledge.provenance.apps, knowledge.provenance.complete)
	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		log.Printf("[memory] failed to update knowledge file %s: %v", filePath, err)
		return "", "", err
	}

	log.Printf("[memory] updated existing knowledge at %s", filePath)
	return knowledge.UpdateID, BackgroundMaterializationUpdated, nil
}

// parseKnowledgeMetadata reads a knowledge markdown file and extracts metadata
// key-value pairs from lines matching "- **Key**: Value".
//
// The `Activity Session` key may span multiple lines when the activity
// triager writes a nested bullet list (one bullet per contributing
// session). The whole block — including the trailing indented bullets —
// is stored verbatim so callers (specifically updateKnowledgeFile) can
// write it back without mangling. A trailing newline is NOT included so
// the caller controls separator formatting.
func parseKnowledgeMetadata(filePath string) map[string]string {
	meta := make(map[string]string)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return meta
	}
	lines := strings.Split(string(data), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "- **") {
			continue
		}
		rest := strings.TrimPrefix(line, "- **")
		idx := strings.Index(rest, "**:")
		if idx < 0 {
			continue
		}
		key := rest[:idx]
		value := strings.TrimSpace(rest[idx+3:])

		// Activity Session may carry an indented bullet list below an
		// empty header line. Capture the whole block so it survives an
		// overwrite unchanged.
		if key == "Activity Session" && value == "" {
			var block strings.Builder
			block.WriteString("- **Activity Session**:")
			for j := i + 1; j < len(lines); j++ {
				raw := lines[j]
				if strings.TrimSpace(raw) == "" {
					break
				}
				if !(strings.HasPrefix(raw, " ") && strings.HasPrefix(strings.TrimLeft(raw, " \t"), "- ")) {
					break
				}
				block.WriteString("\n")
				block.WriteString(raw)
				i = j
			}
			meta[key] = block.String()
			continue
		}

		meta[key] = value
	}
	return meta
}
