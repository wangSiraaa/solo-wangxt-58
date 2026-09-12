package service

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ListAvailableNiches 查询在 [start, end) 期间无任何有效占位、且非维护中的格位。
func (s *Service) ListAvailableNiches(ctx context.Context, zone string, start, end time.Time) ([]Niche, error) {
	if !end.After(start) {
		return nil, ErrBadRequest
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT n.id, n.code, n.zone, n.level, n.price_per_year_cents, n.status
		FROM niches n
		WHERE n.status = 'available'
		  AND ($1 = '' OR n.zone = $1)
		  AND NOT EXISTS (
		      SELECT 1 FROM occupancy_intervals o
		      WHERE o.niche_id = n.id AND o.status = 'active'
		        AND o.period && tstzrange($2, $3, '[)')
		  )
		ORDER BY n.code`, zone, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Niche{}
	for rows.Next() {
		var n Niche
		if err := rows.Scan(&n.ID, &n.Code, &n.Zone, &n.Level, &n.PricePerYearCents, &n.Status); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// GetNiche 返回格位及其当前有效占位（如有）。
func (s *Service) GetNiche(ctx context.Context, id int64) (*Niche, *Occupancy, error) {
	var n Niche
	err := s.Pool.QueryRow(ctx, `
		SELECT id, code, zone, level, price_per_year_cents, status FROM niches WHERE id = $1`, id).
		Scan(&n.ID, &n.Code, &n.Zone, &n.Level, &n.PricePerYearCents, &n.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	var occ Occupancy
	err = s.Pool.QueryRow(ctx, `
		SELECT o.id, o.contract_id, o.niche_id, n.code, o.kind, o.status, lower(o.period), upper(o.period)
		FROM occupancy_intervals o JOIN niches n ON n.id = o.niche_id
		WHERE o.niche_id = $1 AND o.status = 'active'`, id).
		Scan(&occ.ID, &occ.ContractID, &occ.NicheID, &occ.NicheCode, &occ.Kind, &occ.Status, &occ.StartAt, &occ.EndAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return &n, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &n, &occ, nil
}
