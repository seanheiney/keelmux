package store

import (
	"context"
	"database/sql"
)

// Migration only adds empty captain metadata. It never adopts a principal,
// derives grants from group membership, scans a terminal or dispatches work.
// Preexisting v5 objects are validated; missing authority is never reconstructed.
func migrateCaptains(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = createCaptainSchema(ctx, tx); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "PRAGMA user_version=5"); err != nil {
		return err
	}
	return tx.Commit()
}
