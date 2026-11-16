package main

import (
	"encoding/json"
	"os"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ExportAnswers saves Your answers (the answers ledger, every Yes, No, Not
// mine and edit, with what each was about) to a file the person picks in a
// native Save dialog (computer-data v1.0.0 rule 4). Nothing is uploaded.
// It returns false when the person cancels.
func (a *App) ExportAnswers() (bool, error) {
	title := "Export your answers"
	if normalizeUILanguage(a.doingNowLanguage()) == "zh-CN" {
		title = "导出你的回答"
	}
	dest, err := wailsRuntime.SaveFileDialog(a.ctx, wailsRuntime.SaveDialogOptions{
		Title:           title,
		DefaultFilename: "Isoform-answers-" + time.Now().Format("2006-01-02") + ".jsonl",
	})
	if err != nil || dest == "" {
		return false, err
	}
	return true, writeAnswersExport(dest, a.answersLedger().entries())
}

// writeAnswersExport writes one answer per line, as the ledger keeps them.
func writeAnswersExport(dest string, entries []AnswerRecord) error {
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	for _, entry := range entries {
		if err := encoder.Encode(entry); err != nil {
			file.Close()
			return err
		}
	}
	return file.Close()
}
