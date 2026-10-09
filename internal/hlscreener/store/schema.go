// Schema auto-initialization: the data-plane cmds embed the SQL
// migrations and apply them on startup, so containers (docker compose)
// never need a manual psql step. The schema is idempotent — every
// statement uses CREATE TABLE IF NOT EXISTS — and an advisory lock
// serializes concurrent bootstraps: several hl-* containers start at
// the same time in docker compose, and parallel CREATE TABLE can
// collide on PostgreSQL's pg_type catalog (SQLSTATE 23505) even with
// IF NOT EXISTS, because the existence check and the catalog insert
// are not atomic across sessions.
package store

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// schemaLockID is an arbitrary constant key for the advisory lock that
// serializes EnsureSchema across processes sharing the database.
const schemaLockID = 0x6e6f6678686c // "nofxhl"

// EnsureSchema applies all embedded migrations in filename order.
// Safe to call on every startup and from multiple cmds sharing one
// database: the DDL is IF NOT EXISTS throughout and an advisory lock
// prevents concurrent-applies catalog races.
func (s *Store) EnsureSchema(ctx context.Context) error {
	// Hold ONE connection for the whole run — session-level advisory
	// locks are per-connection, and pool.Exec may rotate connections.
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", schemaLockID); err != nil {
		return fmt.Errorf("advisory lock: %w", err)
	}
	defer func() {
		// Best-effort unlock; the lock is also released when the
		// connection closes.
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", schemaLockID)
	}()

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if _, err := conn.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
	}
	return nil
}
