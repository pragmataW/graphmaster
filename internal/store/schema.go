package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// SchemaVersion is stored in graph.db's PRAGMA user_version. Bump it on any
// change to schema.sql or indexes.sql; older databases are rebuilt.
const SchemaVersion = 2

//go:embed schema.sql
var schemaSQL string

//go:embed indexes.sql
var indexesSQL string

// secondaryIndexes lists the index names declared in indexes.sql.
var secondaryIndexes = func() []string {
	re := regexp.MustCompile(`(?m)^CREATE INDEX IF NOT EXISTS (\w+)`)
	var names []string
	for _, m := range re.FindAllStringSubmatch(indexesSQL, -1) {
		names = append(names, m[1])
	}
	return names
}()

// errVersionRace is returned by createSchema when another process created a
// database with a different version between our check and our transaction.
var errVersionRace = errors.New("store: schema version changed concurrently")

// createSchema initializes an empty database inside one IMMEDIATE
// transaction. It is a no-op when a concurrent process already created the
// current schema.
func createSchema(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var ver int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&ver); err != nil {
		return err
	}
	switch ver {
	case SchemaVersion:
		committed = true
		_, err := conn.ExecContext(ctx, "COMMIT")
		return err
	case 0:
	default:
		return errVersionRace
	}
	for _, q := range []string{schemaSQL, indexesSQL} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("store: create schema: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key, value) VALUES(?, ?)`,
		MetaSchemaVer, strconv.Itoa(SchemaVersion)); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(SchemaVersion)); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}
