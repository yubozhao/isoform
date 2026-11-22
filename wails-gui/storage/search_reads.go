package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"

	"modernc.org/sqlite"
)

func init() {
	// SQLite's built-in lower only folds ASCII. This local scalar keeps LIKE
	// matching consistent with the Unicode ranking and file-backed sources.
	sqlite.MustRegisterDeterministicScalarFunction("isoform_search_lower", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		value, _ := args[0].(string)
		return strings.ToLower(value), nil
	})
}

// SearchWordsPredicate takes only a trusted SQL expression; words are bound
// parameters and LIKE metacharacters always mean literal characters.
func SearchWordsPredicate(expression string, words []string) (string, []any) {
	clauses := make([]string, 0, len(words))
	args := make([]any, 0, len(words))
	escape := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	// LIKE already folds ASCII on these stores. Only a definite nonmatch
	// containing non-ASCII text needs Go's full Unicode fold. The OR branch
	// keeps common matching rows inside SQLite instead of crossing into Go.
	nonASCII := `(` + expression + `) GLOB ('*[^' || char(1) || '-' || char(127) || ']*')`
	for _, word := range words {
		pattern := "%" + escape.Replace(strings.ToLower(word)) + "%"
		clauses = append(clauses, `((`+expression+`) LIKE ? ESCAPE '\' OR (`+nonASCII+` AND isoform_search_lower(`+expression+`) LIKE ? ESCAPE '\'))`)
		args = append(args, pattern, pattern)
	}
	if len(clauses) == 0 {
		return "1", args
	}
	return strings.Join(clauses, " AND "), args
}

// Match the visible-chat rule used by phone/native navigation. Background
// sessions are never destinations, even if an old writer left them unhidden.
const searchVisibleChat = `s.hidden=0 AND p.hidden=0 AND w.hidden=0
    AND s.local_id NOT IN (SELECT session_tag FROM cron_runs WHERE session_tag!='')`

type SearchChatRow struct {
	ID, SessionID, Title, Content, Kind, TopicID string
	At                                           int64
}

// SearchChats reads at most 50 matching candidates, reserving title matches
// before recent message matches so existing chat destinations stay reachable.
func (s *Store) SearchChats(ctx context.Context, words []string) ([]SearchChatRow, error) {
	predicate, args := SearchWordsPredicate(`title || char(10) || content`, words)
	// A title hit is a chat destination. A message destination also needs
	// a word in its own displayed text, including when its chat title matches.
	var bodyClauses []string
	for _, word := range words {
		clause, bodyArgs := SearchWordsPredicate(`content`, []string{word})
		bodyClauses = append(bodyClauses, clause)
		args = append(args, bodyArgs...)
	}
	bodyPredicate := strings.Join(bodyClauses, " OR ")
	if bodyPredicate == "" {
		bodyPredicate = "1"
	}
	rows, err := s.db.QueryContext(ctx, `WITH candidates AS (
        SELECT s.local_id AS id,s.local_id AS session_id,s.label AS title,'' AS content,'' AS kind,s.created_at AS at
        FROM sessions s JOIN projects p ON p.id=s.project_id JOIN workspaces w ON w.id=s.workspace_id WHERE `+searchVisibleChat+`
        UNION ALL
        SELECT i.id,s.local_id,s.label,i.content,i.kind,i.created_at
        FROM stream_items i JOIN sessions s ON s.local_id=i.session_local_id JOIN projects p ON p.id=s.project_id JOIN workspaces w ON w.id=s.workspace_id
        WHERE `+searchVisibleChat+` AND i.kind IN ('user','assistant','system') AND i.tool_name=''
    ) SELECT id,session_id,title,content,kind,at FROM candidates WHERE `+predicate+` AND (kind='' OR (`+bodyPredicate+`)) ORDER BY CASE WHEN kind='' THEN 0 ELSE 1 END,at DESC,id LIMIT 50`, args...)
	if err != nil {
		return nil, err
	}
	var result []SearchChatRow
	for rows.Next() {
		var r SearchChatRow
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Title, &r.Content, &r.Kind, &r.At); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range result {
		r := &result[i]
		// C1 is absent. Derived system-row tags cannot be exposed without
		// its read-time evidence rule; user and assistant tags are explicit.
		if r.Kind != "user" && r.Kind != "assistant" {
			continue
		}
		err := s.db.QueryRowContext(ctx, `SELECT topic_id FROM stream_item_topics WHERE stream_item_id=? AND session_id=? ORDER BY topic_id LIMIT 1`, r.ID, r.SessionID).Scan(&r.TopicID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return result, ctx.Err()
}

// FindVisibleStreamItemCursor returns an exclusive sort-order bound, which
// includes the item even with a page limit of one and sparse sort orders.
func (s *Store) FindVisibleStreamItemCursor(ctx context.Context, sessionID, itemID string) (int, error) {
	var cursor int
	err := s.committedReader().QueryRowContext(ctx, `SELECT i.sort_order+1 FROM stream_items i JOIN sessions s ON s.local_id=i.session_local_id
        JOIN projects p ON p.id=s.project_id JOIN workspaces w ON w.id=s.workspace_id WHERE `+searchVisibleChat+` AND s.local_id=? AND i.id=?`, sessionID, itemID).Scan(&cursor)
	return cursor, err
}

type SearchPersonRow struct {
	ID, Title, Body, DirectBody, CreatedFrom string
	ImportedHandles                          string
	ObservedApps                             []string
	At                                       int64
}

// SearchPeople matches only the identity fields the Who you know page displays.
// Capture evidence needs both same-app sightings and overlapping session
// lineage. Import ownership is direct; neither a missing import row nor a
// sessionless counter alone turns a typed handle into captured evidence.
func (s *Store) SearchPeople(ctx context.Context, words []string) ([]SearchPersonRow, error) {
	predicate, args := SearchWordsPredicate(`title || char(10) || body`, words)
	rows, err := s.db.QueryContext(ctx, `WITH captured_apps AS (
        SELECT DISTINCT s.principal_id,s.app FROM principal_sightings s
        JOIN principal_session_sightings ss ON ss.principal_id=s.principal_id
        WHERE s.first_seen_at<=ss.last_seen_at AND s.last_seen_at>=ss.first_seen_at
    ), candidates AS (
        SELECT p.id,p.display_name AS title,p.updated_at AS at,p.created_from,p.relationship || char(10) ||
        COALESCE((SELECT group_concat(alias,char(10)) FROM principal_aliases WHERE principal_id=p.id),'') || char(10) ||
        COALESCE((SELECT group_concat(display_handle,char(10)) FROM app_handles WHERE principal_id=p.id),'') AS body,
        p.relationship || char(10) ||
        COALESCE((SELECT group_concat(alias,char(10)) FROM principal_aliases WHERE principal_id=p.id),'') || char(10) ||
        COALESCE((SELECT group_concat(h.display_handle,char(10)) FROM app_handles h WHERE h.principal_id=p.id
            AND (EXISTS(SELECT 1 FROM contact_handle_provenance c WHERE c.handle_id=h.id)
                OR NOT EXISTS(SELECT 1 FROM captured_apps s WHERE s.principal_id=p.id AND s.app=h.app COLLATE NOCASE))),'') AS direct_body,
        COALESCE((SELECT group_concat(h.display_handle,char(10)) FROM app_handles h WHERE h.principal_id=p.id
            AND EXISTS(SELECT 1 FROM contact_handle_provenance c WHERE c.handle_id=h.id)),'') AS imported_handles,
        (SELECT json_group_array(DISTINCT h.app) FROM app_handles h WHERE h.principal_id=p.id
            AND NOT EXISTS(SELECT 1 FROM contact_handle_provenance c WHERE c.handle_id=h.id)
            AND EXISTS(SELECT 1 FROM captured_apps s WHERE s.principal_id=p.id AND s.app=h.app COLLATE NOCASE)) AS observed_apps
        FROM principals p WHERE p.merged_into=''
    ) SELECT id,title,body,direct_body,imported_handles,observed_apps,at,created_from FROM candidates WHERE `+predicate+` ORDER BY at DESC,id LIMIT 50`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchPersonRow
	for rows.Next() {
		var r SearchPersonRow
		var observedApps string
		if err := rows.Scan(&r.ID, &r.Title, &r.Body, &r.DirectBody, &r.ImportedHandles, &observedApps, &r.At, &r.CreatedFrom); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(observedApps), &r.ObservedApps); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
