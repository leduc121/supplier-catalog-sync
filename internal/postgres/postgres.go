package postgres

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"supplier-catalog-sync/internal/domain"
	"supplier-catalog-sync/internal/store"
	"supplier-catalog-sync/internal/syncer"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Backend struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, databaseURL string) (*Backend, error) {
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	config.MaxConns = 4
	config.MinConns = 0
	config.MaxConnLifetime = 5 * time.Minute
	config.MaxConnIdleTime = time.Minute
	config.HealthCheckPeriod = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Backend{pool: pool}, nil
}

func (b *Backend) Close() { b.pool.Close() }

func (b *Backend) OpenSession(ctx context.Context, sessionID string) (syncer.Repository, error) {
	empty, err := json.Marshal(&store.State{Products: map[string]domain.Product{}, Runs: []domain.Run{}, Issues: []domain.Issue{}, Changes: []domain.Change{}})
	if err != nil {
		return nil, err
	}
	if _, err := b.pool.Exec(ctx, `INSERT INTO catalog_session_states (session_id, state) VALUES ($1, $2::jsonb) ON CONFLICT (session_id) DO NOTHING`, sessionID, empty); err != nil {
		return nil, fmt.Errorf("create session state: %w", err)
	}
	var version int64
	var raw []byte
	if err := b.pool.QueryRow(ctx, `SELECT version, state FROM catalog_session_states WHERE session_id = $1`, sessionID).Scan(&version, &raw); err != nil {
		return nil, fmt.Errorf("load session state: %w", err)
	}
	var state store.State
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("decode session state: %w", err)
	}
	if state.Products == nil {
		state.Products = make(map[string]domain.Product)
	}
	return &SessionStore{backend: b, ctx: ctx, sessionID: sessionID, version: version, state: &state}, nil
}

func (b *Backend) ResetSession(ctx context.Context, sessionID string) error {
	_, err := b.pool.Exec(ctx, `DELETE FROM catalog_session_states WHERE session_id = $1`, sessionID)
	if err != nil {
		return fmt.Errorf("reset session: %w", err)
	}
	return nil
}

type SessionStore struct {
	backend   *Backend
	ctx       context.Context
	sessionID string
	version   int64
	state     *store.State
}

func (s *SessionStore) State() *store.State { return s.state }

func (s *SessionStore) Commit(next *store.State) error {
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	tx, err := s.backend.pool.BeginTx(s.ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin catalog commit: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var current int64
	if err := tx.QueryRow(s.ctx, `SELECT version FROM catalog_session_states WHERE session_id = $1 FOR UPDATE`, s.sessionID).Scan(&current); err != nil {
		return fmt.Errorf("lock session state: %w", err)
	}
	if current != s.version {
		return store.ErrConcurrentUpdate
	}
	if _, err := tx.Exec(s.ctx, `UPDATE catalog_session_states SET state = $2::jsonb, version = version + 1, updated_at = now() WHERE session_id = $1`, s.sessionID, raw); err != nil {
		return fmt.Errorf("write session state: %w", err)
	}
	if err := tx.Commit(s.ctx); err != nil {
		return fmt.Errorf("commit session state: %w", err)
	}
	s.version++
	s.state = next
	return nil
}

func Migrate(ctx context.Context, databaseURL string) error {
	backend, err := Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer backend.Close()
	tx, err := backend.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(739184221)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS catalog_schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		var applied bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM catalog_schema_migrations WHERE version = $1)`, entry.Name()).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		sql, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO catalog_schema_migrations (version) VALUES ($1)`, entry.Name()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
