package store

import "context"

func (s *Store) IdentityCacheEpoch(ctx context.Context) (int64, error) {
	var epoch int64
	err := s.pool.QueryRow(ctx, `SELECT epoch FROM identity_cache_state WHERE singleton`).Scan(&epoch)
	return epoch, err
}
func (s *Store) ClearIdentityCache(ctx context.Context) (int64, error) {
	var epoch int64
	err := s.pool.QueryRow(ctx, `UPDATE identity_cache_state SET epoch=epoch+1 WHERE singleton RETURNING epoch`).
		Scan(&epoch)
	return epoch, err
}
