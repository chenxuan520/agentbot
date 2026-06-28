package sqlite

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/chenxuan520/agentbot/internal/control"
	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/progress"
	"github.com/chenxuan520/agentbot/internal/scheduler"
	appstore "github.com/chenxuan520/agentbot/internal/store"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		_ = db.Close()
		return nil, err
	}

	store := &Store{db: db}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) init() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS conversation_workspaces (
			provider TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			workspace_path TEXT NOT NULL,
			template TEXT NOT NULL,
			agent_backend TEXT NOT NULL,
			active_session_id TEXT NOT NULL DEFAULT '',
			btw_session_id TEXT NOT NULL DEFAULT '',
			last_message_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (provider, conversation_id)
		)
	`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`ALTER TABLE conversation_workspaces ADD COLUMN btw_session_id TEXT NOT NULL DEFAULT ''`)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return err
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS session_tokens (
			provider TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			token_ciphertext TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (provider, conversation_id)
		)
	`)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS conversation_btw_sessions (
			provider TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			sender_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (provider, conversation_id, sender_id)
		)
	`)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS conversation_topic_sessions (
			provider TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			topic_key TEXT NOT NULL,
			session_id TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (provider, conversation_id, topic_key)
		)
	`)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS scheduled_jobs (
			id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			route TEXT NOT NULL,
			payload TEXT NOT NULL,
			run_at INTEGER NOT NULL,
			status TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`)
	if err != nil {
		return err
	}
	// attempts tracks how many times a job has been reclaimed from a crashed
	// (running) state, so a poison job can be dead-lettered instead of looping.
	_, err = s.db.Exec(`ALTER TABLE scheduled_jobs ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return err
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS control_rules (
			id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			scope TEXT NOT NULL,
			match_key TEXT NOT NULL,
			reason TEXT NOT NULL,
			until_at INTEGER NOT NULL,
			status TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS message_progress (
			provider TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			last_message_id TEXT NOT NULL,
			last_message_time_ms INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (provider, conversation_id)
		)
	`)
	return err
}

func (s *Store) Get(ref conversation.Ref) (*appstore.WorkspaceRecord, error) {
	row := s.db.QueryRow(`
		SELECT provider, conversation_id, workspace_path, template, agent_backend, active_session_id, btw_session_id, last_message_at, created_at, updated_at
		FROM conversation_workspaces
		WHERE provider = ? AND conversation_id = ?
	`, ref.Provider, ref.ConversationID)

	var record appstore.WorkspaceRecord
	var lastMessageAt int64
	var createdAt int64
	var updatedAt int64

	err := row.Scan(
		&record.Provider,
		&record.ConversationID,
		&record.WorkspacePath,
		&record.Template,
		&record.AgentBackend,
		&record.ActiveSessionID,
		&record.BTWSessionID,
		&lastMessageAt,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	record.LastMessageAt = fromUnix(lastMessageAt)
	record.CreatedAt = fromUnix(createdAt)
	record.UpdatedAt = fromUnix(updatedAt)
	return &record, nil
}

func (s *Store) Upsert(record appstore.WorkspaceRecord) error {
	_, err := s.db.Exec(`
		INSERT INTO conversation_workspaces (
			provider,
			conversation_id,
			workspace_path,
			template,
			agent_backend,
			active_session_id,
			btw_session_id,
			last_message_at,
			created_at,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, conversation_id) DO UPDATE SET
			workspace_path = excluded.workspace_path,
			template = excluded.template,
			agent_backend = excluded.agent_backend,
			active_session_id = excluded.active_session_id,
			btw_session_id = excluded.btw_session_id,
			last_message_at = excluded.last_message_at,
			updated_at = excluded.updated_at
	`,
		record.Provider,
		record.ConversationID,
		record.WorkspacePath,
		record.Template,
		record.AgentBackend,
		record.ActiveSessionID,
		record.BTWSessionID,
		toUnix(record.LastMessageAt),
		toUnix(record.CreatedAt),
		toUnix(record.UpdatedAt),
	)
	return err
}

func (s *Store) Delete(ref conversation.Ref) error {
	_, err := s.db.Exec(`DELETE FROM conversation_workspaces WHERE provider = ? AND conversation_id = ?`, ref.Provider, ref.ConversationID)
	return err
}

func (s *Store) GetBTWSession(ref conversation.Ref, senderID string) (string, error) {
	row := s.db.QueryRow(`
		SELECT session_id
		FROM conversation_btw_sessions
		WHERE provider = ? AND conversation_id = ? AND sender_id = ?
	`, ref.Provider, ref.ConversationID, senderID)
	var sessionID string
	err := row.Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return sessionID, nil
}

func (s *Store) HasBTWSessions(ref conversation.Ref) (bool, error) {
	row := s.db.QueryRow(`
		SELECT 1
		FROM conversation_btw_sessions
		WHERE provider = ? AND conversation_id = ?
		LIMIT 1
	`, ref.Provider, ref.ConversationID)
	var exists int
	err := row.Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) UpsertBTWSession(ref conversation.Ref, senderID, sessionID string, updatedAt time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO conversation_btw_sessions (
			provider,
			conversation_id,
			sender_id,
			session_id,
			updated_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(provider, conversation_id, sender_id) DO UPDATE SET
			session_id = excluded.session_id,
			updated_at = excluded.updated_at
	`, ref.Provider, ref.ConversationID, senderID, sessionID, toUnix(updatedAt))
	return err
}

func (s *Store) DeleteBTWSession(ref conversation.Ref, senderID string) error {
	_, err := s.db.Exec(`
		DELETE FROM conversation_btw_sessions
		WHERE provider = ? AND conversation_id = ? AND sender_id = ?
	`, ref.Provider, ref.ConversationID, senderID)
	return err
}

func (s *Store) DeleteBTWSessions(ref conversation.Ref) error {
	_, err := s.db.Exec(`
		DELETE FROM conversation_btw_sessions
		WHERE provider = ? AND conversation_id = ?
	`, ref.Provider, ref.ConversationID)
	return err
}

func (s *Store) GetTopicSession(ref conversation.Ref, topicKey string) (string, error) {
	row := s.db.QueryRow(`
		SELECT session_id
		FROM conversation_topic_sessions
		WHERE provider = ? AND conversation_id = ? AND topic_key = ?
	`, ref.Provider, ref.ConversationID, topicKey)
	var sessionID string
	err := row.Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return sessionID, nil
}

func (s *Store) HasTopicSessions(ref conversation.Ref) (bool, error) {
	row := s.db.QueryRow(`
		SELECT 1
		FROM conversation_topic_sessions
		WHERE provider = ? AND conversation_id = ?
		LIMIT 1
	`, ref.Provider, ref.ConversationID)
	var exists int
	err := row.Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) ListTopicSessions(ref conversation.Ref, limit int) ([]appstore.TopicSessionRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`
		SELECT topic_key, session_id, updated_at
		FROM conversation_topic_sessions
		WHERE provider = ? AND conversation_id = ?
		ORDER BY updated_at DESC, topic_key ASC
		LIMIT ?
	`, ref.Provider, ref.ConversationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []appstore.TopicSessionRecord{}
	for rows.Next() {
		var item appstore.TopicSessionRecord
		var updatedAt int64
		if err := rows.Scan(&item.TopicKey, &item.SessionID, &updatedAt); err != nil {
			return nil, err
		}
		item.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) UpsertTopicSession(ref conversation.Ref, topicKey, sessionID string, updatedAt time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO conversation_topic_sessions (
			provider,
			conversation_id,
			topic_key,
			session_id,
			updated_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(provider, conversation_id, topic_key) DO UPDATE SET
			session_id = excluded.session_id,
			updated_at = excluded.updated_at
	`, ref.Provider, ref.ConversationID, topicKey, sessionID, toUnix(updatedAt))
	return err
}

func (s *Store) DeleteTopicSession(ref conversation.Ref, topicKey string) error {
	_, err := s.db.Exec(`
		DELETE FROM conversation_topic_sessions
		WHERE provider = ? AND conversation_id = ? AND topic_key = ?
	`, ref.Provider, ref.ConversationID, topicKey)
	return err
}

func (s *Store) DeleteTopicSessions(ref conversation.Ref) error {
	_, err := s.db.Exec(`
		DELETE FROM conversation_topic_sessions
		WHERE provider = ? AND conversation_id = ?
	`, ref.Provider, ref.ConversationID)
	return err
}

func (s *Store) DeleteConversationState(ref conversation.Ref) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, query := range []string{
		`DELETE FROM conversation_btw_sessions WHERE provider = ? AND conversation_id = ?`,
		`DELETE FROM conversation_topic_sessions WHERE provider = ? AND conversation_id = ?`,
		`DELETE FROM scheduled_jobs WHERE provider = ? AND conversation_id = ?`,
		`DELETE FROM control_rules WHERE provider = ? AND conversation_id = ?`,
		`DELETE FROM message_progress WHERE provider = ? AND conversation_id = ?`,
		`DELETE FROM session_tokens WHERE provider = ? AND conversation_id = ?`,
		`DELETE FROM conversation_workspaces WHERE provider = ? AND conversation_id = ?`,
	} {
		if _, execErr := tx.Exec(query, ref.Provider, ref.ConversationID); execErr != nil {
			err = execErr
			return err
		}
	}
	err = tx.Commit()
	return err
}

func (s *Store) List() ([]appstore.WorkspaceRecord, error) {
	rows, err := s.db.Query(`
		SELECT provider, conversation_id, workspace_path, template, agent_backend, active_session_id, btw_session_id, last_message_at, created_at, updated_at
		FROM conversation_workspaces
		ORDER BY last_message_at DESC, updated_at DESC, provider ASC, conversation_id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []appstore.WorkspaceRecord
	for rows.Next() {
		var record appstore.WorkspaceRecord
		var lastMessageAt int64
		var createdAt int64
		var updatedAt int64
		if err := rows.Scan(
			&record.Provider,
			&record.ConversationID,
			&record.WorkspacePath,
			&record.Template,
			&record.AgentBackend,
			&record.ActiveSessionID,
			&record.BTWSessionID,
			&lastMessageAt,
			&createdAt,
			&updatedAt,
		); err != nil {
			return nil, err
		}
		record.LastMessageAt = fromUnix(lastMessageAt)
		record.CreatedAt = fromUnix(createdAt)
		record.UpdatedAt = fromUnix(updatedAt)
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) GetSessionToken(ref conversation.Ref) (*appstore.SessionTokenRecord, error) {
	row := s.db.QueryRow(`
		SELECT provider, conversation_id, token_hash, token_ciphertext, created_at, updated_at
		FROM session_tokens
		WHERE provider = ? AND conversation_id = ?
	`, ref.Provider, ref.ConversationID)

	var record appstore.SessionTokenRecord
	var createdAt int64
	var updatedAt int64
	err := row.Scan(
		&record.Provider,
		&record.ConversationID,
		&record.TokenHash,
		&record.TokenCiphertext,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record.CreatedAt = fromUnix(createdAt)
	record.UpdatedAt = fromUnix(updatedAt)
	return &record, nil
}

func (s *Store) GetSessionTokenByHash(tokenHash string) (*appstore.SessionTokenRecord, error) {
	row := s.db.QueryRow(`
		SELECT provider, conversation_id, token_hash, token_ciphertext, created_at, updated_at
		FROM session_tokens
		WHERE token_hash = ?
	`, tokenHash)

	var record appstore.SessionTokenRecord
	var createdAt int64
	var updatedAt int64
	err := row.Scan(
		&record.Provider,
		&record.ConversationID,
		&record.TokenHash,
		&record.TokenCiphertext,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record.CreatedAt = fromUnix(createdAt)
	record.UpdatedAt = fromUnix(updatedAt)
	return &record, nil
}

func (s *Store) UpsertSessionToken(record appstore.SessionTokenRecord) error {
	_, err := s.db.Exec(`
		INSERT INTO session_tokens (
			provider,
			conversation_id,
			token_hash,
			token_ciphertext,
			created_at,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, conversation_id) DO UPDATE SET
			token_hash = excluded.token_hash,
			token_ciphertext = excluded.token_ciphertext,
			updated_at = excluded.updated_at
	`,
		record.Provider,
		record.ConversationID,
		record.TokenHash,
		record.TokenCiphertext,
		toUnix(record.CreatedAt),
		toUnix(record.UpdatedAt),
	)
	return err
}

func (s *Store) DeleteSessionToken(ref conversation.Ref) error {
	_, err := s.db.Exec(`DELETE FROM session_tokens WHERE provider = ? AND conversation_id = ?`, ref.Provider, ref.ConversationID)
	return err
}

func (s *Store) CreateJob(job scheduler.Job) error {
	_, err := s.db.Exec(`
		INSERT INTO scheduled_jobs (
			id,
			provider,
			conversation_id,
			route,
			payload,
			run_at,
			status,
			created_at,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		job.ID,
		job.Provider,
		job.ConversationID,
		job.Route,
		job.Payload,
		toUnix(job.RunAt),
		job.Status,
		toUnix(job.CreatedAt),
		toUnix(job.UpdatedAt),
	)
	return err
}

func (s *Store) GetJob(id string) (scheduler.Job, error) {
	var job scheduler.Job
	var runAt int64
	var createdAt int64
	var updatedAt int64
	err := s.db.QueryRow(`
		SELECT id, provider, conversation_id, route, payload, run_at, status, created_at, updated_at
		FROM scheduled_jobs
		WHERE id = ?
	`, id).Scan(
		&job.ID,
		&job.Provider,
		&job.ConversationID,
		&job.Route,
		&job.Payload,
		&runAt,
		&job.Status,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return scheduler.Job{}, err
	}
	job.RunAt = fromUnix(runAt)
	job.CreatedAt = fromUnix(createdAt)
	job.UpdatedAt = fromUnix(updatedAt)
	return job, nil
}

func (s *Store) ListJobs(ref conversation.Ref, limit int) ([]scheduler.Job, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := s.db.Query(`
		SELECT id, provider, conversation_id, route, payload, run_at, status, created_at, updated_at
		FROM scheduled_jobs
		WHERE provider = ? AND conversation_id = ?
		ORDER BY run_at ASC, created_at ASC
		LIMIT ?
	`, ref.Provider, ref.ConversationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	jobs := []scheduler.Job{}
	for rows.Next() {
		var job scheduler.Job
		var runAt int64
		var createdAt int64
		var updatedAt int64

		if err := rows.Scan(
			&job.ID,
			&job.Provider,
			&job.ConversationID,
			&job.Route,
			&job.Payload,
			&runAt,
			&job.Status,
			&createdAt,
			&updatedAt,
		); err != nil {
			return nil, err
		}

		job.RunAt = fromUnix(runAt)
		job.CreatedAt = fromUnix(createdAt)
		job.UpdatedAt = fromUnix(updatedAt)
		jobs = append(jobs, job)
	}

	return jobs, rows.Err()
}

func (s *Store) ListActiveJobs(ref conversation.Ref, limit int) ([]scheduler.Job, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := s.db.Query(`
		SELECT id, provider, conversation_id, route, payload, run_at, status, created_at, updated_at
		FROM scheduled_jobs
		WHERE provider = ? AND conversation_id = ? AND status IN (?, ?)
		ORDER BY run_at ASC, created_at ASC
		LIMIT ?
	`, ref.Provider, ref.ConversationID, scheduler.StatusPending, scheduler.StatusRunning, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	jobs := []scheduler.Job{}
	for rows.Next() {
		var job scheduler.Job
		var runAt int64
		var createdAt int64
		var updatedAt int64

		if err := rows.Scan(
			&job.ID,
			&job.Provider,
			&job.ConversationID,
			&job.Route,
			&job.Payload,
			&runAt,
			&job.Status,
			&createdAt,
			&updatedAt,
		); err != nil {
			return nil, err
		}

		job.RunAt = fromUnix(runAt)
		job.CreatedAt = fromUnix(createdAt)
		job.UpdatedAt = fromUnix(updatedAt)
		jobs = append(jobs, job)
	}

	return jobs, rows.Err()
}

func (s *Store) ListDueJobs(now time.Time, limit int) ([]scheduler.Job, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.Query(`
		SELECT id, provider, conversation_id, route, payload, run_at, status, created_at, updated_at
		FROM scheduled_jobs
		WHERE status = ? AND run_at <= ?
		ORDER BY run_at ASC
		LIMIT ?
	`, scheduler.StatusPending, toUnix(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	jobs := []scheduler.Job{}
	for rows.Next() {
		var job scheduler.Job
		var runAt int64
		var createdAt int64
		var updatedAt int64

		if err := rows.Scan(
			&job.ID,
			&job.Provider,
			&job.ConversationID,
			&job.Route,
			&job.Payload,
			&runAt,
			&job.Status,
			&createdAt,
			&updatedAt,
		); err != nil {
			return nil, err
		}

		job.RunAt = fromUnix(runAt)
		job.CreatedAt = fromUnix(createdAt)
		job.UpdatedAt = fromUnix(updatedAt)
		jobs = append(jobs, job)
	}

	return jobs, rows.Err()
}

func (s *Store) UpdateJobStatus(id, status string, updatedAt time.Time) error {
	_, err := s.db.Exec(`
		UPDATE scheduled_jobs
		SET status = ?, updated_at = ?
		WHERE id = ?
	`, status, toUnix(updatedAt), id)
	return err
}

func (s *Store) UpdateJobPayload(id, payload string, updatedAt time.Time) error {
	_, err := s.db.Exec(`
		UPDATE scheduled_jobs
		SET payload = ?, updated_at = ?
		WHERE id = ?
	`, payload, toUnix(updatedAt), id)
	return err
}

func (s *Store) RescheduleJob(id string, runAt, updatedAt time.Time) error {
	_, err := s.db.Exec(`
		UPDATE scheduled_jobs
		SET run_at = ?, status = ?, updated_at = ?
		WHERE id = ?
	`, toUnix(runAt), scheduler.StatusPending, toUnix(updatedAt), id)
	return err
}

// ReclaimRunningJobs requeues jobs left in the running state by a previous crash.
// The daemon is single-instance, so any running row observed at startup is a
// leftover from a process that died mid-handler. Each reclaim bumps attempts;
// jobs that have exhausted maxAttempts are dead-lettered to failed instead of
// being retried forever (poison-job guard). It returns (reclaimed, deadLettered).
func (s *Store) ReclaimRunningJobs(now time.Time, maxAttempts int) (int, int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	updatedAt := toUnix(now)
	if _, err := tx.Exec(`
		UPDATE scheduled_jobs
		SET attempts = attempts + 1, updated_at = ?
		WHERE status = ?
	`, updatedAt, scheduler.StatusRunning); err != nil {
		return 0, 0, err
	}

	deadLettered := 0
	if maxAttempts > 0 {
		res, err := tx.Exec(`
			UPDATE scheduled_jobs
			SET status = ?, updated_at = ?
			WHERE status = ? AND attempts >= ?
		`, scheduler.StatusFailed, updatedAt, scheduler.StatusRunning, maxAttempts)
		if err != nil {
			return 0, 0, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return 0, 0, err
		}
		deadLettered = int(affected)
	}

	res, err := tx.Exec(`
		UPDATE scheduled_jobs
		SET status = ?, updated_at = ?
		WHERE status = ?
	`, scheduler.StatusPending, updatedAt, scheduler.StatusRunning)
	if err != nil {
		return 0, 0, err
	}
	reclaimed, err := res.RowsAffected()
	if err != nil {
		return 0, 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return int(reclaimed), deadLettered, nil
}

func (s *Store) CreateRule(rule control.Rule) error {
	_, err := s.db.Exec(`
		INSERT INTO control_rules (
			id,
			provider,
			conversation_id,
			kind,
			scope,
			match_key,
			reason,
			until_at,
			status,
			created_at,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		rule.ID,
		rule.Provider,
		rule.ConversationID,
		rule.Kind,
		rule.Scope,
		rule.MatchKey,
		rule.Reason,
		toUnix(rule.UntilAt),
		rule.Status,
		toUnix(rule.CreatedAt),
		toUnix(rule.UpdatedAt),
	)
	return err
}

func (s *Store) ListActiveRules(ref conversation.Ref, now time.Time) ([]control.Rule, error) {
	rows, err := s.db.Query(`
		SELECT id, provider, conversation_id, kind, scope, match_key, reason, until_at, status, created_at, updated_at
		FROM control_rules
		WHERE provider = ? AND conversation_id = ? AND status = ? AND until_at > ?
		ORDER BY until_at ASC
	`, ref.Provider, ref.ConversationID, control.StatusActive, toUnix(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []control.Rule
	for rows.Next() {
		var rule control.Rule
		var untilAt int64
		var createdAt int64
		var updatedAt int64
		if err := rows.Scan(
			&rule.ID,
			&rule.Provider,
			&rule.ConversationID,
			&rule.Kind,
			&rule.Scope,
			&rule.MatchKey,
			&rule.Reason,
			&untilAt,
			&rule.Status,
			&createdAt,
			&updatedAt,
		); err != nil {
			return nil, err
		}
		rule.UntilAt = fromUnix(untilAt)
		rule.CreatedAt = fromUnix(createdAt)
		rule.UpdatedAt = fromUnix(updatedAt)
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (s *Store) UpdateRuleStatus(id, status string, updatedAt time.Time) error {
	_, err := s.db.Exec(`
		UPDATE control_rules
		SET status = ?, updated_at = ?
		WHERE id = ?
	`, status, toUnix(updatedAt), id)
	return err
}

func (s *Store) GetProgress(ref conversation.Ref) (*progress.Record, error) {
	row := s.db.QueryRow(`
		SELECT provider, conversation_id, last_message_id, last_message_time_ms, updated_at
		FROM message_progress
		WHERE provider = ? AND conversation_id = ?
	`, ref.Provider, ref.ConversationID)

	var record progress.Record
	var updatedAt int64
	err := row.Scan(&record.Provider, &record.ConversationID, &record.LastMessageID, &record.LastMessageTimeMS, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record.UpdatedAt = fromUnix(updatedAt)
	return &record, nil
}

func (s *Store) UpsertProgress(record progress.Record) error {
	_, err := s.db.Exec(`
		INSERT INTO message_progress (
			provider,
			conversation_id,
			last_message_id,
			last_message_time_ms,
			updated_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(provider, conversation_id) DO UPDATE SET
			last_message_id = excluded.last_message_id,
			last_message_time_ms = excluded.last_message_time_ms,
			updated_at = excluded.updated_at
	`,
		record.Provider,
		record.ConversationID,
		record.LastMessageID,
		record.LastMessageTimeMS,
		toUnix(record.UpdatedAt),
	)
	return err
}

func toUnix(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}

func fromUnix(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(value, 0).UTC()
}
