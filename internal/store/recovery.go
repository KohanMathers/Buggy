package store

import "context"

func (d *DB) ReplaceRecoveryCodes(ctx context.Context, userID int64, hashes []string) error {
	return d.InTx(ctx, func(tx *Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
			return err
		}
		t := now()
		for _, h := range hashes {
			if _, err := tx.Exec(ctx, `INSERT INTO recovery_codes (user_id, code_hash, used_at, created_at) VALUES (?, ?, 0, ?)`, userID, h, t); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *DB) ConsumeRecoveryCode(ctx context.Context, userID int64, hash string) (bool, error) {
	res, err := d.Exec(ctx, `UPDATE recovery_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at = 0`, now(), userID, hash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (d *DB) RecoveryCodeCounts(ctx context.Context, userID int64) (int, int) {
	var unused, total int
	d.QueryRow(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN used_at = 0 THEN 1 ELSE 0 END), 0) FROM recovery_codes WHERE user_id = ?`, userID).Scan(&total, &unused)
	return unused, total
}
