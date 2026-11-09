package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type workProfileFakeModel struct {
	prompts []string
	output  string
	fail    bool
}

func (m *workProfileFakeModel) Generate(_ context.Context, prompt string) (string, error) {
	m.prompts = append(m.prompts, prompt)
	if m.fail && len(m.prompts) == 1 {
		return "", errors.New("unavailable")
	}
	return m.output, nil
}

func TestWorkProfileGenerateRetriesAndChecksModelOutput(t *testing.T) {
	candidates := []workProfileCandidate{{ID: "c1", Kind: "taste", Text: "You write clear updates."}}
	m := &workProfileFakeModel{fail: true, output: `{"lines":[{"text":"You write clear updates.","from":["c1"]},{"text":"You work for Uncited.","from":["c1"]}],"publicSpecialties":[]}`}
	result, err := workProfileGenerate(context.Background(), m, candidates, "Mina", []string{"owner@example.com"})
	if err != nil || len(m.prompts) != 2 || len(result.Lines) != 1 {
		t.Fatalf("result %+v, attempts %d, error %v", result, len(m.prompts), err)
	}
	if strings.Contains(m.prompts[0], "Mina") || strings.Contains(m.prompts[0], "owner@example.com") {
		t.Fatal("private values reached model")
	}
	m = &workProfileFakeModel{output: `{"lines":[{"text":"You work for Uncited.","from":["c1"]}],"publicSpecialties":[]}`}
	if _, err := workProfileGenerate(context.Background(), m, candidates, "", nil); err == nil || len(m.prompts) != 2 {
		t.Fatal("invalid model output produced a draft")
	}
	m = &workProfileFakeModel{}
	if _, err := workProfileGenerate(context.Background(), m, []workProfileCandidate{{ID: "c1", Text: "You work for Mina."}}, "Mina", nil); err == nil || len(m.prompts) != 0 {
		t.Fatal("unfiltered candidate reached the model")
	}
}
