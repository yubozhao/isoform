package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"time"

	"wails-gui/internal/platform/recentfiles"
)

// The brief's doing section, from the recent files: the documents worked on
// today, by name only, with the app and the commitment each one is linked
// to. The narrative may use only titles from the filtered Today list, and
// the model binding rechecks the same file permissions and private folders.
const mobileBriefFileSource = "file"

// collectMobileBriefDoing adds today's documents to in.Doing.
func collectMobileBriefDoing(a *App, in *mobileBriefInput) {
	collectMobileBriefDoingWithCommitments(a, in, true)
}

// Model context resolves Purpose separately through its read-time evidence
// filter. The display-only commitment suffix must not bypass that boundary.
func collectMobileBriefDoingWithCommitments(a *App, in *mobileBriefInput, includeCommitments bool) {
	collectMobileBriefDoingContext(context.Background(), a, in, includeCommitments)
}

func collectMobileBriefDoingContext(ctx context.Context, a *App, in *mobileBriefInput, includeCommitments bool) {
	if a == nil || a.store == nil || a.activity == nil || a.activity.store == nil {
		return
	}
	if !a.isPermissionEnabledContext(ctx, filesPermissionKey) || ctx.Err() != nil {
		return
	}
	loc := in.Location
	if loc == nil {
		loc = time.UTC
	}
	localAsOf := in.AsOf.In(loc)
	dayStart := time.Date(localAsOf.Year(), localAsOf.Month(), localAsOf.Day(), 0, 0, 0, 0, loc)
	rows, err := a.activity.store.ListFileActivityContext(ctx, dayStart.UnixMilli(), in.AsOf.UnixMilli())
	if err != nil {
		log.Printf("[mobile-brief] recent files unavailable: %v", err)
		return
	}
	var links map[string]fileCommitment
	if includeCommitments {
		if store, err := a.purposeProductStore(); err == nil {
			if details, err := store.ListDetailsAllContext(ctx); err == nil {
				links = linkFilesToCommitments(rows, details)
			}
		}
	}
	home, _ := os.UserHomeDir()
	rawPrivate, err := a.store.GetUIState(ctx, filesPrivateFoldersKey)
	if err != nil || ctx.Err() != nil {
		return
	}
	var private []string
	if rawPrivate != "" && json.Unmarshal([]byte(rawPrivate), &private) != nil {
		return
	}
	for i, folder := range private {
		private[i] = recentfiles.Canonical(folder, home)
	}
	in.Doing = append(in.Doing, mobileBriefDoingCandidates(rows, links, private)...)
}

// mobileBriefDoingCandidates turns stored documents into doing items. A
// document used with an app protected since it was stored is left out, and
// so is one stored before its folder was kept private (the next read
// deletes it; until then the brief does not show it).
func mobileBriefDoingCandidates(rows []FileActivityRow, links map[string]fileCommitment, private []string) []mobileBriefCandidate {
	hidden := map[string]bool{}
	for _, folder := range private {
		hidden[recentfiles.FolderHash(folder)] = true
	}
	var out []mobileBriefCandidate
	for _, row := range rows {
		if fileSensitiveApp(row.AppBundleID, row.AppName) || fileUnderHidden(row.DirHashes, hidden) {
			continue
		}
		detail := "Opened"
		if row.Action == recentfiles.ActionEdited {
			detail = "Edited"
		}
		if link, ok := links[row.ID]; ok {
			detail += "; for " + link.Title
		}
		candidate := mobileBriefCandidate{
			SourceKind: mobileBriefFileSource,
			SourceID:   "file:" + row.ID,
			Title:      row.Name,
			Detail:     detail,
			Status:     row.Action,
			Kind:       row.Kind,
			At:         time.UnixMilli(row.ActiveAt),
		}
		if row.AppName != "" {
			candidate.Apps = []string{row.AppName}
			candidate.AppsShown = true
		}
		out = append(out, candidate)
	}
	return out
}

// fileUnderHidden reports whether one of a document's folders is hidden.
func fileUnderHidden(dirHashes string, hidden map[string]bool) bool {
	if len(hidden) == 0 {
		return false
	}
	start := 0
	for i := 0; i <= len(dirHashes); i++ {
		if i == len(dirHashes) || dirHashes[i] == ' ' {
			if i > start && hidden[dirHashes[start:i]] {
				return true
			}
			start = i + 1
		}
	}
	return false
}
