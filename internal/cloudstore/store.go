// Package cloudstore implements the PostgreSQL backend for the engram cloud sync server.
// It provides storage, conflict resolution (LWW + revisions), and sync protocol support.
package cloudstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the cloud server's PostgreSQL storage backend.
type Store struct {
	pool *pgxpool.Pool
}

// New creates a new CloudStore connected to the given PostgreSQL connection string.
func New(ctx context.Context, connString string) (*Store, error) {
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: parse config: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: connect: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("cloudstore: ping: %w", err)
	}

	return &Store{pool: pool}, nil
}

// RunMigrations creates all required tables and indexes if they don't exist.
func (s *Store) RunMigrations(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, schemaSQL)
	if err != nil {
		return fmt.Errorf("cloudstore: run migrations: %w", err)
	}
	return nil
}

// Close releases the connection pool.
func (s *Store) Close() {
	s.pool.Close()
}

// Pool returns the underlying pgxpool for use by handlers that need direct access.
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}
