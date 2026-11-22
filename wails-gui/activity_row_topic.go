package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/skratchdot/open-golang/open"
)

// ActivityRowDetail carries only the metadata the feed menu can display.
type ActivityRowDetail struct {
	Kind      string              `json:"kind"`
	ID        string              `json:"id"`
	TopicID   string              `json:"topicId,omitempty"`
	TopicName string              `json:"topicName,omitempty"`
	Open      *ActivityOpenTarget `json:"open,omitempty"`
}

type ActivityOpenTarget struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

type activityLocation struct {
	ActivityOpenTarget
	address string
}

var openActivityLocation = open.Start
var errActivityOpenInApp = errors.New("activity_open_in_app")

func activityFileLocation(path string) *activityLocation {
	if filepath.IsAbs(path) && isOpenableDeliverableExt(path) {
		return &activityLocation{ActivityOpenTarget{"file", filepath.Base(path)}, path}
	}
	return nil
}

func activityBrowserLocation(raw string) *activityLocation {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return nil
	}
	switch u.Scheme {
	case "http", "https":
		if u.Hostname() != "" {
			return &activityLocation{ActivityOpenTarget{"page", u.Hostname()}, raw}
		}
	case "file":
		path := filepath.FromSlash(u.Path)
		if u.Host == "" || u.Host == "localhost" {
			return activityFileLocation(path)
		}
	}
	return nil
}

func (a *App) activityRowStore() (*ActivityStore, error) {
	if a == nil || a.activity == nil || a.activity.store == nil || a.store == nil {
		return nil, errActivityFeedNotReady
	}
	return a.activity.store, nil
}

// Read only the linked run's own snapshot interval. Several runs may share a
// session. Do not substitute a page from a neighbouring stretch of activity.
func (a *App) activityRowLocations(ctx context.Context, ids []string, policy activityReadPrivacyPolicy) (map[string]*activityLocation, error) {
	store := a.activity.store
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := store.reader().QueryContext(ctx, `SELECT a.id,g.app_key,g.app_name,g.evidence_excluded,j.value,
		EXISTS(SELECT 1 FROM activity_agent_runs ar WHERE ar.run_id=g.id),
		COALESCE(snap.browser_url,''),
		CASE WHEN snap.app='code_editor' THEN COALESCE(json_extract(snap.parsed_json,'$.metadata.relativePath'),'') ELSE '' END
		FROM activities a JOIN json_each(a.run_ids) r LEFT JOIN activity_timeline_groups g ON g.id=r.value
		LEFT JOIN json_each(g.session_ids) j
		LEFT JOIN activity_snapshots snap ON snap.id=(SELECT s.id FROM activity_snapshots s WHERE s.session_id=j.value
			AND s.timestamp BETWEEN g.start_ms AND g.end_ms AND (s.browser_url != '' OR (s.app='code_editor' AND s.parsed_json != ''))
			ORDER BY s.timestamp DESC,s.id DESC LIMIT 1)
		WHERE a.id IN (SELECT value FROM json_each(?))
		ORDER BY g.end_ms DESC,g.id,j.key`, string(raw))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type source struct {
		moment, session, page, path string
		agent                       bool
	}
	var sources []source
	var sessionIDs []string
	blocked := map[string]bool{}
	seen := map[string]bool{}
	for rows.Next() {
		var src source
		var key, name, sid sql.NullString
		var excluded sql.NullBool
		if err := rows.Scan(&src.moment, &key, &name, &excluded, &sid, &src.agent, &src.page, &src.path); err != nil {
			return nil, err
		}
		if !sid.Valid || excluded.Bool || activityEvidenceAppExcluded(key.String, name.String) {
			blocked[src.moment] = true
			continue
		}
		src.session = sid.String
		if !seen[src.session] {
			seen[src.session] = true
			sessionIDs = append(sessionIDs, src.session)
		}
		sources = append(sources, src)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	sessions, err := store.GetSessionsByIDsContext(ctx, sessionIDs)
	if err != nil {
		return nil, err
	}
	targets := map[string]*activityLocation{}
	checked := map[string]bool{}
	for _, src := range sources {
		session := sessions[src.session]
		if session == nil {
			blocked[src.moment] = true
			continue
		}
		if !src.agent && !blocked[src.moment] {
			current := targets[src.moment]
			if session.IsMeeting && session.AudioPath != "" && (current == nil || current.Kind != "call") {
				targets[src.moment] = &activityLocation{ActivityOpenTarget{"call", "Recording"}, session.ID}
			} else if current == nil {
				targets[src.moment] = activityBrowserLocation(src.page)
				if targets[src.moment] == nil {
					targets[src.moment] = activityFileLocation(src.path)
				}
			}
		}
	}
	// A private member with no target withholds the whole moment as well.
	for _, src := range sources {
		if blocked[src.moment] || targets[src.moment] == nil {
			continue
		}
		allowed, ok := checked[src.session]
		if !ok {
			allowed, err = a.activitySessionAllowsWithPolicy(store, *sessions[src.session], policy)
			if err != nil {
				return nil, err
			}
			checked[src.session] = allowed
		}
		if !allowed {
			blocked[src.moment] = true
		}
	}
	for id := range blocked {
		delete(targets, id)
	}
	return targets, ctx.Err()
}

func (a *App) GetActivityRowDetails(startMs, endMs int64) ([]ActivityRowDetail, error) {
	store, err := a.activityRowStore()
	if err != nil {
		return nil, err
	}
	if endMs <= startMs {
		return nil, errors.New("invalid activity range")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := store.GetActivitiesInRangeContext(ctx, startMs, endMs)
	if err != nil {
		return nil, err
	}
	agents, err := store.agentRunIDsBetween(ctx, startMs, endMs)
	if err != nil {
		return nil, err
	}
	// The owner reading their own kept rows on this computer, which Closed
	// Eyes allows (A9; redesign v1.0.0 rule 27), as Chat's own reads do.
	ctx = context.WithValue(ctx, pausedBriefReadKey{}, true)
	policy, err := a.activityReadPrivacyPolicyContext(ctx)
	if err != nil {
		return nil, err
	}
	topics, err := a.activityRowTopics(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	out := []ActivityRowDetail{}
	for _, row := range withoutAgentMoments(rows, agents) {
		if row.StartMs >= endMs || row.EndMs <= startMs {
			continue
		}
		d := ActivityRowDetail{Kind: "screen", ID: row.ID}
		if p := topics[row.ID]; p != nil {
			d.TopicID, d.TopicName = p.ID, strings.TrimSpace(p.Name)
		}
		// Closed Eyes stops seeing and keeping, not reading what is already
		// kept (computer-conformance-answers v1.3.0 A9): only rows inside a
		// pause, the Closed Eyes stretch itself, have nothing to open.
		if activityActionsWithheld(row, policy.pauses) == "" {
			ids = append(ids, row.ID)
		}
		out = append(out, d)
	}
	targets, err := a.activityRowLocations(ctx, ids, policy)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if target := targets[out[i].ID]; target != nil {
			out[i].Open = &target.ActivityOpenTarget
		}
	}
	return slices.DeleteFunc(out, func(d ActivityRowDetail) bool { return d.TopicID == "" && d.Open == nil }), nil
}

func (a *App) activityScreenMoment(ctx context.Context, kind, id string) (*ActivityRow, error) {
	store, err := a.activityRowStore()
	if err != nil {
		return nil, err
	}
	if kind != "screen" || id == "" {
		return nil, errors.New("expected a screen moment")
	}
	row, err := store.GetActivityByIDContext(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.New("activity moment not found")
	}
	agents, err := store.agentRunIDsBetween(ctx, row.StartMs, row.EndMs)
	if err != nil {
		return nil, err
	}
	if activityRowIsAgent(*row, agents) {
		return nil, errors.New("expected a screen moment")
	}
	return row, nil
}

func (a *App) OpenActivityRow(kind, id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	row, err := a.activityScreenMoment(ctx, kind, id)
	if err != nil {
		return err
	}
	// The owner reading their own kept rows on this computer, which Closed
	// Eyes allows (A9; redesign v1.0.0 rule 27), as Chat's own reads do.
	ctx = context.WithValue(ctx, pausedBriefReadKey{}, true)
	policy, err := a.activityReadPrivacyPolicyContext(ctx)
	if err != nil {
		return err
	}
	if activityActionsWithheld(*row, policy.pauses) != "" {
		return errActivityActionsWithheld
	}
	targets, err := a.activityRowLocations(ctx, []string{id}, policy)
	if err != nil {
		return err
	}
	target := targets[id]
	if target == nil {
		return errors.New("no open target for this moment")
	}
	switch target.Kind {
	case "call":
		return fmt.Errorf("%w:%s", errActivityOpenInApp, target.address)
	case "file":
		path, err := filepath.EvalSymlinks(target.address)
		if err != nil {
			return fmt.Errorf("activity file unavailable: %w", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || !isOpenableDeliverableExt(path) {
			return errors.New("activity file is not openable")
		}
		return openActivityLocation(path)
	default:
		return openActivityLocation(target.address)
	}
}
