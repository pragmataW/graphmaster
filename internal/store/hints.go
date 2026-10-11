package store

import "context"

// AddDirtyHints records paths reported as edited by hooks (ts in unix ns).
// Autocommit; safe to call from short-lived hook processes.
func (s *Store) AddDirtyHints(ctx context.Context, paths []string, ts int64) error {
	if len(paths) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	st, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO dirty_hints(path, ts) VALUES(?, ?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, p := range paths {
		if _, err := st.ExecContext(ctx, p, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TakeDirtyHints atomically returns and clears all dirty hints.
func (s *Store) TakeDirtyHints(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `DELETE FROM dirty_hints RETURNING path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PendingNames returns queued name ids in ascending order; limit <= 0
// returns all.
func (s *Store) PendingNames(ctx context.Context, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = -1
	}
	return queryIDs(ctx, s.db, `SELECT name_id FROM pending_names ORDER BY name_id LIMIT ?`, limit)
}
