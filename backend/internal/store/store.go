// Package store persists jobs, one JSON document per job, so the many small
// mutations coming from tus completion callbacks and FFmpeg workers are simple
// read-modify-write cycles guarded by an in-process lock. Open() backs onto a
// local SQLite file — zero external infra, used for local dev and every unit
// test in this repo. OpenMySQL() backs onto an existing MySQL/MariaDB server
// instead — used in production so job history survives container/volume loss
// and is centrally queryable. Both drivers use `?` placeholders and identical
// schema/SQL, so every method below is driver-agnostic; only Open/OpenMySQL differ.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"

	"github.com/rokelvisar/storyframe/backend/internal/models"
)

// ErrNotFound is returned when a job id is unknown.
var ErrNotFound = errors.New("not found")

// Store is a SQLite- or MySQL-backed job store (see Open / OpenMySQL).
type Store struct {
	db *sql.DB
	mu sync.Mutex // serialises read-modify-write on the JSON documents
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS jobs (
    id         TEXT PRIMARY KEY,
    status     TEXT NOT NULL,
    data       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
CREATE INDEX IF NOT EXISTS idx_jobs_created_at ON jobs(created_at);
`

// mysqlSchema mirrors sqliteSchema; timestamps stay plain strings (not a
// native DATETIME column) so CreateJob/GetJob/UpdateJob need no driver
// branching. IF NOT EXISTS on CREATE INDEX needs MariaDB 10.5.2+/MySQL
// 8.0.29+, which not every server runs, so a plain "table already has this
// index" error on repeat Open() calls (the migration only runs once per
// fresh database) is caught and ignored below instead.
const mysqlSchema = `
CREATE TABLE IF NOT EXISTS jobs (
    id         VARCHAR(64) PRIMARY KEY,
    status     VARCHAR(32) NOT NULL,
    data       LONGTEXT NOT NULL,
    created_at VARCHAR(40) NOT NULL,
    updated_at VARCHAR(40) NOT NULL
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
`

// Open opens (creating if needed) a local SQLite database at path — used for
// local dev and tests, zero external infra needed.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // modernc sqlite + WAL: keep writes serial and simple
	if _, err := db.Exec(sqliteSchema); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// OpenMySQL opens a job store against an existing MySQL/MariaDB server. dsn is
// a go-sql-driver/mysql DSN, e.g.
// "user:pass@tcp(host:3306)/video_extractor?parseTime=false".
func OpenMySQL(dsn string) (*Store, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(8)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	if _, err := db.Exec(mysqlSchema); err != nil {
		return nil, fmt.Errorf("migrate jobs table: %w", err)
	}
	for _, idx := range []struct{ name, cols string }{
		{"idx_jobs_status", "status"},
		{"idx_jobs_created_at", "created_at"},
	} {
		if _, err := db.Exec(fmt.Sprintf("CREATE INDEX %s ON jobs(%s)", idx.name, idx.cols)); err != nil &&
			!isDuplicateKeyErr(err) {
			return nil, fmt.Errorf("migrate index %s: %w", idx.name, err)
		}
	}
	return &Store{db: db}, nil
}

func isDuplicateKeyErr(err error) bool {
	// MySQL error 1061: "Duplicate key name" — the index already exists from a
	// prior Open() call against this database.
	return err != nil && strings.Contains(err.Error(), "1061")
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// CreateJob inserts a new job.
func (s *Store) CreateJob(j *models.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	j.CreatedAt = now
	j.UpdatedAt = now
	blob, err := json.Marshal(j)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO jobs (id, status, data, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		j.ID, string(j.Status), string(blob), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	)
	return err
}

// GetJob returns a deep copy of the stored job.
func (s *Store) GetJob(id string) (*models.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked(id)
}

func (s *Store) getLocked(id string) (*models.Job, error) {
	var data string
	err := s.db.QueryRow(`SELECT data FROM jobs WHERE id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var j models.Job
	if err := json.Unmarshal([]byte(data), &j); err != nil {
		return nil, err
	}
	return &j, nil
}

// UpdateJob applies fn to the current job under lock and persists the result.
// fn must not retain the *models.Job after returning. The updated job is returned.
func (s *Store) UpdateJob(id string, fn func(*models.Job) error) (*models.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	j, err := s.getLocked(id)
	if err != nil {
		return nil, err
	}
	if err := fn(j); err != nil {
		return nil, err
	}
	j.UpdatedAt = time.Now().UTC()
	blob, err := json.Marshal(j)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(
		`UPDATE jobs SET status = ?, data = ?, updated_at = ? WHERE id = ?`,
		string(j.Status), string(blob), j.UpdatedAt.Format(time.RFC3339Nano), id,
	); err != nil {
		return nil, err
	}
	return j, nil
}

// ListJobs returns the most recent jobs (newest first) as lightweight
// summaries for the "previous videos" timeline, without shipping every job's
// full segments/events/chat history over the wire.
func (s *Store) ListJobs(limit int) ([]models.JobSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`SELECT data FROM jobs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.JobSummary{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var j models.Job
		if err := json.Unmarshal([]byte(data), &j); err != nil {
			return nil, err
		}
		out = append(out, models.JobSummary{
			ID: j.ID, Filename: j.Filename, Status: j.Status, Origin: j.Origin,
			DurationSec: j.DurationSec, OverviewCollageUrl: j.OverviewCollage,
			OverviewStory: j.OverviewStory, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
		})
	}
	return out, rows.Err()
}

// FindSegment is a helper that locates a segment inside a job by id.
func FindSegment(j *models.Job, segmentID string) *models.TimelineSegment {
	for _, seg := range j.Segments {
		if seg.ID == segmentID {
			return seg
		}
	}
	return nil
}
