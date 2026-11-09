package main

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// WorkProfileUpload is the complete request body. Source details, draft
// metadata, and removed lines never belong in this type.
type WorkProfileUpload struct {
	Lines             []string `json:"lines"`
	PublicSpecialties []string `json:"publicSpecialties"`
	LearnedThrough    string   `json:"learnedThrough,omitempty"`
	ExpectedVersion   int64    `json:"expectedVersion"`
}

type WorkProfileUploadResult struct {
	Version    int64  `json:"version"`
	ApprovedAt string `json:"approvedAt"`
}

// The owner client supplies this operation; only explicit approval may call it.
type workProfileUploader interface {
	UploadApprovedProfile(context.Context, WorkProfileUpload) (WorkProfileUploadResult, error)
}

type workProfileUploadGroup struct {
	Name     string
	Selected bool
	Lines    []string
}

// workProfilePrepareUpload builds the exact request to preview and later
// send. General lines have no specialty prefix; selected names need a line.
func workProfilePrepareUpload(groups []workProfileUploadGroup, general []string, learned string, expected int64, fullName string, privateValues []string, now time.Time) (WorkProfileUpload, error) {
	payload := WorkProfileUpload{Lines: []string{}, PublicSpecialties: []string{}, LearnedThrough: learned, ExpectedVersion: expected}
	seen := make(map[string]bool)
	for _, group := range groups {
		name := strings.TrimSpace(group.Name)
		clean, _ := workProfileClean(name, fullName, privateValues)
		if name == "" || clean != name || seen[strings.ToLower(name)] {
			return WorkProfileUpload{}, errors.New("invalid specialty")
		}
		seen[strings.ToLower(name)] = true
		if group.Selected {
			if len(group.Lines) == 0 {
				return WorkProfileUpload{}, errors.New("selected specialty has no lines")
			}
			payload.PublicSpecialties = append(payload.PublicSpecialties, name)
		}
		for _, line := range group.Lines {
			if strings.TrimSpace(line) == "" {
				return WorkProfileUpload{}, errors.New("empty specialty line")
			}
			payload.Lines = append(payload.Lines, name+": "+line)
		}
	}
	payload.Lines = append(payload.Lines, general...)
	if err := workProfileValidateUpload(payload, fullName, privateValues, now); err != nil {
		return WorkProfileUpload{}, err
	}
	return payload, nil
}

func workProfileValidateUpload(payload WorkProfileUpload, fullName string, privateValues []string, now time.Time) error {
	if len(payload.Lines) < 1 || len(payload.Lines) > 200 || len(payload.PublicSpecialties) > 3 || payload.ExpectedVersion < 0 {
		return errors.New("invalid profile size or version")
	}
	if payload.LearnedThrough != "" {
		date, err := time.Parse("2006-01-02", payload.LearnedThrough)
		if err != nil || date.Format("2006-01-02") != payload.LearnedThrough || payload.LearnedThrough > now.UTC().Format("2006-01-02") {
			return errors.New("invalid learned-through date")
		}
	}
	total := 0
	for _, line := range payload.Lines {
		clean, _ := workProfileClean(line, fullName, privateValues)
		if !utf8.ValidString(line) || len(line) == 0 || len(line) > 1000 || clean != line || strings.IndexFunc(line, unicode.IsControl) >= 0 {
			return errors.New("invalid profile line")
		}
		total += len(line)
	}
	if total > 20000 {
		return errors.New("profile too large")
	}
	for _, specialty := range payload.PublicSpecialties {
		if !utf8.ValidString(specialty) || utf8.RuneCountInString(specialty) < 1 || utf8.RuneCountInString(specialty) > 40 || strings.IndexFunc(specialty, unicode.IsControl) >= 0 {
			return errors.New("invalid specialty")
		}
		found := false
		for _, line := range payload.Lines {
			if strings.HasPrefix(line, specialty+": ") {
				found = true
				break
			}
		}
		if !found {
			return errors.New("specialty has no line")
		}
	}
	return nil
}
