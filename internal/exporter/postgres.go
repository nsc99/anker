package exporter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// One database stores one configured Anki collection. This lock coordinates
// migration and refresh transactions across replicas of that exporter.
const postgresLockID int64 = 0x616e6b6572

const postgresSchema = `
CREATE SCHEMA IF NOT EXISTS anker;
CREATE TABLE IF NOT EXISTS anker.collection_state (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 rollover smallint NOT NULL CHECK (rollover BETWEEN 0 AND 23),
 synced_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS anker.reviews (
 id bigint PRIMARY KEY,
 ease smallint NOT NULL CHECK (ease BETWEEN 1 AND 4),
 kind smallint NOT NULL CHECK (kind BETWEEN 0 AND 3),
 milliseconds integer NOT NULL
);`

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid ANKER_DATABASE_URL")
	}
	config.MaxConns = 4
	if config.ConnConfig.ConnectTimeout == 0 {
		config.ConnConfig.ConnectTimeout = 10 * time.Second
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, postgresError("connect", err)
	}
	store := &PostgresStore{pool: pool}
	if err := store.initialize(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

func (p *PostgresStore) Close() { p.pool.Close() }

func (p *PostgresStore) initialize(ctx context.Context) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return postgresError("initialize", err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", postgresLockID+1); err != nil {
		return postgresError("migration lock", err)
	}
	if _, err = tx.Exec(ctx, postgresSchema); err != nil {
		return postgresError("create schema", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return postgresError("commit schema", err)
	}
	return nil
}

// Load reads history and metadata from the same committed snapshot, so an API
// replica can never restore the timestamp from one refresh with another's data.
func (p *PostgresStore) Load(ctx context.Context) (*Collection, time.Time, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, time.Time{}, postgresError("load", err)
	}
	defer tx.Rollback(context.Background())
	col := &Collection{}
	var syncedAt time.Time
	err = tx.QueryRow(ctx, "SELECT rollover, synced_at FROM anker.collection_state WHERE singleton").Scan(&col.Rollover, &syncedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, postgresError("load metadata", err)
	}
	rows, err := tx.Query(ctx, "SELECT id, ease, kind, milliseconds FROM anker.reviews ORDER BY id")
	if err != nil {
		return nil, time.Time{}, postgresError("load reviews", err)
	}
	for rows.Next() {
		var review Review
		if err := rows.Scan(&review.Timestamp, &review.Ease, &review.Kind, &review.Milliseconds); err != nil {
			rows.Close()
			return nil, time.Time{}, postgresError("read review", err)
		}
		col.Reviews = append(col.Reviews, review)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, postgresError("read reviews", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, time.Time{}, postgresError("finish load", err)
	}
	return col, syncedAt.UTC(), nil
}

// Sync holds an advisory transaction lock while downloading and saving. Other
// replicas restore the last committed snapshot instead of duplicating the sync.
// A failed download, COPY, or commit leaves every row of the last snapshot intact.
func (p *PostgresStore) Sync(ctx context.Context, minInterval time.Duration, fetch func() (*Collection, time.Time, error)) (*Collection, time.Time, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, time.Time{}, postgresError("begin refresh", err)
	}
	defer tx.Rollback(context.Background())
	var acquired bool
	if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", postgresLockID).Scan(&acquired); err != nil {
		return nil, time.Time{}, postgresError("refresh lock", err)
	}
	if !acquired {
		if err := tx.Rollback(ctx); err != nil {
			return nil, time.Time{}, postgresError("release refresh", err)
		}
		return p.Load(ctx)
	}
	if minInterval > 0 {
		var lastSync time.Time
		err := tx.QueryRow(ctx, "SELECT synced_at FROM anker.collection_state WHERE singleton").Scan(&lastSync)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, time.Time{}, postgresError("check refresh interval", err)
		}
		if err == nil && time.Since(lastSync) < minInterval {
			if err := tx.Rollback(ctx); err != nil {
				return nil, time.Time{}, postgresError("release refresh", err)
			}
			return p.Load(ctx)
		}
	}
	col, syncedAt, err := fetch()
	if err != nil {
		return nil, time.Time{}, err
	}
	if col == nil {
		return nil, time.Time{}, fmt.Errorf("refresh returned no collection")
	}
	syncedAt = syncedAt.UTC().Truncate(time.Microsecond)
	if _, err := tx.Exec(ctx, "DELETE FROM anker.reviews"); err != nil {
		return nil, time.Time{}, postgresError("replace reviews", err)
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"anker", "reviews"}, []string{"id", "ease", "kind", "milliseconds"}, pgx.CopyFromSlice(len(col.Reviews), func(i int) ([]any, error) {
		r := col.Reviews[i]
		return []any{r.Timestamp, r.Ease, r.Kind, r.Milliseconds}, nil
	}))
	if err != nil {
		return nil, time.Time{}, postgresError("save reviews", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO anker.collection_state (singleton,rollover,synced_at) VALUES (true,$1,$2)
 ON CONFLICT (singleton) DO UPDATE SET rollover=excluded.rollover,synced_at=excluded.synced_at`, col.Rollover, syncedAt)
	if err != nil {
		return nil, time.Time{}, postgresError("save metadata", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, time.Time{}, postgresError("commit refresh", err)
	}
	return col, syncedAt, nil
}

func postgresError(action string, err error) error {
	// pgx connection errors and server messages can contain DSNs, usernames, or
	// data values. Log only the operation and standardized SQLSTATE when present.
	var serverError *pgconn.PgError
	if errors.As(err, &serverError) {
		return fmt.Errorf("postgres %s failed (SQLSTATE %s)", action, serverError.Code)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("postgres %s: context canceled", action)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("postgres %s: deadline exceeded", action)
	}
	return fmt.Errorf("postgres %s failed; check database connectivity and configuration", action)
}
