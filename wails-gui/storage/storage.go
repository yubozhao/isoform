package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Store provides SQLite-backed persistence for projects, sessions, and stream items.
type Store struct {
	db           *sql.DB
	streamReadDB *sql.DB
	contextUses  contextUseWriter
	closeMu      sync.Mutex
	closing      bool
	closeOnce    sync.Once
	closeErr     error
	operations   sync.WaitGroup
}

// hasColumn reports whether a table contains the given column.
// tableName/columnName are internal constants (not user input).
func (s *Store) hasColumn(tableName, columnName string) (bool, error) {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue any
			pk           int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == columnName {
			return true, nil
		}
	}

	if err := rows.Err(); err != nil {
		return false, err
	}

	return false, nil
}

// ensureProjectSortOrderColumn makes sure projects.sort_order exists.
// If a legacy projects.sort_ord column exists, copy values forward.
func (s *Store) ensureProjectSortOrderColumn() error {
	hasSortOrder, err := s.hasColumn("projects", "sort_order")
	if err != nil {
		return fmt.Errorf("check projects.sort_order: %w", err)
	}
	if hasSortOrder {
		return nil
	}

	if _, err := s.db.Exec("ALTER TABLE projects ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0"); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add projects.sort_order: %w", err)
		}
	}

	hasLegacySortOrd, err := s.hasColumn("projects", "sort_ord")
	if err != nil {
		return fmt.Errorf("check projects.sort_ord: %w", err)
	}

	if hasLegacySortOrd {
		if _, err := s.db.Exec("UPDATE projects SET sort_order = sort_ord"); err != nil {
			return fmt.Errorf("backfill projects.sort_order from sort_ord: %w", err)
		}
		return nil
	}

	if _, err := s.db.Exec(`
		UPDATE projects SET sort_order = (
			SELECT COUNT(*) FROM projects AS p2 WHERE p2.created_at < projects.created_at
				OR (p2.created_at = projects.created_at AND p2.id < projects.id)
		)
	`); err != nil {
		return fmt.Errorf("backfill projects.sort_order by created_at: %w", err)
	}

	return nil
}

// New opens (or creates) a SQLite database at dbPath and runs migrations.
func New(dbPath string) (*Store, error) {
	return newStore(dbPath, freeDiskBytes)
}

func newStore(dbPath string, availableBytes func(string) (uint64, error)) (*Store, error) {
	return newStoreWithSnapshotOptions(dbPath, snapshotOptions{availableBytes: availableBytes, removeFile: os.Remove})
}

func newStoreWithSnapshotOptions(dbPath string, snapshots snapshotOptions) (*Store, error) {
	// Reject newer databases through a read-only connection before opening a
	// writable handle or changing the journal mode.
	if err := checkExistingSchemaVersion(dbPath); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Use a single connection so PRAGMAs persist for all queries.
	// Desktop apps don't need connection pooling.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	version, err := checkSchemaVersion(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	if version < schemaVersion {
		if err := snapshotBeforeMigration(db, dbPath, version, snapshots); err != nil {
			db.Close()
			return nil, fmt.Errorf("snapshot before migration: %w", err)
		}
	} else {
		cleanupStalePendingSnapshots(dbPath, snapshots.removeFile)
	}

	// Enable WAL mode for better concurrent read performance.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}

	// Enable foreign keys.
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	// Remove orphaned rows left by earlier foreign-key enforcement gap.
	s.cleanupOrphans()
	// Stream pagination and its version check need not queue behind the single
	// writer connection. WAL readers see only committed data.
	streamReadDB, err := openReadOnlyDatabase(dbPath)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open stream readers: %w", err)
	}
	streamReadDB.SetMaxOpenConns(4)
	streamReadDB.SetMaxIdleConns(4)
	if err := streamReadDB.Ping(); err != nil {
		streamReadDB.Close()
		db.Close()
		return nil, fmt.Errorf("connect stream readers: %w", err)
	}
	s.streamReadDB = streamReadDB
	s.startContextUses()

	return s, nil
}

func (s *Store) streamReader() *sql.DB {
	if s.streamReadDB != nil {
		return s.streamReadDB
	}
	return s.db
}

// Closing reports whether Close has begun. It reads memory only, so a read can
// ask it without touching the database.
func (s *Store) Closing() bool {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closing
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.closeMu.Lock()
		s.closing = true
		s.closeMu.Unlock()
		s.operations.Wait()
		if s.contextUses.cancel != nil {
			s.contextUses.cancel()
			<-s.contextUses.done
			s.contextUses.mu.Lock()
			s.contextUses.closed = true
			s.contextUses.mu.Unlock()
		}
		if s.streamReadDB != nil {
			s.closeErr = s.streamReadDB.Close()
		}
		if s.db != nil {
			if err := s.db.Close(); s.closeErr == nil {
				s.closeErr = err
			}
		}
	})
	return s.closeErr
}

// migrateDefaultWorkspaces creates a default workspace for every existing project
// and assigns all existing sessions to that workspace. Called during v6 migration.
func (s *Store) migrateDefaultWorkspaces() error {
	rows, err := s.db.Query("SELECT id, active_session_id FROM projects")
	if err != nil {
		return err
	}
	defer rows.Close()

	type projectInfo struct {
		id              string
		activeSessionID string
	}
	var projects []projectInfo
	for rows.Next() {
		var p projectInfo
		if err := rows.Scan(&p.id, &p.activeSessionID); err != nil {
			return err
		}
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	now := time.Now().UnixMilli()
	for _, p := range projects {
		wsID := fmt.Sprintf("default-%s", p.id)

		// Create default workspace.
		if _, err := s.db.Exec(`
			INSERT OR IGNORE INTO workspaces (id, project_id, label, is_default, active_session_id, created_at, updated_at)
			VALUES (?, ?, 'Default', 1, ?, ?, ?)
		`, wsID, p.id, p.activeSessionID, now, now); err != nil {
			return fmt.Errorf("create default workspace for project %s: %w", p.id, err)
		}

		// Assign all sessions in this project to the default workspace.
		if _, err := s.db.Exec(
			"UPDATE sessions SET workspace_id = ? WHERE project_id = ? AND workspace_id = ''",
			wsID, p.id,
		); err != nil {
			return fmt.Errorf("assign sessions for project %s: %w", p.id, err)
		}

		// Set project's active workspace.
		if _, err := s.db.Exec(
			"UPDATE projects SET active_workspace_id = ? WHERE id = ? AND active_workspace_id = ''",
			wsID, p.id,
		); err != nil {
			return fmt.Errorf("set active workspace for project %s: %w", p.id, err)
		}
	}

	return nil
}

// cleanupOrphans removes rows whose parent was never written or was deleted
// before foreign-key enforcement was active. Wrapped in a transaction so the
// deletes are atomic; errors are logged but non-fatal.
func (s *Store) cleanupOrphans() {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		log.Printf("cleanupOrphans: begin tx: %v", err)
		return
	}
	defer tx.Rollback()

	stmts := []string{
		`DELETE FROM stream_items WHERE session_local_id IN (
			SELECT local_id FROM sessions WHERE project_id NOT IN (SELECT id FROM projects)
		)`,
		`DELETE FROM sessions WHERE project_id NOT IN (SELECT id FROM projects)`,
		`DELETE FROM sessions WHERE workspace_id != '' AND workspace_id NOT IN (SELECT id FROM workspaces)`,
		`DELETE FROM workspaces WHERE project_id NOT IN (SELECT id FROM projects)`,
		`DELETE FROM stream_items WHERE session_local_id NOT IN (SELECT local_id FROM sessions)`,
		// Clean up orphaned checkpoint_files whose session no longer exists.
		`DELETE FROM checkpoint_files WHERE session_local_id NOT IN (SELECT local_id FROM sessions)`,
		// Clean up orphaned git_snapshots whose session no longer exists.
		`DELETE FROM git_snapshots WHERE session_local_id NOT IN (SELECT local_id FROM sessions)`,
		// Clear dangling active_session_id references.
		`UPDATE projects SET active_session_id = '' WHERE active_session_id != '' AND active_session_id NOT IN (SELECT local_id FROM sessions)`,
		`UPDATE workspaces SET active_session_id = '' WHERE active_session_id != '' AND active_session_id NOT IN (SELECT local_id FROM sessions)`,
		// Clean up sidebar group items whose group no longer exists.
		`DELETE FROM sidebar_group_items WHERE group_id NOT IN (SELECT id FROM sidebar_groups)`,
		// Clean up sidebar group items referencing chat sessions that no longer exist.
		`DELETE FROM sidebar_group_items WHERE item_type = 'chat' AND ref_id NOT IN (SELECT local_id FROM sessions)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			log.Printf("cleanupOrphans: %v", err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("cleanupOrphans: commit: %v", err)
	}
}

// Vacuum runs VACUUM on the database to reclaim free space after large deletions.
func (s *Store) Vacuum() error {
	_, err := s.db.Exec("VACUUM")
	return err
}

// ClearActiveSessionReferences clears any project or workspace active_session_id
// that references the given session, preventing dangling references after deletion.
func (s *Store) ClearActiveSessionReferences(ctx context.Context, sessionLocalID string) error {
	if _, err := s.db.ExecContext(ctx,
		"UPDATE projects SET active_session_id = '' WHERE active_session_id = ?",
		sessionLocalID,
	); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		"UPDATE workspaces SET active_session_id = '' WHERE active_session_id = ?",
		sessionLocalID,
	); err != nil {
		return err
	}
	return nil
}

// schemaVersion is the latest migration this binary implements and may stamp.
const schemaVersion = 74

// supportedSchemaVersion is the audited compatibility ceiling. Keep the newer
// stamp refusal in place when raising this ceiling alongside implemented
// migrations. Contact provenance uses v61, contact links v62. RC 1 topics (#140)
// v63, context uses v64, People merges v65, answer chips v66, outbound sends v67.
const supportedSchemaVersion = 74

// SchemaVersion is the schema this binary stamps, for tests outside the package.
const SchemaVersion = schemaVersion

func (s *Store) migrate() error {
	version, err := checkSchemaVersion(s.db)
	if err != nil {
		return err
	}
	if version > schemaVersion {
		// Preserve a supported sibling generation without replaying older
		// repair migrations or downgrading its stamp. Proactive structures
		// are additive and must also exist on a sibling-stamped database.
		return s.ensureProactiveStructures()
	}

	if version < 1 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS projects (
				id                TEXT PRIMARY KEY,
				name              TEXT NOT NULL,
				path              TEXT NOT NULL UNIQUE,
				organization      TEXT NOT NULL DEFAULT '',
				product           TEXT NOT NULL DEFAULT '',
				is_yansu_project  INTEGER NOT NULL DEFAULT 0,
				active_session_id TEXT NOT NULL DEFAULT '',
				created_at        INTEGER NOT NULL,
				updated_at        INTEGER NOT NULL
			);

			CREATE TABLE IF NOT EXISTS sessions (
				local_id    TEXT PRIMARY KEY,
				project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
				session_id  TEXT NOT NULL DEFAULT '',
				label       TEXT NOT NULL,
				model       TEXT NOT NULL,
				prd_enabled INTEGER NOT NULL DEFAULT 0,
				created_at  INTEGER NOT NULL,
				updated_at  INTEGER NOT NULL
			);

			CREATE TABLE IF NOT EXISTS stream_items (
				id               TEXT PRIMARY KEY,
				session_local_id TEXT NOT NULL REFERENCES sessions(local_id) ON DELETE CASCADE,
				kind             TEXT NOT NULL,
				content          TEXT NOT NULL DEFAULT '',
				tool_name        TEXT NOT NULL DEFAULT '',
				tool_input       TEXT NOT NULL DEFAULT '',
				run_id           TEXT NOT NULL DEFAULT '',
				session_id       TEXT NOT NULL DEFAULT '',
				sort_order       INTEGER NOT NULL,
				created_at       INTEGER NOT NULL
			);

			CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project_id);
			CREATE INDEX IF NOT EXISTS idx_stream_items_session ON stream_items(session_local_id, sort_order);

			CREATE TABLE IF NOT EXISTS ui_state (
				key   TEXT PRIMARY KEY,
				value TEXT NOT NULL
			);
		`); err != nil {
			return fmt.Errorf("migration v1: %w", err)
		}
	}

	if version < 2 {
		addColumns := []string{
			"ALTER TABLE sessions ADD COLUMN thinking_level TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE sessions ADD COLUMN plan_mode INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE sessions ADD COLUMN thinking_enabled INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE sessions ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE sessions ADD COLUMN pinned_at INTEGER NOT NULL DEFAULT 0",
		}
		for _, stmt := range addColumns {
			if _, err := s.db.Exec(stmt); err != nil {
				if !strings.Contains(err.Error(), "duplicate column") {
					return fmt.Errorf("migration v2: %w", err)
				}
			}
		}
	}

	if version < 3 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS checkpoint_files (
				id               INTEGER PRIMARY KEY AUTOINCREMENT,
				session_local_id TEXT NOT NULL,
				sort_order       INTEGER NOT NULL,
				file_path        TEXT NOT NULL,
				backup_path      TEXT NOT NULL,
				existed          INTEGER NOT NULL DEFAULT 1,
				created_at       INTEGER NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_checkpoint_session_order
				ON checkpoint_files(session_local_id, sort_order);
		`); err != nil {
			return fmt.Errorf("migration v3: %w", err)
		}
	}

	if version < 5 {
		addColumns := []string{
			"ALTER TABLE sessions ADD COLUMN workspace_type TEXT NOT NULL DEFAULT 'session'",
			"ALTER TABLE sessions ADD COLUMN worktree_path TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE sessions ADD COLUMN worktree_branch TEXT NOT NULL DEFAULT ''",
		}
		for _, stmt := range addColumns {
			if _, err := s.db.Exec(stmt); err != nil {
				if !strings.Contains(err.Error(), "duplicate column") {
					return fmt.Errorf("migration v5: %w", err)
				}
			}
		}
	}

	if version < 6 {
		// Create workspaces table.
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS workspaces (
				id                TEXT PRIMARY KEY,
				project_id        TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
				label             TEXT NOT NULL,
				is_default        INTEGER NOT NULL DEFAULT 0,
				worktree_path     TEXT NOT NULL DEFAULT '',
				worktree_branch   TEXT NOT NULL DEFAULT '',
				base_branch       TEXT NOT NULL DEFAULT '',
				active_session_id TEXT NOT NULL DEFAULT '',
				created_at        INTEGER NOT NULL,
				updated_at        INTEGER NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_workspaces_project ON workspaces(project_id);
		`); err != nil {
			return fmt.Errorf("migration v6 create workspaces: %w", err)
		}

		// Add workspace_id to sessions.
		if _, err := s.db.Exec("ALTER TABLE sessions ADD COLUMN workspace_id TEXT NOT NULL DEFAULT ''"); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migration v6 add workspace_id: %w", err)
			}
		}

		// Add active_workspace_id to projects.
		if _, err := s.db.Exec("ALTER TABLE projects ADD COLUMN active_workspace_id TEXT NOT NULL DEFAULT ''"); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migration v6 add active_workspace_id: %w", err)
			}
		}

		// Migrate existing data: create default workspace for each project.
		if err := s.migrateDefaultWorkspaces(); err != nil {
			return fmt.Errorf("migration v6 default workspaces: %w", err)
		}
	}

	// Ensure v7 schema is present even when older builds stamped user_version
	// without actually adding projects.sort_order.
	if err := s.ensureProjectSortOrderColumn(); err != nil {
		return fmt.Errorf("migration v7 ensure sort_order: %w", err)
	}

	// Ensure v8 schema is present even when older builds stamped user_version
	// without actually adding projects.setup_command.
	if _, err := s.db.Exec("ALTER TABLE projects ADD COLUMN setup_command TEXT NOT NULL DEFAULT ''"); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migration v8 add setup_command: %w", err)
		}
	}

	// v9: git_snapshots table for Codex worktree snapshots.
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS git_snapshots (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			session_local_id TEXT NOT NULL,
			sort_order       INTEGER NOT NULL,
			stash_sha        TEXT NOT NULL,
			untracked_files  TEXT NOT NULL DEFAULT '',
			work_dir         TEXT NOT NULL,
			created_at       INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_git_snapshots_session_order
			ON git_snapshots(session_local_id, sort_order);
	`); err != nil {
		return fmt.Errorf("migration v9: %w", err)
	}

	// v10: add sort_order to workspaces for drag-and-drop reordering.
	if _, err := s.db.Exec("ALTER TABLE workspaces ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0"); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migration v10 add workspaces.sort_order: %w", err)
		}
	}

	// v11: add UNIQUE constraint on git_snapshots(session_local_id, sort_order).
	// Without this, rewind-then-re-run creates duplicate rows at the same
	// sort_order, causing LoadGitSnapshotAtOrBefore to return a stale snapshot.
	// SQLite does not support ADD CONSTRAINT, so we recreate the index as UNIQUE.
	if _, err := s.db.Exec(`
		DROP INDEX IF EXISTS idx_git_snapshots_session_order;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_git_snapshots_session_order
			ON git_snapshots(session_local_id, sort_order);
	`); err != nil {
		// If unique index creation fails due to existing duplicates, clean them
		// up first: keep only the latest row per (session_local_id, sort_order).
		if _, err2 := s.db.Exec(`
			DELETE FROM git_snapshots WHERE id NOT IN (
				SELECT MAX(id) FROM git_snapshots GROUP BY session_local_id, sort_order
			);
			DROP INDEX IF EXISTS idx_git_snapshots_session_order;
			CREATE UNIQUE INDEX IF NOT EXISTS idx_git_snapshots_session_order
				ON git_snapshots(session_local_id, sort_order);
		`); err2 != nil {
			return fmt.Errorf("migration v11 git_snapshots unique index: %w (cleanup: %v)", err, err2)
		}
	}

	// v12: add head_sha column to git_snapshots for full worktree snapshot support.
	// The snapshot now uses write-tree + commit-tree (not git stash create), so
	// we record the HEAD SHA at snapshot time to support HEAD-aware restore.
	if _, err := s.db.Exec("ALTER TABLE git_snapshots ADD COLUMN head_sha TEXT NOT NULL DEFAULT ''"); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migration v12 add head_sha: %w", err)
		}
	}

	// v13/v14: removed — these added crystal-app columns (bound_crystal_id,
	// crystal_creation) that no longer exist. Version numbers stay reserved.

	// v15: sidebar custom groups.
	// v15+: sidebar groups. Always run CREATE TABLE IF NOT EXISTS for
	// compatibility with databases migrated from the SSH feature branch
	// where version was bumped to 18 before these tables existed.
	{
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS sidebar_groups (
				id         TEXT PRIMARY KEY,
				name       TEXT NOT NULL,
				sort_order INTEGER NOT NULL DEFAULT 0,
				collapsed  INTEGER NOT NULL DEFAULT 0,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			);

			CREATE TABLE IF NOT EXISTS sidebar_group_items (
				id         TEXT PRIMARY KEY,
				group_id   TEXT NOT NULL REFERENCES sidebar_groups(id) ON DELETE CASCADE,
				item_type  TEXT NOT NULL,
				ref_id     TEXT NOT NULL,
				sort_order INTEGER NOT NULL DEFAULT 0,
				added_at   INTEGER NOT NULL
			);

			CREATE UNIQUE INDEX IF NOT EXISTS idx_sidebar_group_items_unique
				ON sidebar_group_items(group_id, item_type, ref_id);
		`); err != nil {
			return fmt.Errorf("migration v15 sidebar groups: %w", err)
		}
	}

	// v16: cron scheduler tables and hidden column for cron-owned sessions.
	{
		hiddenCols := []string{
			"ALTER TABLE projects ADD COLUMN hidden INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE workspaces ADD COLUMN hidden INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE sessions ADD COLUMN hidden INTEGER NOT NULL DEFAULT 0",
		}
		for _, stmt := range hiddenCols {
			if _, err := s.db.Exec(stmt); err != nil {
				if !strings.Contains(err.Error(), "duplicate column") {
					return fmt.Errorf("migration v16 add hidden column: %w", err)
				}
			}
		}

		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS cron_jobs (
				id                 TEXT PRIMARY KEY,
				name               TEXT NOT NULL,
				description        TEXT NOT NULL DEFAULT '',
				enabled            INTEGER NOT NULL DEFAULT 1,
				schedule_kind      TEXT NOT NULL,
				schedule_config    TEXT NOT NULL,
				target_kind        TEXT NOT NULL DEFAULT 'agent',
				target_config      TEXT NOT NULL,
				delete_after_run   INTEGER NOT NULL DEFAULT 0,
				retry_policy       TEXT NOT NULL DEFAULT '{}',
				next_run_at        INTEGER NOT NULL DEFAULT 0,
				last_run_at        INTEGER NOT NULL DEFAULT 0,
				last_run_status    TEXT NOT NULL DEFAULT '',
				last_error         TEXT NOT NULL DEFAULT '',
				consecutive_errors INTEGER NOT NULL DEFAULT 0,
				retry_attempt      INTEGER NOT NULL DEFAULT 0,
				retry_run_at       INTEGER NOT NULL DEFAULT 0,
				created_at         INTEGER NOT NULL,
				updated_at         INTEGER NOT NULL
			);

			CREATE TABLE IF NOT EXISTS cron_runs (
				id             TEXT PRIMARY KEY,
				job_id         TEXT NOT NULL REFERENCES cron_jobs(id) ON DELETE CASCADE,
				started_at     INTEGER NOT NULL,
				finished_at    INTEGER NOT NULL DEFAULT 0,
				status         TEXT NOT NULL DEFAULT 'running',
				trigger_kind   TEXT NOT NULL DEFAULT 'scheduled',
				error          TEXT NOT NULL DEFAULT '',
				duration_ms    INTEGER NOT NULL DEFAULT 0,
				result_summary TEXT NOT NULL DEFAULT ''
			);

			CREATE INDEX IF NOT EXISTS idx_cron_runs_job ON cron_runs(job_id, started_at DESC);
		`); err != nil {
			return fmt.Errorf("migration v16 cron tables: %w", err)
		}
	}

	// v17: add source_session_id to cron_jobs.
	{
		if _, err := s.db.Exec(`ALTER TABLE cron_jobs ADD COLUMN source_session_id TEXT NOT NULL DEFAULT ''`); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migration v17 add source_session_id: %w", err)
			}
		}
	}

	// v18: add session_tag and item_sort_start to cron_runs for run log loading.
	{
		addCols := []string{
			"ALTER TABLE cron_runs ADD COLUMN session_tag TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE cron_runs ADD COLUMN item_sort_start INTEGER NOT NULL DEFAULT -1",
		}
		for _, stmt := range addCols {
			if _, err := s.db.Exec(stmt); err != nil {
				if !strings.Contains(err.Error(), "duplicate column") {
					return fmt.Errorf("migration v18: %w", err)
				}
			}
		}
	}

	// v19: add source_platform to cron_jobs.
	{
		if _, err := s.db.Exec(`ALTER TABLE cron_jobs ADD COLUMN source_platform TEXT NOT NULL DEFAULT ''`); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migration v19 add source_platform: %w", err)
			}
		}
	}

	// v20: add remote_env to workspaces + create ssh_configs table.
	if version < 20 {
		if _, err := s.db.Exec("ALTER TABLE workspaces ADD COLUMN remote_env TEXT NOT NULL DEFAULT ''"); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migration v20 add remote_env: %w", err)
			}
		}

		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS ssh_configs (
				id         TEXT PRIMARY KEY,
				name       TEXT NOT NULL,
				host       TEXT NOT NULL,
				port       INTEGER NOT NULL DEFAULT 22,
				user       TEXT NOT NULL DEFAULT '',
				auth_type  TEXT NOT NULL DEFAULT 'key',
				key_path   TEXT NOT NULL DEFAULT '',
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)
		`); err != nil {
			return fmt.Errorf("migration v20 create ssh_configs: %w", err)
		}
	}

	// v21: add exec_mode and cli_tool columns to ssh_configs.
	if version < 21 {
		for _, col := range []struct{ name, def string }{
			{"exec_mode", "TEXT NOT NULL DEFAULT 'subscription'"},
			{"cli_tool", "TEXT NOT NULL DEFAULT ''"},
		} {
			if _, err := s.db.Exec(fmt.Sprintf("ALTER TABLE ssh_configs ADD COLUMN %s %s", col.name, col.def)); err != nil {
				if !strings.Contains(err.Error(), "duplicate column") {
					return fmt.Errorf("migration v21 add %s: %w", col.name, err)
				}
			}
		}
	}

	// v22: add password column to ssh_configs.
	if version < 22 {
		if _, err := s.db.Exec("ALTER TABLE ssh_configs ADD COLUMN password TEXT NOT NULL DEFAULT ''"); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migration v22 add password: %w", err)
			}
		}
	}

	// v23: add auto_connect column to ssh_configs.
	if version < 23 {
		if _, err := s.db.Exec("ALTER TABLE ssh_configs ADD COLUMN auto_connect INTEGER NOT NULL DEFAULT 0"); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migration v23 add auto_connect: %w", err)
			}
		}
	}

	// v24: add fingerprint column to cron_jobs for automation dedup.
	if version < 24 {
		if _, err := s.db.Exec(`ALTER TABLE cron_jobs ADD COLUMN fingerprint TEXT NOT NULL DEFAULT ''`); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migration v24 add fingerprint: %w", err)
			}
		}
		if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_cron_jobs_fingerprint ON cron_jobs(fingerprint) WHERE fingerprint != ''`); err != nil {
			return fmt.Errorf("migration v24 fingerprint index: %w", err)
		}
	}

	// v25: add notify_result column to cron_runs for DM notification tracking.
	if version < 25 {
		if _, err := s.db.Exec(`ALTER TABLE cron_runs ADD COLUMN notify_result TEXT NOT NULL DEFAULT ''`); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migration v25 add notify_result: %w", err)
			}
		}
	}

	// v26: add notify_dm column to cron_jobs (default true = 1).
	if version < 26 {
		if _, err := s.db.Exec(`ALTER TABLE cron_jobs ADD COLUMN notify_dm INTEGER NOT NULL DEFAULT 1`); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migration v26 add notify_dm: %w", err)
			}
		}
	}

	// v27: add automation_result column to sessions.
	// Run unconditionally (like setup_command / sort_order) so databases
	// whose user_version was bumped past 27 by another build still get the column.
	if _, err := s.db.Exec(`ALTER TABLE sessions ADD COLUMN automation_result INTEGER NOT NULL DEFAULT 0`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migration v27 add automation_result: %w", err)
		}
	}

	// v28: removed — created the crystal_versions table for crystal-app build
	// history, which no longer exists. Version number stays reserved.

	// v29: add sort_order to cron_jobs for drag-and-drop reordering.
	if version < 29 {
		if _, err := s.db.Exec("ALTER TABLE cron_jobs ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0"); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migration v29 add cron_jobs.sort_order: %w", err)
			}
		}
	}

	// v30: add notify_platform column to cron_jobs (default empty = legacy auto-pick).
	if version < 30 {
		if _, err := s.db.Exec(`ALTER TABLE cron_jobs ADD COLUMN notify_platform TEXT NOT NULL DEFAULT ''`); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migration v30 add notify_platform: %w", err)
			}
		}
	}

	// v31: add index on cron_runs(started_at) for date-range queries (Timeline).
	if version < 31 {
		if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_cron_runs_started ON cron_runs(started_at DESC)`); err != nil {
			return fmt.Errorf("migration v31 cron_runs started_at index: %w", err)
		}
	}

	if version < 32 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS extractor_records (
				id               TEXT PRIMARY KEY,
				pipeline_run_id  TEXT NOT NULL,
				platform         TEXT NOT NULL,
				channel_name     TEXT NOT NULL,
				processed_at     INTEGER NOT NULL,
				title            TEXT NOT NULL,
				full_output      TEXT NOT NULL,
				source_digest    TEXT NOT NULL,
				deliverables     TEXT NOT NULL DEFAULT '[]',
				message_count    INTEGER NOT NULL,
				participants     TEXT,
				work_dir         TEXT NOT NULL DEFAULT '',
				session_temp_dir TEXT NOT NULL DEFAULT '',
				created_at       INTEGER NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_extractor_records_processed_at ON extractor_records(processed_at);
			CREATE INDEX IF NOT EXISTS idx_extractor_records_platform ON extractor_records(platform, processed_at);
		`); err != nil {
			return fmt.Errorf("migration v32 extractor_records: %w", err)
		}
	}

	// v33: enforce one extractor_records row per pipeline_run_id so retries
	// upsert in place instead of inflating the table (and Calendar counts).
	// Also speeds up GetExtractorFullOutputByPipelineRunID lookups by avoiding
	// a full table scan when expanding history entries.
	if version < 33 {
		// Drop pre-existing duplicates before adding the UNIQUE index.
		//
		// Tiebreaker matters: the previous version of this query used
		// `WHERE created_at = MAX(...) GROUP BY pipeline_run_id` and relied on
		// SQLite's bare-column behavior to pick one id per group. When two
		// rows for the same pipeline_run_id share a created_at (entirely
		// possible at ms granularity for fast retries written pre-UPSERT),
		// SQLite would pick the surviving row arbitrarily — non-deterministic
		// and could drop the row with the more complete full_output.
		//
		// ROW_NUMBER() with an explicit ORDER BY (created_at DESC, id DESC)
		// gives a stable, reproducible winner per pipeline_run_id. id DESC is
		// just a deterministic tiebreaker; UUIDs aren't ordered semantically
		// but string comparison is well-defined.
		if _, err := s.db.Exec(`
			WITH ranked AS (
				SELECT id, ROW_NUMBER() OVER (
					PARTITION BY pipeline_run_id
					ORDER BY created_at DESC, id DESC
				) AS rn
				FROM extractor_records
			)
			DELETE FROM extractor_records
			WHERE id IN (SELECT id FROM ranked WHERE rn > 1)
		`); err != nil {
			return fmt.Errorf("migration v33 extractor_records dedupe: %w", err)
		}
		if _, err := s.db.Exec(`
			CREATE UNIQUE INDEX IF NOT EXISTS idx_extractor_records_pipeline_run_id
			ON extractor_records(pipeline_run_id)
		`); err != nil {
			return fmt.Errorf("migration v33 extractor_records pipeline_run_id index: %w", err)
		}
	}

	// v34: add schedule_evidence column to cron_jobs. When activity triage
	// extracts an automation with an explicit recurring schedule, the LLM must
	// cite a verbatim quote from the source text; we persist it for debug
	// ("why did this fire?") and to be cleared when the user edits the schedule
	// manually, marking the schedule as a user decision rather than LLM-inferred.
	if version < 34 {
		if _, err := s.db.Exec(`ALTER TABLE cron_jobs ADD COLUMN schedule_evidence TEXT NOT NULL DEFAULT ''`); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migration v34 add schedule_evidence: %w", err)
			}
		}
	}

	// v35: composer_suggestions — cached LLM-generated prompt chips shown
	// under the Composer in empty-state. Refreshed roughly hourly by the
	// suggestion generator. Older rows are purged in place.
	if version < 35 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS composer_suggestions (
				id           TEXT PRIMARY KEY,
				generated_at INTEGER NOT NULL,
				suggestions  TEXT NOT NULL,
				source_hash  TEXT NOT NULL DEFAULT '',
				expires_at   INTEGER NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_composer_suggestions_generated_at
				ON composer_suggestions(generated_at DESC);
		`); err != nil {
			return fmt.Errorf("migration v35 composer_suggestions: %w", err)
		}
	}

	// v36: persist user-attached images on stream_items so uploads survive
	// session reload. Previously images lived only in frontend memory and
	// vanished the next time the chat was opened.
	if version < 36 {
		if _, err := s.db.Exec(`ALTER TABLE stream_items ADD COLUMN images TEXT NOT NULL DEFAULT ''`); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return fmt.Errorf("migration v36 stream_items.images: %w", err)
			}
		}
	}

	// v37: rename legacy cron target_kind from the retired subprocess name
	// to the neutral 'agent' used by the native in-process runner.
	if version < 37 {
		if _, err := s.db.Exec(`UPDATE cron_jobs SET target_kind = 'agent' WHERE target_kind = 'acp_agent'`); err != nil {
			return fmt.Errorf("migration v37 rename target_kind: %w", err)
		}
	}

	// v38: add tool_use_id column to stream_items so history can rebuild
	// paired tool_use/tool_result blocks for the Anthropic API.
	if _, err := s.db.Exec(`ALTER TABLE stream_items ADD COLUMN tool_use_id TEXT NOT NULL DEFAULT ''`); err != nil {
		// Column already exists (idempotent).
	}

	// v39: add surface_kind column to cron_jobs so the sidebar can route jobs
	// to the right sub-group under "Automations". Empty string is the legacy default
	// (treated as 'scheduled'); 'handoff' marks one-shot jobs created from the
	// activity-monitor handoff proposal flow.
	if version < 39 {
		if _, err := s.db.Exec(`ALTER TABLE cron_jobs ADD COLUMN surface_kind TEXT NOT NULL DEFAULT ''`); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migration v39 add surface_kind: %w", err)
			}
		}
	}

	// v40: rewrite any leftover path="" project owned by a random-UUID id to
	// the canonical __standalone_chat__ sentinel. Older builds of
	// activeProjectAndWorkspace step 3 minted random UUIDs against path="",
	// which kept the slot occupied so subsequent callers (Spotlight,
	// useComposerInput) that pass id=__standalone_chat__ + path="" silently
	// no-op'd under the chained ON CONFLICT(path) introduced in #1741, then
	// FK-failed on the downstream workspace/session insert.
	if version < 40 {
		if err := migrateLegacyStandaloneChat(s.db); err != nil {
			return fmt.Errorf("migration v40 standalone-chat sentinel: %w", err)
		}
	}

	// v41: identity tables. Stores who the local user is (self), who they
	// know (contacts), and which per-app handles belong to each principal.
	// Activity Recorder consumers (renderParsedContentSection, the memory
	// pipeline) resolve incoming chat sender names against these tables to
	// avoid attributing third-party messages to the local user.
	if version < 41 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS principals (
				id            TEXT PRIMARY KEY,
				kind          TEXT NOT NULL,
				display_name  TEXT NOT NULL DEFAULT '',
				relationship  TEXT NOT NULL DEFAULT '',
				notes         TEXT NOT NULL DEFAULT '',
				created_from  TEXT NOT NULL DEFAULT '',
				confidence    REAL NOT NULL DEFAULT 1.0,
				created_at    INTEGER NOT NULL,
				updated_at    INTEGER NOT NULL
			);

			CREATE TABLE IF NOT EXISTS principal_aliases (
				principal_id  TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
				alias         TEXT NOT NULL,
				PRIMARY KEY (principal_id, alias)
			);

			CREATE TABLE IF NOT EXISTS app_handles (
				id             INTEGER PRIMARY KEY AUTOINCREMENT,
				principal_id   TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
				app            TEXT NOT NULL,
				handle_id      TEXT NOT NULL DEFAULT '',
				display_handle TEXT NOT NULL DEFAULT '',
				created_at     INTEGER NOT NULL,
				UNIQUE (app, handle_id, display_handle)
			);

			CREATE INDEX IF NOT EXISTS idx_principals_kind
				ON principals(kind);
			CREATE INDEX IF NOT EXISTS idx_principal_aliases_alias
				ON principal_aliases(alias);
			CREATE INDEX IF NOT EXISTS idx_app_handles_lookup
				ON app_handles(app, handle_id);
			CREATE INDEX IF NOT EXISTS idx_app_handles_display
				ON app_handles(app, display_handle);
			CREATE UNIQUE INDEX IF NOT EXISTS uq_principals_self
				ON principals(kind) WHERE kind = 'self';
		`); err != nil {
			return fmt.Errorf("migration v41 identity tables: %w", err)
		}
	}

	// v42: principal sightings tables. The activity render path UPSERTs into
	// principal_sightings whenever a chat message resolves to a known
	// Principal, and writes a rolling-window message preview into
	// principal_recent_messages. The Who you know page queries these to show
	// per-person stats (which apps, how many messages, last seen) and a
	// short recent-messages preview without re-scanning the parsed-content
	// store on every page load.
	if version < 42 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS principal_sightings (
				principal_id   TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
				app            TEXT NOT NULL,
				message_count  INTEGER NOT NULL DEFAULT 0,
				first_seen_at  INTEGER NOT NULL,
				last_seen_at   INTEGER NOT NULL,
				PRIMARY KEY (principal_id, app)
			);

			CREATE TABLE IF NOT EXISTS principal_recent_messages (
				principal_id  TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
				app           TEXT NOT NULL,
				seen_at       INTEGER NOT NULL,
				preview       TEXT NOT NULL DEFAULT '',
				PRIMARY KEY (principal_id, seen_at)
			);

			CREATE INDEX IF NOT EXISTS idx_principal_sightings_last_seen
				ON principal_sightings(last_seen_at DESC);
			CREATE INDEX IF NOT EXISTS idx_principal_recent_messages_principal
				ON principal_recent_messages(principal_id, seen_at DESC);
		`); err != nil {
			return fmt.Errorf("migration v42 principal sightings: %w", err)
		}
	}

	// v43: per-session sighting index. The Who you know page needs to drill
	// from a principal to "which activity sessions did Yansu see them
	// in?", and the per-(principal, app) counters from v42 don't carry
	// the session_id. principal_session_sightings tracks one row per
	// (principal_id, session_id) tuple updated on every sender resolution
	// — first_seen_at takes the MIN and last_seen_at the MAX across
	// repeated bumps inside the same session group.
	if version < 43 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS principal_session_sightings (
				principal_id  TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
				session_id    TEXT NOT NULL,
				first_seen_at INTEGER NOT NULL,
				last_seen_at  INTEGER NOT NULL,
				PRIMARY KEY (principal_id, session_id)
			);
			CREATE INDEX IF NOT EXISTS idx_principal_session_sightings_last_seen
				ON principal_session_sightings(principal_id, last_seen_at DESC);
		`); err != nil {
			return fmt.Errorf("migration v43 principal session sightings: %w", err)
		}
	}

	// v44: voice fingerprint embeddings per principal. Each row stores
	// one enrollment sample's float32 embedding (encoded as little-endian
	// raw bytes — dim is recoverable as len(blob)/4) plus the source
	// audio metadata. Multiple samples per principal are normal — the
	// verification path averages or compares-against-best to be robust
	// against single-clip noise.
	if version < 44 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS principal_voice_embeddings (
				id             INTEGER PRIMARY KEY AUTOINCREMENT,
				principal_id   TEXT    NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
				embedding      BLOB    NOT NULL,
				dim            INTEGER NOT NULL,
				sample_rate    INTEGER NOT NULL,
				audio_seconds  REAL    NOT NULL,
				label          TEXT    NOT NULL DEFAULT '',
				captured_at    INTEGER NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_principal_voice_embeddings_principal
				ON principal_voice_embeddings(principal_id, captured_at DESC);
		`); err != nil {
			return fmt.Errorf("migration v44 voice embeddings: %w", err)
		}
	}

	// v45: persist automation run context (output + owning job id) on the
	// stream item so a "continue in chat" follow-up keeps its collapsible
	// "Run output" quote card and clickable deliverable chips after a session
	// reload, instead of degrading to a bare follow-up bubble. Stored as a
	// JSON blob ({output, jobId}) to keep the schema flat, mirroring `images`.
	if version < 45 {
		if _, err := s.db.Exec(`ALTER TABLE stream_items ADD COLUMN automation_context TEXT NOT NULL DEFAULT ''`); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return fmt.Errorf("migration v45 stream_items.automation_context: %w", err)
			}
		}
	}

	// v46: tag chat sessions that belong to a single meeting (transcripts +
	// handoffs the meeting produced) with a shared meeting_id so the sidebar
	// Recent section can collapse them under one parent. Empty for sessions
	// not produced by a meeting (regular chats, pre-migration rows).
	if version < 46 {
		if _, err := s.db.Exec(`ALTER TABLE sessions ADD COLUMN meeting_id TEXT NOT NULL DEFAULT ''`); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return fmt.Errorf("migration v46 sessions.meeting_id: %w", err)
			}
		}
		if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_meeting_id ON sessions(meeting_id) WHERE meeting_id != ''`); err != nil {
			return fmt.Errorf("migration v46 sessions.meeting_id index: %w", err)
		}
	}

	// v47: persist non-image file attachment metadata (name, type, size,
	// category) on stream_items so file chips survive session reload.
	if version < 47 {
		if _, err := s.db.Exec(`ALTER TABLE stream_items ADD COLUMN files TEXT NOT NULL DEFAULT ''`); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return fmt.Errorf("migration v47 stream_items.files: %w", err)
			}
		}
	}

	// v48: chat_deliverables — persist the files an interactive chat agent
	// declared via its <deliverables> block so they can surface in the unified
	// Deliverables hub alongside automation/handoff/extractor outputs. Chat
	// deliverables previously lived only in frontend memory (parsed from the
	// reply text) with no backend record, so they couldn't appear in any list
	// view. Keyed on a content hash (session + sorted paths) so re-recording the
	// same set upserts in place rather than duplicating. work_dir/session_temp_dir
	// are captured at record time so the file stays openable (path validation)
	// after the in-memory session temp-dir cache is lost on restart.
	if version < 48 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS chat_deliverables (
				id               TEXT PRIMARY KEY,
				session_id       TEXT NOT NULL,
				title            TEXT NOT NULL DEFAULT '',
				summary          TEXT NOT NULL DEFAULT '',
				files            TEXT NOT NULL DEFAULT '[]',
				work_dir         TEXT NOT NULL DEFAULT '',
				session_temp_dir TEXT NOT NULL DEFAULT '',
				created_at       INTEGER NOT NULL,
				updated_at       INTEGER NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_chat_deliverables_updated_at
				ON chat_deliverables(updated_at DESC);
		`); err != nil {
			return fmt.Errorf("migration v48 chat_deliverables: %w", err)
		}
	}

	// v49: principal_research — the AI-generated research dossier for a
	// person on the Who you know page (Contact / Who They Are / Your Relationship,
	// with [n] citations). Keyed 1:1 on principals.id and cascade-deleted
	// with the principal. The dossier is regenerated in place (not versioned);
	// sources holds a JSON array of {n,title,url} backing the inline markers.
	if version < 49 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS principal_research (
				principal_id  TEXT PRIMARY KEY REFERENCES principals(id) ON DELETE CASCADE,
				status        TEXT    NOT NULL DEFAULT 'idle',
				summary       TEXT    NOT NULL DEFAULT '',
				markdown      TEXT    NOT NULL DEFAULT '',
				sources       TEXT    NOT NULL DEFAULT '[]',
				model         TEXT    NOT NULL DEFAULT '',
				error         TEXT    NOT NULL DEFAULT '',
				researched_at INTEGER NOT NULL DEFAULT 0,
				created_at    INTEGER NOT NULL,
				updated_at    INTEGER NOT NULL
			);
		`); err != nil {
			return fmt.Errorf("migration v49 principal_research: %w", err)
		}
	}

	// v50: the Engagement service. Moments are candidate reasons to reach
	// the user, receipts are what happened to each delivery, and prefs are
	// the per-kind settings. An open moment (pending or scheduled) is unique
	// per dedupe key; sent and suppressed rows keep the history.
	if version < 50 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS engagement_moments (
				id          TEXT PRIMARY KEY,
				kind        TEXT    NOT NULL,
				subject_id  TEXT    NOT NULL DEFAULT '',
				dedupe_key  TEXT    NOT NULL,
				score       REAL    NOT NULL DEFAULT 0,
				state       TEXT    NOT NULL DEFAULT 'pending',
				channel     TEXT    NOT NULL DEFAULT '',
				level       TEXT    NOT NULL DEFAULT '',
				reason      TEXT    NOT NULL DEFAULT '',
				params      TEXT    NOT NULL DEFAULT '{}',
				created_at  INTEGER NOT NULL,
				due_at      INTEGER NOT NULL DEFAULT 0,
				hold_until  INTEGER NOT NULL DEFAULT 0,
				sent_at     INTEGER NOT NULL DEFAULT 0,
				expires_at  INTEGER NOT NULL DEFAULT 0,
				updated_at  INTEGER NOT NULL
			);
			CREATE UNIQUE INDEX IF NOT EXISTS uq_engagement_moments_open
				ON engagement_moments(dedupe_key) WHERE state IN ('pending', 'scheduled');
			CREATE INDEX IF NOT EXISTS idx_engagement_moments_state
				ON engagement_moments(state, hold_until);
			CREATE INDEX IF NOT EXISTS idx_engagement_moments_kind_sent
				ON engagement_moments(kind, sent_at);

			CREATE TABLE IF NOT EXISTS engagement_receipts (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				moment_id  TEXT    NOT NULL REFERENCES engagement_moments(id) ON DELETE CASCADE,
				channel    TEXT    NOT NULL DEFAULT '',
				state      TEXT    NOT NULL,
				action     TEXT    NOT NULL DEFAULT '',
				at         INTEGER NOT NULL,
				UNIQUE (moment_id, channel, state, action)
			);
			CREATE INDEX IF NOT EXISTS idx_engagement_receipts_moment
				ON engagement_receipts(moment_id);

			CREATE TABLE IF NOT EXISTS engagement_prefs (
				kind         TEXT PRIMARY KEY,
				weight       REAL    NOT NULL DEFAULT -1,
				enabled      INTEGER NOT NULL DEFAULT -1,
				muted_until  INTEGER NOT NULL DEFAULT 0,
				updated_at   INTEGER NOT NULL
			);
		`); err != nil {
			return fmt.Errorf("migration v50 engagement: %w", err)
		}
	}

	// v51: which account served each model call, reported by the gateway's
	// route headers. Feeds the model accounts settings and debugging.
	if version < 51 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS model_calls (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				at          INTEGER NOT NULL,
				session_id  TEXT    NOT NULL DEFAULT '',
				run_id      TEXT    NOT NULL DEFAULT '',
				requested   TEXT    NOT NULL DEFAULT '',
				provider    TEXT    NOT NULL DEFAULT '',
				model       TEXT    NOT NULL DEFAULT '',
				priority    TEXT    NOT NULL DEFAULT '',
				source      TEXT    NOT NULL DEFAULT ''
			);
			CREATE INDEX IF NOT EXISTS idx_model_calls_at ON model_calls(at);
		`); err != nil {
			return fmt.Errorf("migration v51 model_calls: %w", err)
		}
	}

	// v52: a moment's stakes are stored apart from its derived score, so a
	// rescheduled moment is scored from what the producer said, not from its
	// own previous score.
	if version < 52 {
		if _, err := s.db.Exec(`ALTER TABLE engagement_moments ADD COLUMN stakes REAL NOT NULL DEFAULT 0`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migration v52 engagement stakes: %w", err)
		}
	}

	// v53: receipts for phone requests that changed state on this Mac. A retry
	// with the same idempotency key replays the stored answer (PROTOCOL.md 15.2).
	if version < 53 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS mobile_receipts (
				request_key TEXT    PRIMARY KEY,
				device_id   TEXT    NOT NULL,
				route       TEXT    NOT NULL,
				status      INTEGER NOT NULL,
				body        TEXT    NOT NULL,
				at          INTEGER NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_mobile_receipts_at ON mobile_receipts(at);
		`); err != nil {
			return fmt.Errorf("migration v53 mobile receipts: %w", err)
		}
	}

	// v54: the chat model. kind marks the one main chat ("main") and the side
	// chats split off from it ("side"); every other session keeps "" and its
	// legacy inference. parent_id links a side chat to the main chat.
	if version < 54 {
		for _, stmt := range []string{
			`ALTER TABLE sessions ADD COLUMN kind TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE sessions ADD COLUMN parent_id TEXT NOT NULL DEFAULT ''`,
		} {
			if _, err := s.db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migration v54 sessions kind/parent_id: %w", err)
			}
		}
		if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_kind ON sessions(kind) WHERE kind != ''`); err != nil {
			return fmt.Errorf("migration v54 sessions.kind index: %w", err)
		}
		if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_parent_id ON sessions(parent_id) WHERE parent_id != ''`); err != nil {
			return fmt.Errorf("migration v54 sessions.parent_id index: %w", err)
		}
	}

	// v55: the day ahead. calendar_events holds every occurrence read from
	// EventKit or a web calendar in the sync window (vanished rows are kept
	// with deleted_at set, since a cancellation is information too),
	// calendar_event_attendees the people on each one resolved to principals,
	// calendar_sources which calendars the user actually uses and how that
	// was noticed, and meeting_followups the decisions and follow-ups a model
	// pass found in a meeting transcript.
	if version < 55 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS calendar_events (
				id              TEXT PRIMARY KEY,
				uid             TEXT    NOT NULL,
				local_id        TEXT    NOT NULL DEFAULT '',
				calendar_id     TEXT    NOT NULL DEFAULT '',
				calendar_title  TEXT    NOT NULL DEFAULT '',
				source          TEXT    NOT NULL,
				provider        TEXT    NOT NULL DEFAULT '',
				title           TEXT    NOT NULL DEFAULT '',
				start_at        INTEGER NOT NULL,
				end_at          INTEGER NOT NULL,
				all_day         INTEGER NOT NULL DEFAULT 0,
				time_zone       TEXT    NOT NULL DEFAULT '',
				location        TEXT    NOT NULL DEFAULT '',
				lat             REAL    NOT NULL DEFAULT 0,
				lon             REAL    NOT NULL DEFAULT 0,
				has_coordinate  INTEGER NOT NULL DEFAULT 0,
				conference_url  TEXT    NOT NULL DEFAULT '',
				conference_host TEXT    NOT NULL DEFAULT '',
				url             TEXT    NOT NULL DEFAULT '',
				availability    TEXT    NOT NULL DEFAULT '',
				status          TEXT    NOT NULL DEFAULT '',
				my_status       TEXT    NOT NULL DEFAULT '',
				organizer_email TEXT    NOT NULL DEFAULT '',
				organizer_name  TEXT    NOT NULL DEFAULT '',
				recurring       INTEGER NOT NULL DEFAULT 0,
				attendee_count  INTEGER NOT NULL DEFAULT 0,
				session_id      TEXT    NOT NULL DEFAULT '',
				first_seen_at   INTEGER NOT NULL,
				updated_at      INTEGER NOT NULL,
				deleted_at      INTEGER NOT NULL DEFAULT 0
			);
			CREATE INDEX IF NOT EXISTS idx_calendar_events_start
				ON calendar_events(start_at) WHERE deleted_at = 0;
			CREATE INDEX IF NOT EXISTS idx_calendar_events_session
				ON calendar_events(session_id) WHERE session_id != '';

			CREATE TABLE IF NOT EXISTS calendar_event_attendees (
				event_id     TEXT    NOT NULL REFERENCES calendar_events(id) ON DELETE CASCADE,
				email        TEXT    NOT NULL DEFAULT '',
				name         TEXT    NOT NULL DEFAULT '',
				role         TEXT    NOT NULL DEFAULT '',
				status       TEXT    NOT NULL DEFAULT '',
				is_me        INTEGER NOT NULL DEFAULT 0,
				principal_id TEXT    NOT NULL DEFAULT '',
				PRIMARY KEY (event_id, email, name)
			);
			CREATE INDEX IF NOT EXISTS idx_calendar_event_attendees_principal
				ON calendar_event_attendees(principal_id) WHERE principal_id != '';

			CREATE TABLE IF NOT EXISTS calendar_sources (
				id            TEXT PRIMARY KEY,
				kind          TEXT    NOT NULL,
				provider      TEXT    NOT NULL DEFAULT '',
				title         TEXT    NOT NULL DEFAULT '',
				detected_by   TEXT    NOT NULL DEFAULT '',
				calendars     INTEGER NOT NULL DEFAULT 0,
				first_seen_at INTEGER NOT NULL,
				last_seen_at  INTEGER NOT NULL,
				last_read_at  INTEGER NOT NULL DEFAULT 0
			);

			CREATE TABLE IF NOT EXISTS meeting_followups (
				session_id     TEXT PRIMARY KEY,
				event_id       TEXT    NOT NULL DEFAULT '',
				title          TEXT    NOT NULL DEFAULT '',
				decisions_json TEXT    NOT NULL DEFAULT '[]',
				followups_json TEXT    NOT NULL DEFAULT '[]',
				created_at     INTEGER NOT NULL
			);
		`); err != nil {
			return fmt.Errorf("migration v55 calendars: %w", err)
		}
	}

	// v56: reminders read through EventKit. One row per reminder, keyed by
	// its external identifier; completed_at and deleted_at record what
	// happened to it, since a ticked-off or deleted reminder resolves the
	// commitment made from it.
	if version < 56 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS reminders (
				uid           TEXT PRIMARY KEY,
				title         TEXT    NOT NULL DEFAULT '',
				due_at        INTEGER NOT NULL DEFAULT 0,
				due_all_day   INTEGER NOT NULL DEFAULT 0,
				list          TEXT    NOT NULL DEFAULT '',
				priority      INTEGER NOT NULL DEFAULT 0,
				completed_at  INTEGER NOT NULL DEFAULT 0,
				first_seen_at INTEGER NOT NULL,
				updated_at    INTEGER NOT NULL,
				deleted_at    INTEGER NOT NULL DEFAULT 0
			);
			CREATE INDEX IF NOT EXISTS idx_reminders_open_due
				ON reminders(due_at) WHERE deleted_at = 0 AND completed_at = 0;
		`); err != nil {
			return fmt.Errorf("migration v56 reminders: %w", err)
		}
	}

	// v57: mail headers read from Mail.app over Apple Events. One row per
	// message of the inboxes and the sent mailbox in the last week, keyed by
	// a hash of the mailbox and the message-id: sender, recipients, scrubbed
	// subject, date and read, flagged and replied state. Never a body.
	if version < 57 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS mail_messages (
				id            TEXT PRIMARY KEY,
				mailbox       TEXT    NOT NULL,
				sender_email  TEXT    NOT NULL DEFAULT '',
				sender_name   TEXT    NOT NULL DEFAULT '',
				to_addresses  TEXT    NOT NULL DEFAULT '',
				cc_addresses  TEXT    NOT NULL DEFAULT '',
				to_me         INTEGER NOT NULL DEFAULT 0,
				subject       TEXT    NOT NULL DEFAULT '',
				thread_key    TEXT    NOT NULL DEFAULT '',
				at            INTEGER NOT NULL,
				read          INTEGER NOT NULL DEFAULT 0,
				flagged       INTEGER NOT NULL DEFAULT 0,
				replied       INTEGER NOT NULL DEFAULT 0,
				principal_id  TEXT    NOT NULL DEFAULT '',
				first_seen_at INTEGER NOT NULL,
				updated_at    INTEGER NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_mail_messages_at ON mail_messages(at);
			CREATE INDEX IF NOT EXISTS idx_mail_messages_thread ON mail_messages(thread_key);
		`); err != nil {
			return fmt.Errorf("migration v57 mail messages: %w", err)
		}
	}

	if err := s.ensureProactiveStructures(); err != nil {
		return err
	}

	// v60: retain the assistant route for scheduled and playbook run history.
	// v58/v59 are reserved for voice; tolerate route objects from earlier builds.
	if version < 60 {
		for _, column := range []string{"candidate_backends", "backend", "fallback_from", "backend_reason", "model"} {
			if _, err := s.db.Exec("ALTER TABLE cron_runs ADD COLUMN " + column + " TEXT NOT NULL DEFAULT ''"); err != nil && !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migration v60 cron run route %s: %w", column, err)
			}
		}
		if _, err := s.db.Exec("ALTER TABLE cron_runs ADD COLUMN actions_started INTEGER NOT NULL DEFAULT 0"); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migration v60 cron run action count: %w", err)
		}
		if _, err := s.db.Exec(`
            CREATE TABLE IF NOT EXISTS agent_run_routes (
                run_id TEXT PRIMARY KEY,
                session_id TEXT NOT NULL DEFAULT '',
                backend TEXT NOT NULL DEFAULT '',
                candidate_backends TEXT NOT NULL DEFAULT '[]',
                fallback_from TEXT NOT NULL DEFAULT '[]',
                backend_reason TEXT NOT NULL DEFAULT '',
                model TEXT NOT NULL DEFAULT '',
                status TEXT NOT NULL DEFAULT 'running',
                actions_started INTEGER NOT NULL DEFAULT 0,
                updated_at INTEGER NOT NULL
            );
            CREATE INDEX IF NOT EXISTS idx_agent_run_routes_updated ON agent_run_routes(updated_at DESC);
        `); err != nil {
			return fmt.Errorf("migration v60 agent run routes: %w", err)
		}
	}

	// v61: retain imported-contact provenance separately from creation history.
	if version < 61 {
		if err := s.migratePrincipalImportProvenance(); err != nil {
			return fmt.Errorf("migration v61 principal import provenance: %w", err)
		}
	}

	// v62: local contact cache, handle provenance and reversible link receipts.
	if version < 62 {
		if err := s.migrateContactLinks(); err != nil {
			return fmt.Errorf("migration v62 contact links: %w", err)
		}
	}

	// v64: identifier-only context-use receipts shared by memory and knowledge.
	if version < 64 {
		if err := s.migrateContextUses(); err != nil {
			return fmt.Errorf("migration v64 context uses: %w", err)
		}
	}

	// v65: reversible People merges (#143).
	if version < 65 {
		if err := s.migratePeopleMerges(); err != nil {
			return fmt.Errorf("migration v65 people merges: %w", err)
		}
	}

	// v66: prepared requests beside a main-chat answer.
	if version < 66 {
		if err := s.migrateStreamItemChips(); err != nil {
			return fmt.Errorf("migration v66 answer chips: %w", err)
		}
	}

	// v67: durable external-send attempts keyed by the exact stored draft.
	if version < 67 {
		if err := s.migrateOutboundSends(); err != nil {
			return fmt.Errorf("migration v67 outbound sends: %w", err)
		}
	}

	if version < 68 {
		if err := s.migrateOpenAnswers(); err != nil {
			return fmt.Errorf("migration v68 open answers: %w", err)
		}
	}

	if version < 69 {
		if err := s.migrateOpenAnswerMutedKinds(); err != nil {
			return fmt.Errorf("migration v69 answer preferences: %w", err)
		}
	}

	if version < 70 {
		if err := s.migrateOpenAnswerChecks(); err != nil {
			return fmt.Errorf("migration v70 answer checks: %w", err)
		}
	}

	if version < 71 {
		if err := s.migrateOpenAnswerAskState(); err != nil {
			return fmt.Errorf("migration v71 answer asks: %w", err)
		}
	}

	if version < 72 {
		if err := s.migrateOpenAnswerQuietHolds(); err != nil {
			return fmt.Errorf("migration v72 quiet-hour answer holds: %w", err)
		}
	}

	if version < 73 {
		if err := s.migrateOpenAnswerLearned(); err != nil {
			return fmt.Errorf("migration v73 learned answers: %w", err)
		}
	}
	if version < 74 {
		if err := s.migrateSenderTimestampNames(); err != nil {
			return fmt.Errorf("migration v74 sender timestamp names: %w", err)
		}
	}

	// Advance the version only after all required migrations succeed.
	if version < schemaVersion {
		if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return fmt.Errorf("set schema version: %w", err)
		}
	}

	return nil
}

// ensureProactiveStructures repairs additive chat access structures regardless
// of the version stamp. A sibling build may have stamped a later migration
// without implementing v58 or v59.
func (s *Store) ensureProactiveStructures() error {
	// v58: indexed root moment lookup, including malformed legacy payloads.
	if _, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_stream_items_session_tool_order
			ON stream_items(session_local_id, tool_name, sort_order);
		CREATE INDEX IF NOT EXISTS idx_stream_items_proactive_moment
			ON stream_items(session_local_id, json_extract(CASE WHEN json_valid(tool_input) THEN tool_input ELSE '{}' END, '$.momentId'))
			WHERE tool_name = 'main_chat_proactive';
	`); err != nil {
		return fmt.Errorf("migration v58 proactive stream lookup: %w", err)
	}
	// v59: digest-child and status-entry identities maintained by triggers.
	if err := s.migrateMainChatItemIdentities(); err != nil {
		return fmt.Errorf("migration v59 main chat item identities: %w", err)
	}
	// v63: repair missing or older RC topic/proof triggers on stamped stores.
	if err := s.migrateStreamItemTopics(); err != nil {
		return fmt.Errorf("migration v63 topic structures: %w", err)
	}
	if err := s.ensureMainChatViewIndexes(); err != nil {
		return fmt.Errorf("main chat view indexes: %w", err)
	}
	if err := s.ensureStreamItemVersions(); err != nil {
		return err
	}
	if err := s.ensureVoiceSourceVersion(); err != nil {
		return err
	}
	return nil
}

// migrateLegacyStandaloneChat rewrites a stray path="" project (left behind
// by older builds of activeProjectAndWorkspace step 3) to the canonical
// __standalone_chat__ id so callers passing that id can materialize the
// sentinel without colliding on projects.path.
//
// Child rows (sessions / workspaces) are always migrated first via
// PRAGMA defer_foreign_keys to preserve user history. Whether the
// projects table itself is renamed or merged depends on whether a sentinel
// row already exists:
//   - sentinel absent: rename the legacy row's id to the sentinel.
//   - sentinel present: drop the legacy row and reclaim path="" for the
//     sentinel. (Shouldn't occur given current writers, but this stays
//     correct against any future bug that might create such a state.)
func migrateLegacyStandaloneChat(db *sql.DB) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`PRAGMA defer_foreign_keys = ON`); err != nil {
		return fmt.Errorf("defer_foreign_keys: %w", err)
	}

	const sentinelID = "__standalone_chat__"

	var legacyID string
	err = tx.QueryRow(`
		SELECT id FROM projects WHERE path = '' AND id != ? LIMIT 1
	`, sentinelID).Scan(&legacyID)
	if err == sql.ErrNoRows {
		return tx.Commit() // nothing to migrate
	}
	if err != nil {
		return fmt.Errorf("locate legacy row: %w", err)
	}

	var sentinelExists int
	err = tx.QueryRow(`SELECT 1 FROM projects WHERE id = ?`, sentinelID).Scan(&sentinelExists)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("check sentinel: %w", err)
	}

	// Reparent child rows first so no history is lost regardless of which
	// branch handles the projects table below.
	for _, table := range []string{"sessions", "workspaces"} {
		if _, err := tx.Exec(
			fmt.Sprintf(`UPDATE %s SET project_id = ? WHERE project_id = ?`, table),
			sentinelID, legacyID,
		); err != nil {
			return fmt.Errorf("rewrite %s: %w", table, err)
		}
	}

	if sentinelExists == 1 {
		// Sentinel already occupies the projects table under some non-empty
		// path (path="" is UNIQUE and currently owned by legacyID). Drop the
		// legacy row to free the slot, then claim path="" for the sentinel.
		if _, err := tx.Exec(`DELETE FROM projects WHERE id = ?`, legacyID); err != nil {
			return fmt.Errorf("delete legacy row: %w", err)
		}
		if _, err := tx.Exec(`UPDATE projects SET path = '' WHERE id = ?`, sentinelID); err != nil {
			return fmt.Errorf("reclaim sentinel path: %w", err)
		}
		log.Printf("[storage] migration v40: merged legacy path=\"\" project %s into existing sentinel", legacyID)
		return tx.Commit()
	}

	// No sentinel yet — promote the legacy row in place.
	if _, err := tx.Exec(`UPDATE projects SET id = ? WHERE id = ?`, sentinelID, legacyID); err != nil {
		return fmt.Errorf("rewrite projects: %w", err)
	}
	log.Printf("[storage] migration v40: renamed legacy path=\"\" project %s -> %s", legacyID, sentinelID)
	return tx.Commit()
}
