package main

import (
	"errors"
	"testing"
)

func TestTaskWorkflowComposeTextQualification(t *testing.T) {
	tests := []struct {
		name          string
		task          string
		desiredResult string
		want          bool
	}{
		{
			name:          "draft proposal",
			task:          "Draft a customer proposal",
			desiredResult: "A concise proposal ready to send",
			want:          true,
		},
		{
			name:          "compose message",
			task:          "Compose a message to Jenny",
			desiredResult: "A clear response in the user's tone",
			want:          true,
		},
		{
			name:          "prepare update",
			task:          "Prepare a customer update",
			desiredResult: "A factual update ready to publish",
			want:          true,
		},
		{
			name:          "convert email to one pager",
			task:          "Convert email content into a one-page document using ChatGPT",
			desiredResult: "A one-page version of the email content",
			want:          true,
		},
		{
			name:          "summarize text into brief",
			task:          "Summarize the interview into a concise brief",
			desiredResult: "A written brief ready to share",
			want:          true,
		},
		{
			name:          "review draft is passive",
			task:          "Review a draft proposal",
			desiredResult: "Identify gaps",
			want:          false,
		},
		{
			name:          "coding is excluded",
			task:          "Write code for the proposal editor",
			desiredResult: "A tested implementation",
			want:          false,
		},
		{
			name:          "browsing is excluded",
			task:          "Explore GitHub projects",
			desiredResult: "Understand the available repositories",
			want:          false,
		},
		{
			name:          "convert image format is not text composition",
			task:          "Convert an image from PNG to JPEG",
			desiredResult: "A JPEG file",
			want:          false,
		},
		{
			name:          "convert currency is not text composition",
			task:          "Convert USD to BRL",
			desiredResult: "The current exchange value",
			want:          false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := taskWorkflowComposeTextQualified(
				test.task,
				test.desiredResult,
			); got != test.want {
				t.Fatalf("qualified=%v, want %v", got, test.want)
			}
		})
	}
}

func TestTaskWorkflowAnalyzeTextQualification(t *testing.T) {
	tests := []struct {
		name          string
		task          string
		desiredResult string
		want          bool
	}{
		{
			name:          "review proposal gaps",
			task:          "Review a draft proposal",
			desiredResult: "Findings that identify the material gaps and risks",
			want:          true,
		},
		{
			name:          "compare options",
			task:          "Compare the two pricing approaches",
			desiredResult: "A comparison with tradeoffs and a recommendation",
			want:          true,
		},
		{
			name:          "explain source",
			task:          "Explain why the supplied plan missed its target",
			desiredResult: "A source-backed explanation with unknowns separated",
			want:          true,
		},
		{
			name:          "composition keeps precedence",
			task:          "Write an assessment of the proposal",
			desiredResult: "A concise assessment",
			want:          false,
		},
		{
			name:          "research needs newer retrieval",
			task:          "Research and compare current Codex alternatives",
			desiredResult: "A comparison report with recommendations",
			want:          false,
		},
		{
			name:          "delegated ai answer is excluded",
			task:          "Ask ChatGPT to analyze the proposal",
			desiredResult: "An answer identifying the main risks",
			want:          false,
		},
		{
			name:          "analysis inside ai chat is excluded",
			task:          "Review the proposal in ChatGPT",
			desiredResult: "Findings that identify the main risks",
			want:          false,
		},
		{
			name:          "analysis inside Yansu is excluded",
			task:          "Review the proposal in Yansu",
			desiredResult: "Findings that identify the main risks",
			want:          false,
		},
		{
			name:          "configuration mutation is excluded",
			task:          "Review and configure the account settings",
			desiredResult: "A report of issues and updated settings",
			want:          false,
		},
		{
			name:          "code review is excluded",
			task:          "Review code in the repository",
			desiredResult: "Findings and recommendations",
			want:          false,
		},
		{
			name:          "pull request shorthand is excluded",
			task:          "Review this PR",
			desiredResult: "Findings, risks, and recommendations",
			want:          false,
		},
		{
			name:          "visual review is excluded despite textual result",
			task:          "Review images in a social post carousel",
			desiredResult: "A comparison report with findings",
			want:          false,
		},
		{
			name:          "passive understanding is excluded",
			task:          "Understand the supplied proposal",
			desiredResult: "A summary of its main points",
			want:          false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := taskWorkflowAnalyzeTextQualified(
				test.task,
				test.desiredResult,
			); got != test.want {
				t.Fatalf("qualified=%v, want %v", got, test.want)
			}
		})
	}
}

func TestTaskWorkflowSourceDependentCompositionRequiresFrozenVisualContext(
	t *testing.T,
) {
	episode := TaskEpisode{
		ID:              "episode-source-dependent",
		InferredStartAt: 10_000,
		LatestHypothesis: TaskEpisodeHypothesisRevision{
			TaskHypothesis:          "Convert email content into a one-page document using ChatGPT",
			DesiredResultHypothesis: "A one-page version of the email content",
		},
	}
	ready, err := taskEpisodeProductionShadowReady(episode)
	if ready || !errors.Is(err, ErrTaskWorkflowInsufficientFrozenContext) {
		t.Fatalf("ready=%v err=%v", ready, err)
	}

	episode.FrozenContext.Manifest = []TaskContextManifestEntry{{
		SourceKind: taskContextSourceActivitySnapshotKind,
		SourceID:   "session:42",
		ObservedAt: 9_900,
		Included:   true,
	}}
	ready, err = taskEpisodeProductionShadowReady(episode)
	if !ready || err != nil {
		t.Fatalf("source-backed ready=%v err=%v", ready, err)
	}

	episode.LatestHypothesis.TaskHypothesis = "Draft a customer update"
	episode.FrozenContext.Manifest = nil
	ready, err = taskEpisodeProductionShadowReady(episode)
	if !ready || err != nil {
		t.Fatalf("self-contained draft ready=%v err=%v", ready, err)
	}
}

func TestTaskWorkflowAnalyzeTextAlwaysRequiresFrozenVisualContext(
	t *testing.T,
) {
	episode := TaskEpisode{
		ID:              "episode-analyze-source",
		InferredStartAt: 10_000,
		LatestHypothesis: TaskEpisodeHypothesisRevision{
			TaskHypothesis: "Review the supplied proposal",
			DesiredResultHypothesis: "Findings that identify the material " +
				"gaps and risks",
		},
	}
	if got := taskEpisodeProductionShadowCapability(episode); got != TaskWorkflowAnalyzeText {
		t.Fatalf("workflow=%q, want %q", got, TaskWorkflowAnalyzeText)
	}
	ready, err := taskEpisodeProductionShadowReady(episode)
	if ready || !errors.Is(err, ErrTaskWorkflowInsufficientFrozenContext) {
		t.Fatalf("ready=%v err=%v", ready, err)
	}

	episode.FrozenContext.Manifest = []TaskContextManifestEntry{{
		SourceKind: taskContextSourceActivitySnapshotKind,
		SourceID:   "session:analysis",
		ObservedAt: 9_999,
		Included:   true,
	}}
	ready, err = taskEpisodeProductionShadowReady(episode)
	if !ready || err != nil {
		t.Fatalf("source-backed ready=%v err=%v", ready, err)
	}

	episode.FrozenContext.Manifest[0].ObservedAt = 10_001
	ready, err = taskEpisodeProductionShadowReady(episode)
	if ready || !errors.Is(err, ErrTaskWorkflowInsufficientFrozenContext) {
		t.Fatalf("post-onset ready=%v err=%v", ready, err)
	}
}

func TestTaskWorkflowCapabilityKeyGroupsTextComposition(t *testing.T) {
	first := taskWorkflowCapabilityKey(
		"Draft a customer proposal",
		"A concise proposal ready to send",
		"episode-one",
	)
	second := taskWorkflowCapabilityKey(
		"Compose a message to Jenny",
		"A clear response in the user's tone",
		"episode-two",
	)
	if first != TaskWorkflowComposeText || second != TaskWorkflowComposeText {
		t.Fatalf("capability keys=%q %q, want %q", first, second, TaskWorkflowComposeText)
	}
}

func TestTaskWorkflowCapabilityKeyKeepsComposePrecedence(t *testing.T) {
	if got := taskWorkflowCapabilityKey(
		"Compose a comparison of the two options",
		"A comparison with recommendations",
		"episode-compose-analysis",
	); got != TaskWorkflowComposeText {
		t.Fatalf("capability=%q, want compose precedence", got)
	}
	if got := taskWorkflowCapabilityKey(
		"Compare the two options",
		"A comparison with recommendations",
		"episode-analysis",
	); got != TaskWorkflowAnalyzeText {
		t.Fatalf("capability=%q, want %q", got, TaskWorkflowAnalyzeText)
	}
}
