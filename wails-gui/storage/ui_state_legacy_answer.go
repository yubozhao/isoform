package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// UniqueLegacyMainAnswer finds a single legacy user row matching the saved
// answer in the brief's week. Ambiguous matches are deliberately left alone:
// two matching rows, or a row that replies to a question Isoform asked in the
// chat, since that row may answer the chat question rather than the week's.
func (s *Store) UniqueLegacyMainAnswer(ctx context.Context, answer string, from, until int64) (string, error) {
	rows, err := s.streamReader().QueryContext(ctx, `SELECT i.id, COALESCE(i.automation_context,'') FROM stream_items i JOIN sessions s ON s.local_id=i.session_local_id
        WHERE s.kind='main' AND i.kind='user' AND i.content=? AND i.created_at>=? AND i.created_at<? LIMIT 2`, answer, from, until)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	id := ""
	for rows.Next() {
		var next, origin string
		if err := rows.Scan(&next, &origin); err != nil {
			return "", err
		}
		if id != "" || origin != "" {
			return "", nil
		}
		id = next
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if id == "" {
		return "", nil
	}
	var kind, content string
	err = s.streamReader().QueryRowContext(ctx, `SELECT p.kind, p.content FROM stream_items i JOIN stream_items p
        ON p.session_local_id=i.session_local_id AND p.sort_order<i.sort_order AND p.kind IN ('user','assistant') AND p.tool_name=''
        WHERE i.id=? ORDER BY p.sort_order DESC LIMIT 1`, id).Scan(&kind, &content)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if kind == "assistant" && legacyAnswerFollowsQuestion(content) {
		return "", nil
	}
	return id, nil
}

func legacyAnswerFollowsQuestion(content string) bool {
	text := strings.TrimRight(strings.TrimSpace(content), "\"'”’」』*_ ")
	return strings.HasSuffix(text, "?") || strings.HasSuffix(text, "？")
}
