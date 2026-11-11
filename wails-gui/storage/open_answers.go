package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var ErrOpenAnswerLimit = errors.New("five answers are already open")
var ErrOpenAnswerTurnChanged = errors.New("open answer chat turn changed")
var ErrOpenAnswerKindMuted = errors.New("open answer kind is muted")
var ErrOpenAnswerExpired = errors.New("open answer has expired")
var ErrOpenAnswerNotAsk = errors.New("open answer is not awaiting promotion")

// OpenAnswerRow is the durable state of one promised answer. Times are Unix
// milliseconds; a zero window means the answer is watched for an event.
type OpenAnswerRow struct {
	ID                  string `json:"id"`
	ChatSessionID       string `json:"chatSessionId"`
	AssistantMessageID  string `json:"assistantMessageId"`
	Question            string `json:"question"`
	Topic               string `json:"topic"`
	Settles             string `json:"settles"`
	Source              string `json:"source"`
	WindowStart         int64  `json:"windowStart,omitempty"`
	WindowEnd           int64  `json:"windowEnd,omitempty"`
	WatchedEvent        string `json:"watchedEvent,omitempty"`
	MailAnchorMessageID string `json:"-"`
	MailCorrespondent   string `json:"-"`
	LastValue           string `json:"-"`
	LastCheckAt         int64  `json:"-"`
	HeldTransition      string `json:"-"`
	HeldText            string `json:"-"`
	HeldAt              int64  `json:"-"`
	// A negative value means undone at |value|.
	LearnedAt int64  `json:"-"`
	Delivery  string `json:"delivery"`
	ExpiresAt int64  `json:"expiresAt"`
	Posts     int    `json:"posts"`
	State     string `json:"state"`
	CreatedAt int64  `json:"createdAt"`
	SawAt     int64  `json:"sawAt,omitempty"`
}

type OpenAnswerBriefPost struct {
	Question      string
	Topic         string
	Source        string
	MessageID     string
	Content       string
	CreatedAt     int64
	HasMoment     bool
	OpenedOrActed bool
	OpenedAt      int64
}

func (s *Store) migrateOpenAnswers() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS open_answers (
			id TEXT PRIMARY KEY,
			chat_session_id TEXT NOT NULL REFERENCES sessions(local_id) ON DELETE CASCADE,
			assistant_message_id TEXT NOT NULL UNIQUE,
			question TEXT NOT NULL, topic TEXT NOT NULL,
			settles TEXT NOT NULL CHECK (settles IN ('result','reply','status')),
			source TEXT NOT NULL,
			window_start INTEGER NOT NULL DEFAULT 0, window_end INTEGER NOT NULL DEFAULT 0,
			watched_event TEXT NOT NULL DEFAULT '',
			delivery TEXT NOT NULL CHECK (delivery IN ('around','settled_only','brief')),
			expires_at INTEGER NOT NULL, posts INTEGER NOT NULL DEFAULT 0 CHECK (posts BETWEEN 0 AND 2),
			state TEXT NOT NULL CHECK (state IN ('open','ask','settled','saw_it','gave_up','cancelled')),
			created_at INTEGER NOT NULL, saw_at INTEGER NOT NULL DEFAULT 0,
			CHECK ((window_start > 0 AND window_end >= window_start AND watched_event = '') OR
			       (window_start = 0 AND window_end = 0 AND watched_event <> ''))
		);
		CREATE INDEX IF NOT EXISTS idx_open_answers_state ON open_answers(state, expires_at);
		CREATE TABLE IF NOT EXISTS open_answer_posts (
			answer_id TEXT NOT NULL REFERENCES open_answers(id) ON DELETE CASCADE,
			kind TEXT NOT NULL CHECK (kind IN ('interim','settled','gave_up')),
			message_id TEXT NOT NULL UNIQUE REFERENCES stream_items(id) ON DELETE CASCADE,
			PRIMARY KEY(answer_id, kind)
		);
	`)
	return err
}

func (s *Store) migrateOpenAnswerAskState() error {
	var definition string
	if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='open_answers'`).Scan(&definition); err != nil {
		return err
	}
	if strings.Contains(definition, "'ask'") {
		return nil
	}
	if _, err := s.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	migrate := func() error {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`
			CREATE TABLE open_answers_v71 (
				id TEXT PRIMARY KEY,
				chat_session_id TEXT NOT NULL REFERENCES sessions(local_id) ON DELETE CASCADE,
				assistant_message_id TEXT NOT NULL UNIQUE,
				question TEXT NOT NULL, topic TEXT NOT NULL,
				settles TEXT NOT NULL CHECK (settles IN ('result','reply','status')),
				source TEXT NOT NULL,
				window_start INTEGER NOT NULL DEFAULT 0, window_end INTEGER NOT NULL DEFAULT 0,
				watched_event TEXT NOT NULL DEFAULT '',
				mail_anchor_message_id TEXT, mail_correspondent TEXT,
				last_value TEXT NOT NULL DEFAULT '', last_check_at INTEGER NOT NULL DEFAULT 0,
				delivery TEXT NOT NULL CHECK (delivery IN ('around','settled_only','brief')),
				expires_at INTEGER NOT NULL, posts INTEGER NOT NULL DEFAULT 0 CHECK (posts BETWEEN 0 AND 2),
				state TEXT NOT NULL CHECK (state IN ('open','ask','settled','saw_it','gave_up','cancelled')),
				created_at INTEGER NOT NULL, saw_at INTEGER NOT NULL DEFAULT 0,
				CHECK ((window_start > 0 AND window_end >= window_start AND watched_event = '') OR
				       (window_start = 0 AND window_end = 0 AND watched_event <> ''))
			)`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO open_answers_v71 (` + openAnswerColumnsV71 + `) SELECT ` + openAnswerColumnsV71 + ` FROM open_answers`); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			CREATE TABLE open_answer_posts_v71 (
				answer_id TEXT NOT NULL REFERENCES open_answers_v71(id) ON DELETE CASCADE,
				kind TEXT NOT NULL CHECK (kind IN ('interim','settled','gave_up')),
				message_id TEXT NOT NULL UNIQUE REFERENCES stream_items(id) ON DELETE CASCADE,
				PRIMARY KEY(answer_id, kind)
			)`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO open_answer_posts_v71 SELECT answer_id,kind,message_id FROM open_answer_posts`); err != nil {
			return err
		}
		for _, statement := range []string{
			`DROP TABLE open_answer_posts`,
			`DROP TABLE open_answers`,
			`ALTER TABLE open_answers_v71 RENAME TO open_answers`,
			`ALTER TABLE open_answer_posts_v71 RENAME TO open_answer_posts`,
			`CREATE INDEX idx_open_answers_state ON open_answers(state, expires_at)`,
		} {
			if _, err := tx.Exec(statement); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	migrateErr := migrate()
	_, restoreErr := s.db.Exec(`PRAGMA foreign_keys=ON`)
	if migrateErr != nil {
		return fmt.Errorf("rebuild open-answer state constraint: %w", migrateErr)
	}
	if restoreErr != nil {
		return fmt.Errorf("restore foreign keys after open-answer migration: %w", restoreErr)
	}
	return nil
}

func (s *Store) migrateOpenAnswerQuietHolds() error {
	rows, err := s.db.Query(`PRAGMA table_info(open_answers)`)
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, column := range []struct {
		name string
		kind string
	}{
		{name: "held_transition", kind: "TEXT"},
		{name: "held_text", kind: "TEXT"},
		{name: "held_at", kind: "INTEGER"},
	} {
		if columns[column.name] {
			continue
		}
		if _, err := s.db.Exec(`ALTER TABLE open_answers ADD COLUMN ` + column.name + ` ` + column.kind); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrateOpenAnswerLearned() error {
	if _, err := s.db.Exec(`ALTER TABLE open_answers ADD COLUMN learned_at INTEGER NOT NULL DEFAULT 0`); err != nil &&
		!strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return fmt.Errorf("add learned_at: %w", err)
	}
	return nil
}

const openAnswerColumnsV71 = `id, chat_session_id, assistant_message_id, question, topic, settles, source,
	window_start, window_end, watched_event, mail_anchor_message_id, mail_correspondent,
	last_value, last_check_at, delivery, expires_at, posts, state, created_at, saw_at`

const openAnswerColumns = `id, chat_session_id, assistant_message_id, question, topic, settles, source,
	window_start, window_end, watched_event, mail_anchor_message_id, mail_correspondent,
	last_value, last_check_at, held_transition, held_text, held_at,
	delivery, expires_at, posts, state, created_at, saw_at, learned_at`

func scanOpenAnswer(row interface{ Scan(...any) error }) (OpenAnswerRow, error) {
	var a OpenAnswerRow
	var mailAnchor, correspondent, heldTransition, heldText sql.NullString
	var heldAt sql.NullInt64
	err := row.Scan(&a.ID, &a.ChatSessionID, &a.AssistantMessageID, &a.Question, &a.Topic,
		&a.Settles, &a.Source, &a.WindowStart, &a.WindowEnd, &a.WatchedEvent,
		&mailAnchor, &correspondent, &a.LastValue, &a.LastCheckAt, &heldTransition, &heldText, &heldAt, &a.Delivery,
		&a.ExpiresAt, &a.Posts, &a.State, &a.CreatedAt, &a.SawAt, &a.LearnedAt)
	a.MailAnchorMessageID, a.MailCorrespondent = mailAnchor.String, correspondent.String
	a.HeldTransition, a.HeldText, a.HeldAt = heldTransition.String, heldText.String, heldAt.Int64
	return a, err
}

// OpenAnswerTurn identifies the persisted text and provenance-bearing metadata
// used to evaluate a candidate. It is not evidence that a source is readable
// or checkable; callers must validate these values before admission.
type OpenAnswerTurn struct {
	UserID, UserContent, AnswerContent, RunID          string
	UserAutomationContext, UserImages, UserFiles       string
	AnswerAutomationContext, AnswerImages, AnswerFiles string
}

// CreateOpenAnswer atomically enforces the global limit and one record per
// assistant answer. This low-level method does not validate the originating
// chat turn, eligibility, source privacy, or an actionable check.
func (s *Store) CreateOpenAnswer(ctx context.Context, a OpenAnswerRow) error {
	return s.createOpenAnswer(ctx, a, nil, nil, "open", true)
}

// CreateOpenAnswerForTurn additionally requires the original user and answer
// rows to remain unchanged in the same main-chat run, with no later user turn
// or subsequent output from that run. The check and insert share a transaction
// so a rewind, edit or later turn cannot race the admission. This is
// only one admission prerequisite. This method does not create a durable
// actionable check and must not be used to publish a production promise by
// itself. A future admission path must establish current source provenance and
// privacy and commit the check and promise atomically or recoverably.
func (s *Store) CreateOpenAnswerForTurn(ctx context.Context, a OpenAnswerRow, turn OpenAnswerTurn) error {
	return s.createOpenAnswer(ctx, a, &turn, nil, "open", true)
}

// CreateOpenAnswerForTurnIfCurrent also fences source facts read from this
// store. Capture revision before those reads. This does not fence other stores
// or in-memory privacy settings, verify a source, or create a durable check.
// It is an admission prerequisite, not a production promise publisher.
func (s *Store) CreateOpenAnswerForTurnIfCurrent(ctx context.Context, a OpenAnswerRow, turn OpenAnswerTurn, revision ReadRevision) error {
	return s.createOpenAnswerForTurnIfCurrent(ctx, a, turn, revision, "open", true)
}

// CreateOpenAnswerAskForTurnIfCurrent stores a non-counting ask while retaining
// the same source revision, persisted-turn, and muted-kind fences as admission.
func (s *Store) CreateOpenAnswerAskForTurnIfCurrent(ctx context.Context, a OpenAnswerRow, turn OpenAnswerTurn, revision ReadRevision) error {
	return s.createOpenAnswerForTurnIfCurrent(ctx, a, turn, revision, "ask", false)
}

func (s *Store) createOpenAnswerForTurnIfCurrent(ctx context.Context, a OpenAnswerRow, turn OpenAnswerTurn, revision ReadRevision, state string, enforceLimit bool) error {
	release, err := s.lockContextUseRevision(ctx)
	if err != nil {
		return err
	}
	defer release()
	revision.changes += s.contextUses.changes
	return s.createOpenAnswer(ctx, a, &turn, &revision, state, enforceLimit)
}

func (s *Store) createOpenAnswer(ctx context.Context, a OpenAnswerRow, turn *OpenAnswerTurn, revision *ReadRevision, state string, enforceLimit bool) error {
	if strings.TrimSpace(a.ID) == "" || strings.TrimSpace(a.ChatSessionID) == "" || strings.TrimSpace(a.AssistantMessageID) == "" || strings.TrimSpace(a.Question) == "" || strings.TrimSpace(a.Topic) == "" || strings.TrimSpace(a.Source) == "" || a.CreatedAt <= 0 || a.ExpiresAt <= a.CreatedAt || a.Posts != 0 || a.State != state || (state != "open" && state != "ask") || a.SawAt != 0 {
		return errors.New("invalid open answer")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if revision != nil {
		// Reserve the writer before checking the stamp, including for writers
		// on other SQLite connections. The no-op does not advance the stamp.
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET updated_at=updated_at WHERE 0`); err != nil {
			return err
		}
		current, err := DatabaseRevision(ctx, tx)
		if err != nil {
			return err
		}
		if current != *revision {
			return ErrReadRevisionChanged
		}
	}
	if turn != nil {
		if strings.TrimSpace(turn.UserID) == "" || strings.TrimSpace(turn.RunID) == "" || strings.TrimSpace(turn.AnswerContent) == "" || strings.TrimSpace(turn.UserContent) != a.Question {
			return ErrOpenAnswerTurnChanged
		}
		var valid bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM stream_items u JOIN stream_items a ON a.session_local_id=u.session_local_id
			JOIN sessions s ON s.local_id=u.session_local_id
			WHERE s.kind='main' AND u.session_local_id=? AND u.id=? AND u.kind='user' AND u.tool_name='' AND u.run_id=? AND u.content=?
			AND u.automation_context=? AND u.images=? AND u.files=?
			AND a.id=? AND a.kind='assistant' AND a.tool_name='' AND a.run_id=? AND a.content=? AND a.sort_order>u.sort_order
			AND a.automation_context=? AND a.images=? AND a.files=?
			AND NOT EXISTS (SELECT 1 FROM stream_items later WHERE later.session_local_id=u.session_local_id AND later.sort_order>u.sort_order AND later.id<>a.id AND (later.kind='user' OR (later.run_id=a.run_id AND later.sort_order>a.sort_order AND later.kind IN ('assistant','tool','tool_use','tool_result'))))
		)`, a.ChatSessionID, turn.UserID, turn.RunID, turn.UserContent, turn.UserAutomationContext, turn.UserImages, turn.UserFiles,
			a.AssistantMessageID, turn.RunID, turn.AnswerContent, turn.AnswerAutomationContext, turn.AnswerImages, turn.AnswerFiles).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return ErrOpenAnswerTurnChanged
		}
	}
	// Check the preference in the insertion transaction, including records
	// saved before topic normalization was introduced.
	matched, err := openAnswerMatchingMutedKinds(ctx, tx, a.Topic)
	if err != nil {
		return err
	}
	if len(matched) != 0 {
		return ErrOpenAnswerKindMuted
	}
	if enforceLimit {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM open_answers WHERE state = 'open'`).Scan(&count); err != nil {
			return err
		}
		if count >= 5 {
			return ErrOpenAnswerLimit
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO open_answers (`+openAnswerColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.ChatSessionID, a.AssistantMessageID, a.Question, a.Topic, a.Settles, a.Source,
		a.WindowStart, a.WindowEnd, a.WatchedEvent, nullableOpenAnswerText(a.MailAnchorMessageID),
		nullableOpenAnswerText(a.MailCorrespondent), a.LastValue, a.LastCheckAt,
		nullableOpenAnswerText(a.HeldTransition), nullableOpenAnswerText(a.HeldText), nullableOpenAnswerMillis(a.HeldAt),
		a.Delivery, a.ExpiresAt, a.Posts, a.State, a.CreatedAt, a.SawAt, a.LearnedAt)
	if err != nil {
		return fmt.Errorf("create open answer: %w", err)
	}
	return tx.Commit()
}

// PromoteOpenAnswerAsk changes one ask into an open record in the same
// transaction that rechecks expiry, muted kinds, and the five-open limit. An
// optional revision fences source facts read while rebinding a Mail promise.
func (s *Store) PromoteOpenAnswerAsk(ctx context.Context, id string, now time.Time, revisions ...ReadRevision) error {
	if len(revisions) > 1 {
		return errors.New("at most one open-answer read revision may be supplied")
	}
	var revision *ReadRevision
	if len(revisions) == 1 {
		revision = &revisions[0]
		release, err := s.lockContextUseRevision(ctx)
		if err != nil {
			return err
		}
		defer release()
		revision.changes += s.contextUses.changes
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if revision != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET updated_at=updated_at WHERE 0`); err != nil {
			return err
		}
		current, err := DatabaseRevision(ctx, tx)
		if err != nil {
			return err
		}
		if current != *revision {
			return ErrReadRevisionChanged
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE open_answers SET state=state WHERE 0`); err != nil {
		return err
	}
	answer, err := scanOpenAnswer(tx.QueryRowContext(ctx, `SELECT `+openAnswerColumns+` FROM open_answers WHERE id=?`, id))
	if err != nil {
		return err
	}
	if answer.State != "ask" {
		return ErrOpenAnswerNotAsk
	}
	if answer.ExpiresAt <= now.UnixMilli() {
		return ErrOpenAnswerExpired
	}
	muted, err := openAnswerMatchingMutedKinds(ctx, tx, answer.Topic)
	if err != nil {
		return err
	}
	if len(muted) != 0 {
		return ErrOpenAnswerKindMuted
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM open_answers WHERE state='open'`).Scan(&count); err != nil {
		return err
	}
	if count >= 5 {
		return ErrOpenAnswerLimit
	}
	result, err := tx.ExecContext(ctx, `UPDATE open_answers SET state='open' WHERE id=? AND state='ask' AND expires_at>?`, id, now.UnixMilli())
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrOpenAnswerNotAsk
	}
	return tx.Commit()
}

// OpenAnswerAskForTurn returns only the ask attached to the latest assistant
// message before the persisted user turn in a main-chat session.
func (s *Store) OpenAnswerAskForTurn(ctx context.Context, sessionID, userID string) (OpenAnswerRow, bool, error) {
	answer, err := scanOpenAnswer(s.db.QueryRowContext(ctx, `SELECT `+openAnswerColumns+`
		FROM open_answers
		WHERE state='ask' AND chat_session_id=? AND assistant_message_id=(
			SELECT a.id FROM stream_items u
			JOIN sessions s ON s.local_id=u.session_local_id AND s.kind='main'
			JOIN stream_items a ON a.session_local_id=u.session_local_id AND a.kind='assistant' AND a.sort_order<u.sort_order
			JOIN open_answers ask ON ask.assistant_message_id=a.id AND ask.chat_session_id=a.session_local_id AND ask.state='ask'
			WHERE u.id=? AND u.session_local_id=? AND u.kind='user' AND u.tool_name=''
			ORDER BY a.sort_order DESC LIMIT 1
		) LIMIT 1`, sessionID, userID, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return OpenAnswerRow{}, false, nil
	}
	return answer, err == nil, err
}

// OpenAnswerAskForTurnByID returns the selected ask only when it precedes the
// persisted user turn in a main-chat session.
func (s *Store) OpenAnswerAskForTurnByID(ctx context.Context, sessionID, userID, askID string) (OpenAnswerRow, bool, error) {
	answer, err := scanOpenAnswer(s.db.QueryRowContext(ctx, `SELECT `+openAnswerColumns+`
		FROM open_answers
		WHERE id=? AND state='ask' AND chat_session_id=? AND assistant_message_id=(
			SELECT a.id FROM stream_items u
			JOIN sessions s ON s.local_id=u.session_local_id AND s.kind='main'
			JOIN stream_items a ON a.session_local_id=u.session_local_id AND a.kind='assistant' AND a.sort_order<u.sort_order
			JOIN open_answers ask ON ask.assistant_message_id=a.id AND ask.chat_session_id=a.session_local_id AND ask.state='ask' AND ask.id=?
			WHERE u.id=? AND u.session_local_id=? AND u.kind='user' AND u.tool_name=''
			ORDER BY a.sort_order DESC LIMIT 1
		) LIMIT 1`, askID, sessionID, askID, userID, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return OpenAnswerRow{}, false, nil
	}
	return answer, err == nil, err
}

// OpenAnswerSourceTurn returns the persisted user turn that produced an
// answer, for revalidating a Mail binding before promotion.
func (s *Store) OpenAnswerSourceTurn(ctx context.Context, id string) (sessionID, userID, runID string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT oa.chat_session_id,u.id,a.run_id
		FROM open_answers oa
		JOIN stream_items a ON a.id=oa.assistant_message_id AND a.session_local_id=oa.chat_session_id AND a.kind='assistant'
		JOIN stream_items u ON u.session_local_id=a.session_local_id AND u.kind='user' AND u.tool_name=''
			AND u.run_id=a.run_id AND u.content=oa.question AND u.sort_order<a.sort_order
		WHERE oa.id=? ORDER BY u.sort_order DESC LIMIT 1`, id).Scan(&sessionID, &userID, &runID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", false, nil
	}
	return sessionID, userID, runID, err == nil, err
}

func (s *Store) ListOpenAnswers(ctx context.Context) ([]OpenAnswerRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+openAnswerColumns+` FROM open_answers WHERE state = 'open' ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OpenAnswerRow{}
	for rows.Next() {
		a, err := scanOpenAnswer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetOpenAnswer(ctx context.Context, id string) (OpenAnswerRow, error) {
	return scanOpenAnswer(s.db.QueryRowContext(ctx, `SELECT `+openAnswerColumns+` FROM open_answers WHERE id = ?`, id))
}

// ChangeOpenAnswer applies only to an open record. It returns sql.ErrNoRows if
// the answer disappeared or already reached a terminal state.
func (s *Store) ChangeOpenAnswer(ctx context.Context, id, delivery string) error {
	switch delivery {
	case "around", "settled_only", "brief":
	default:
		return errors.New("invalid delivery")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE open_answers SET delivery = ? WHERE id = ? AND state = 'open'`, delivery, id)
	return openAnswerUpdated(result, err)
}

func (s *Store) RetimeOpenAnswer(ctx context.Context, id string, at, expiresAt int64) error {
	if strings.TrimSpace(id) == "" || at <= 0 || expiresAt <= at {
		return errors.New("invalid open-answer time")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE open_answers
		SET window_start=?, window_end=?, expires_at=?, last_value='', last_check_at=0,
			held_transition=NULL, held_text=NULL, held_at=NULL
		WHERE id=? AND state='open' AND window_start>0`, at, at, expiresAt, id)
	return openAnswerUpdated(result, err)
}

func (s *Store) RestoreOpenAnswerSchedule(ctx context.Context, prev OpenAnswerRow) error {
	if strings.TrimSpace(prev.ID) == "" {
		return errors.New("invalid open-answer id")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE open_answers SET
		window_start=?, window_end=?, expires_at=?, last_value=?, last_check_at=?,
		held_transition=?, held_text=?, held_at=?
		WHERE id=? AND state='open'`,
		prev.WindowStart, prev.WindowEnd, prev.ExpiresAt, prev.LastValue, prev.LastCheckAt,
		nullableOpenAnswerText(prev.HeldTransition), nullableOpenAnswerText(prev.HeldText),
		nullableOpenAnswerMillis(prev.HeldAt), prev.ID)
	return openAnswerUpdated(result, err)
}

func (s *Store) IsCurrentOpenAnswerChangeTurn(ctx context.Context, sessionID, userID, runID string) (bool, error) {
	var current bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM stream_items u
		JOIN sessions s ON s.local_id=u.session_local_id AND s.kind='main'
		WHERE u.id=? AND u.session_local_id=? AND u.kind='user' AND u.tool_name='' AND u.run_id=?
		AND NOT EXISTS (
			SELECT 1 FROM stream_items later
			WHERE later.session_local_id=u.session_local_id AND later.kind='user' AND later.sort_order>u.sort_order
		)
	)`, userID, sessionID, runID).Scan(&current)
	return current, err
}

func (s *Store) CancelOpenAnswer(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE open_answers SET state = 'cancelled',
		held_transition=NULL, held_text=NULL, held_at=NULL WHERE id = ? AND state = 'open'`, id)
	return openAnswerUpdated(result, err)
}

func nullableOpenAnswerText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullableOpenAnswerMillis(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func (s *Store) RecordOpenAnswerCheck(ctx context.Context, id, value string, checkedAt int64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE open_answers SET last_value=?, last_check_at=?
		WHERE id=? AND state='open' AND held_transition IS NULL`, value, checkedAt, id)
	return openAnswerUpdated(result, err)
}

// HoldOpenAnswerTransition stores a post for later release without changing
// the open state or adding a stream item. Settled outcomes outrank interim and
// gave-up outcomes if concurrent sources attempt to hold a post.
func (s *Store) HoldOpenAnswerTransition(ctx context.Context, id, transition, text string, at int64) error {
	switch transition {
	case "interim", "settled", "gave_up":
	default:
		return errors.New("invalid open answer transition")
	}
	if strings.TrimSpace(text) == "" || at <= 0 {
		return errors.New("a held post requires text and a time")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE open_answers SET held_at=held_at WHERE id=? AND state='open'`, id); err != nil {
		return err
	}
	answer, err := scanOpenAnswer(tx.QueryRowContext(ctx, `SELECT `+openAnswerColumns+` FROM open_answers WHERE id=?`, id))
	if err != nil {
		return err
	}
	if answer.State != "open" || answer.Posts >= 2 || transition == "interim" && answer.Posts != 0 {
		return errors.New("open answer is no longer postable")
	}
	if openAnswerHeldTransitionPriority(answer.HeldTransition) >= openAnswerHeldTransitionPriority(transition) {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE open_answers SET held_transition=?, held_text=?, held_at=?
		WHERE id=? AND state='open'`, transition, strings.TrimSpace(text), at, id); err != nil {
		return err
	}
	return tx.Commit()
}

func openAnswerHeldTransitionPriority(transition string) int {
	switch transition {
	case "settled":
		return 3
	case "interim":
		return 2
	case "gave_up":
		return 1
	default:
		return 0
	}
}

func openAnswerUpdated(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

// CommitOpenAnswerTransition atomically publishes a post with its lifecycle
// transition. The caller must first verify source evidence, privacy, timing,
// quiet hours and whether an interim contains new information. A saw_it
// transition has no message; the other transitions require a new chat row.
func (s *Store) CommitOpenAnswerTransition(ctx context.Context, id, transition string, at int64, message *StreamItemRow) error {
	switch transition {
	case "interim", "settled", "gave_up":
		if message == nil || strings.TrimSpace(message.ID) == "" || strings.TrimSpace(message.Content) == "" {
			return errors.New("a post requires a chat message")
		}
	case "saw_it":
		if message != nil || at <= 0 {
			return errors.New("saw_it requires a time and no post")
		}
	default:
		return errors.New("invalid open answer transition")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := scanOpenAnswer(tx.QueryRowContext(ctx, `SELECT `+openAnswerColumns+` FROM open_answers WHERE id = ?`, id))
	if err != nil {
		return err
	}
	if a.State != "open" || a.Posts >= 2 || a.HeldTransition != "" && a.HeldTransition != transition {
		return errors.New("open answer is no longer postable")
	}
	if transition == "interim" && a.Posts != 0 {
		return errors.New("an interim post has already been made")
	}
	if transition == "saw_it" {
		_, err = tx.ExecContext(ctx, `UPDATE open_answers SET state='saw_it', saw_at=?,
			held_transition=NULL, held_text=NULL, held_at=NULL WHERE id=?`, at, id)
	} else {
		// An existing ID is not a new post, even when it belongs to this chat.
		var exists int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM stream_items WHERE id=?`, message.ID).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			return errors.New("post message id already exists")
		}
		sortOrder, appendErr := appendStreamItem(ctx, tx, a.ChatSessionID, *message)
		if appendErr != nil {
			return appendErr
		}
		message.SortOrder = sortOrder
		if _, err = tx.ExecContext(ctx, `INSERT INTO open_answer_posts(answer_id, kind, message_id) VALUES (?,?,?)`, id, transition, message.ID); err != nil {
			return err
		}
		state := "open"
		switch transition {
		case "settled", "gave_up":
			state = transition
		}
		_, err = tx.ExecContext(ctx, `UPDATE open_answers SET posts=posts+1, state=?,
			held_transition=NULL, held_text=NULL, held_at=NULL WHERE id=?`, state, id)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// OpenAnswerForMessage finds either a promise or one of its posts. The post
// flag is derived from the durable post association, never renderer input.
func (s *Store) OpenAnswerForMessage(ctx context.Context, messageID string) (OpenAnswerRow, bool, error) {
	answer, post, _, err := s.OpenAnswerForMessageWithKind(ctx, messageID)
	return answer, post, err
}

// OpenAnswerForMessageWithKind also returns the durable post transition kind.
func (s *Store) OpenAnswerForMessageWithKind(ctx context.Context, messageID string) (OpenAnswerRow, bool, string, error) {
	var post bool
	var postKind string
	var answerID string
	err := s.db.QueryRowContext(ctx, `SELECT id, 0, '' FROM open_answers WHERE assistant_message_id=?
		UNION ALL SELECT answer_id, 1, kind FROM open_answer_posts WHERE message_id=? LIMIT 1`, messageID, messageID).Scan(&answerID, &post, &postKind)
	if err != nil {
		return OpenAnswerRow{}, false, "", err
	}
	a, err := s.GetOpenAnswer(ctx, answerID)
	return a, post, postKind, err
}

// OpenAnswerMessage records the stored association for a chat row.
type OpenAnswerMessage struct {
	Answer OpenAnswerRow
	Post   bool
}

// OpenAnswersForMessages avoids a database lookup for every ordinary chat
// message. Queries are chunked to stay below SQLite's parameter limit.
func (s *Store) OpenAnswersForMessages(ctx context.Context, ids []string) (map[string]OpenAnswerMessage, error) {
	out := make(map[string]OpenAnswerMessage)
	answers := make(map[string]OpenAnswerRow)
	const batchSize = 300
	for start := 0; start < len(ids); start += batchSize {
		end := min(start+batchSize, len(ids))
		batch := ids[start:end]
		marks := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, 0, len(batch)*2)
		for _, id := range batch {
			args = append(args, id)
		}
		for _, id := range batch {
			args = append(args, id)
		}
		rows, err := s.streamReader().QueryContext(ctx, `SELECT assistant_message_id,id,0 FROM open_answers WHERE assistant_message_id IN (`+marks+`)
			UNION ALL SELECT message_id,answer_id,1 FROM open_answer_posts WHERE message_id IN (`+marks+`)`, args...)
		if err != nil {
			return nil, err
		}
		type association struct {
			messageID, answerID string
			post                bool
		}
		var associations []association
		for rows.Next() {
			var item association
			if err := rows.Scan(&item.messageID, &item.answerID, &item.post); err != nil {
				rows.Close()
				return nil, err
			}
			associations = append(associations, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		for _, item := range associations {
			answer, ok := answers[item.answerID]
			if !ok {
				answer, err = scanOpenAnswer(s.streamReader().QueryRowContext(ctx, `SELECT `+openAnswerColumns+` FROM open_answers WHERE id = ?`, item.answerID))
				if err != nil {
					return nil, err
				}
				answers[item.answerID] = answer
			}
			out[item.messageID] = OpenAnswerMessage{Answer: answer, Post: item.post}
		}
	}
	return out, nil
}

func (s *Store) OpenAnswerBriefPosts(ctx context.Context, start, end time.Time) ([]OpenAnswerBriefPost, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.question, a.topic, a.source, p.message_id, i.content, i.created_at,
			EXISTS (
				SELECT 1 FROM engagement_moments m
				WHERE m.kind='answer_ready' AND m.subject_id=a.id
					AND json_extract(m.params, '$.message_id')=p.message_id
			),
			EXISTS (
				SELECT 1 FROM engagement_moments m
				JOIN engagement_receipts r ON r.moment_id=m.id
				WHERE m.kind='answer_ready' AND m.subject_id=a.id
					AND json_extract(m.params, '$.message_id')=p.message_id
					AND r.state IN (?, ?)
			),
			COALESCE((SELECT MIN(r.at) FROM engagement_moments m
				JOIN engagement_receipts r ON r.moment_id=m.id
				WHERE m.kind='answer_ready' AND m.subject_id=a.id
					AND json_extract(m.params, '$.message_id')=p.message_id
					AND r.state IN (?, ?)), 0)
		FROM open_answer_posts p
		JOIN open_answers a ON a.id=p.answer_id
		JOIN stream_items i ON i.id=p.message_id
		WHERE p.kind IN ('interim','settled') AND i.created_at >= ? AND i.created_at <= ?
		ORDER BY i.created_at DESC, i.sort_order DESC, p.message_id DESC
	`, EngagementReceiptOpened, EngagementReceiptActed, EngagementReceiptOpened, EngagementReceiptActed, start.UnixMilli(), end.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var posts []OpenAnswerBriefPost
	for rows.Next() {
		var post OpenAnswerBriefPost
		if err := rows.Scan(&post.Question, &post.Topic, &post.Source, &post.MessageID, &post.Content,
			&post.CreatedAt, &post.HasMoment, &post.OpenedOrActed, &post.OpenedAt); err != nil {
			return nil, err
		}
		posts = append(posts, post)
	}
	return posts, rows.Err()
}

type OpenAnswerLearnedPost struct {
	Kind      string
	SessionID string
	MessageID string
	SortOrder int
}

type unopenedAnswerPost struct {
	answerID, topic, sessionID, messageID string
	createdAt                             int64
	sortOrder                             int
	hasMoment, openedOrActed              bool
}

// LearnOpenAnswerKindsAfterUnopenedPosts mutes kinds whose two newest eligible
// answer posts are both more than 24 hours old and were never opened or acted.
func (s *Store) LearnOpenAnswerKindsAfterUnopenedPosts(ctx context.Context, kind string, now time.Time) ([]OpenAnswerLearnedPost, error) {
	if now.IsZero() {
		now = time.Now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	checkpoints, err := tx.QueryContext(ctx, `SELECT topic, learned_at FROM open_answers WHERE learned_at != 0`)
	if err != nil {
		return nil, err
	}
	learnedAtByKind := make(map[string]int64)
	for checkpoints.Next() {
		var topic string
		var learnedAt int64
		if err := checkpoints.Scan(&topic, &learnedAt); err != nil {
			checkpoints.Close()
			return nil, err
		}
		if learnedAt < 0 {
			learnedAt = -learnedAt
		}
		normalized := normalizeOpenAnswerKind(topic)
		if normalized != "" && learnedAt > learnedAtByKind[normalized] {
			learnedAtByKind[normalized] = learnedAt
		}
	}
	if err := checkpoints.Err(); err != nil {
		checkpoints.Close()
		return nil, err
	}
	if err := checkpoints.Close(); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT a.id, a.topic, a.chat_session_id, p.message_id, i.created_at, i.sort_order,
			EXISTS (
				SELECT 1 FROM engagement_moments m
				WHERE m.kind='answer_ready' AND m.subject_id=a.id
					AND json_extract(m.params, '$.message_id')=p.message_id
			),
			EXISTS (
				SELECT 1 FROM engagement_moments m
				JOIN engagement_receipts r ON r.moment_id=m.id
				WHERE m.kind='answer_ready' AND m.subject_id=a.id
					AND json_extract(m.params, '$.message_id')=p.message_id
					AND r.state IN (?, ?)
			)
		FROM open_answer_posts p
		JOIN open_answers a ON a.id=p.answer_id
		JOIN stream_items i ON i.id=p.message_id
		WHERE p.kind IN ('interim','settled')
		ORDER BY i.created_at DESC, i.sort_order DESC, p.message_id DESC
	`, EngagementReceiptOpened, EngagementReceiptActed)
	if err != nil {
		return nil, err
	}
	byKind := make(map[string][]unopenedAnswerPost)
	for rows.Next() {
		var post unopenedAnswerPost
		if err := rows.Scan(&post.answerID, &post.topic, &post.sessionID, &post.messageID, &post.createdAt,
			&post.sortOrder, &post.hasMoment, &post.openedOrActed); err != nil {
			rows.Close()
			return nil, err
		}
		normalized := normalizeOpenAnswerKind(post.topic)
		if normalized == "" || kind != "" && normalized != normalizeOpenAnswerKind(kind) {
			continue
		}
		if post.createdAt <= learnedAtByKind[normalized] {
			continue
		}
		byKind[normalized] = append(byKind[normalized], post)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var learned []OpenAnswerLearnedPost
	for _, posts := range byKind {
		if len(posts) < 2 || posts[0].createdAt >= now.Add(-24*time.Hour).UnixMilli() ||
			posts[1].createdAt >= now.Add(-24*time.Hour).UnixMilli() ||
			!posts[0].hasMoment || posts[0].openedOrActed ||
			!posts[1].hasMoment || posts[1].openedOrActed {
			continue
		}
		mutedKinds, err := openAnswerMatchingMutedKinds(ctx, tx, posts[0].topic)
		if err != nil {
			return nil, err
		}
		if len(mutedKinds) != 0 {
			continue
		}
		if err := setOpenAnswerKindMuted(ctx, tx, posts[0].topic, true); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE open_answers SET learned_at=? WHERE id=?`, now.UnixMilli(), posts[0].answerID); err != nil {
			return nil, err
		}
		learned = append(learned, OpenAnswerLearnedPost{
			Kind: posts[0].topic, SessionID: posts[0].sessionID,
			MessageID: posts[0].messageID, SortOrder: posts[0].sortOrder,
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return learned, nil
}

// normalizeOpenAnswerKind only removes textual differences. It does not
// classify semantically related topics into a stable kind.
func normalizeOpenAnswerKind(kind string) string {
	return norm.NFC.String(cases.Fold().String(strings.Join(strings.Fields(norm.NFC.String(kind)), " ")))
}

type openAnswerKindQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func openAnswerMatchingMutedKinds(ctx context.Context, q openAnswerKindQueryer, kind string) ([]string, error) {
	want := normalizeOpenAnswerKind(kind)
	rows, err := q.QueryContext(ctx, `SELECT kind FROM open_answer_muted_kinds`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var matches []string
	for rows.Next() {
		var stored string
		if err := rows.Scan(&stored); err != nil {
			return nil, err
		}
		if normalizeOpenAnswerKind(stored) == want {
			matches = append(matches, stored)
		}
	}
	return matches, rows.Err()
}

// SetOpenAnswerKindMuted persists an explicit user preference. Undo removes
// all equivalent legacy spellings as well as newly normalized preferences.
func (s *Store) SetOpenAnswerKindMuted(ctx context.Context, kind string, muted bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setOpenAnswerKindMuted(ctx, tx, kind, muted); err != nil {
		return err
	}
	return tx.Commit()
}

func setOpenAnswerKindMuted(ctx context.Context, tx *sql.Tx, kind string, muted bool) error {
	normalized := normalizeOpenAnswerKind(kind)
	if normalized == "" {
		return errors.New("invalid answer kind")
	}
	matches, err := openAnswerMatchingMutedKinds(ctx, tx, normalized)
	if err != nil {
		return err
	}
	switch muted {
	case true:
		if len(matches) == 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO open_answer_muted_kinds(kind) VALUES (?)`, normalized); err != nil {
				return err
			}
		}
	case false:
		for _, match := range matches {
			if _, err := tx.ExecContext(ctx, `DELETE FROM open_answer_muted_kinds WHERE kind=?`, match); err != nil {
				return err
			}
		}
		undoAt := time.Now().UnixMilli()
		rows, err := tx.QueryContext(ctx, `SELECT id, topic FROM open_answers WHERE learned_at != 0`)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id, topic string
			if err := rows.Scan(&id, &topic); err != nil {
				rows.Close()
				return err
			}
			if normalizeOpenAnswerKind(topic) == normalized {
				ids = append(ids, id)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE open_answers SET learned_at=? WHERE id=?`, -undoAt, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) OpenAnswerKindMuted(ctx context.Context, kind string) (bool, error) {
	matches, err := openAnswerMatchingMutedKinds(ctx, s.db, kind)
	return len(matches) != 0, err
}

func (s *Store) migrateOpenAnswerMutedKinds() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS open_answer_muted_kinds (kind TEXT PRIMARY KEY)`)
	return err
}

func (s *Store) migrateOpenAnswerChecks() error {
	for _, column := range []struct{ name, declaration string }{
		{"mail_anchor_message_id", "TEXT"},
		{"mail_correspondent", "TEXT"},
		{"last_value", "TEXT NOT NULL DEFAULT ''"},
		{"last_check_at", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if _, err := s.db.Exec(`ALTER TABLE open_answers ADD COLUMN ` + column.name + ` ` + column.declaration); err != nil &&
			!strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return fmt.Errorf("add %s: %w", column.name, err)
		}
	}
	return nil
}
