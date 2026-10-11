package store

import (
	"context"
	"database/sql"
	"errors"
)

// Well-known meta keys.
const (
	MetaSchemaVer      = "schema_ver"
	MetaState          = "state"    // StateBuilding | StateReady
	MetaProgress       = "progress" // free-form, e.g. "37"
	MetaLastScanNS     = "last_scan_ns"
	MetaGitIndexStat   = "gitindex_stat"
	MetaBulk           = "bulk"           // "1" while a bulk build defers indexes/FTS
	MetaExtractorVerPf = "extractor_ver:" // + model.Lang.String()
)

// Values for MetaState.
const (
	StateBuilding = "building"
	StateReady    = "ready"
)

// Meta returns a meta value; ok is false when the key is absent.
func (s *Store) Meta(ctx context.Context, key string) (value string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

// SetMeta upserts a meta value outside any batch (autocommit).
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key, value) VALUES(?, ?)`, key, value)
	return err
}

// DeleteMeta removes a meta key.
func (s *Store) DeleteMeta(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE key = ?`, key)
	return err
}
