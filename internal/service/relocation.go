package service

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const relocationCols = `
	r.id, r.contract_id, r.from_niche_id, r.to_niche_id, nf.code, nt.code,
	r.contact_id, r.status, r.fail_reason, r.requested_by, r.created_at, r.committed_at,
	(SELECT p.id FROM payments p WHERE p.relocation_id = r.id AND p.kind = 'relocation_fee'
	 ORDER BY p.id LIMIT 1)`

func scanRelocation(row pgx.Row) (*Relocation, error) {
	var r Relocation
	err := row.Scan(&r.ID, &r.ContractID, &r.FromNicheID, &r.ToNicheID, &r.FromNicheCode, &r.ToNicheCode,
		&r.ContactID, &r.Status, &r.FailReason, &r.RequestedBy, &r.CreatedAt, &r.CommittedAt, &r.FeePaymentID)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Service) GetRelocation(ctx context.Context, id int64) (*Relocation, error) {
	r, err := scanRelocation(s.Pool.QueryRow(ctx, `
		SELECT `+relocationCols+`
		FROM relocations r
		JOIN niches nf ON nf.id = r.from_niche_id
		JOIN niches nt ON nt.id = r.to_niche_id
		WHERE r.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// CreateRelocation 建立迁位草案：校验合同有效、经办联系人已核实且具办理权限、
// 目标格位可办理，并同时开立迁位手续费账单。欠费不阻止建草案——
// 费用清偿在提交（commit）时核对，欠费时草案原样保留。
func (s *Service) CreateRelocation(ctx context.Context, staffID string, contractID, toNicheID, contactID int64) (*Relocation, error) {
	var id int64
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var status string
		var fromNicheID int64
		err := tx.QueryRow(ctx, `
			SELECT status, niche_id FROM contracts WHERE id = $1 FOR UPDATE`,
			contractID).Scan(&status, &fromNicheID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != "active" {
			return ErrInvalidState
		}
		if toNicheID == fromNicheID {
			return ErrBadRequest
		}
		if err := requireVerifiedHandler(ctx, tx, contractID, contactID); err != nil {
			return err
		}
		var nstatus string
		err = tx.QueryRow(ctx, `SELECT status FROM niches WHERE id = $1`, toNicheID).Scan(&nstatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if nstatus != "available" {
			return ErrInvalidState
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO relocations (contract_id, from_niche_id, to_niche_id, contact_id, requested_by)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			contractID, fromNicheID, toNicheID, contactID, staffID).Scan(&id); err != nil {
			return err
		}
		// 迁位手续费账单（未付），提交迁位前须一并清偿
		_, err = tx.Exec(ctx, `
			INSERT INTO payments (contract_id, relocation_id, kind, amount_cents)
			VALUES ($1, $2, 'relocation_fee', $3)`, contractID, id, RelocationFeeCents)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetRelocation(ctx, id)
}

// CommitRelocation 提交迁位。在单个事务内：核对费用清偿 → 结束原占位 → 建立新占位 →
// 更新合同格位 → 草案置为 committed。任何一步失败整体回滚，原合同与原占位保持不变。
// 欠费时返回 ErrOutstandingFees，草案保持 draft（不产生半完成迁出）；
// 目标格位被并发占用时草案置为 failed 并记录原因。
func (s *Service) CommitRelocation(ctx context.Context, staffID string, relocationID int64) (*Relocation, error) {
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var status string
		var contractID, toNicheID int64
		err := tx.QueryRow(ctx, `
			SELECT status, contract_id, to_niche_id FROM relocations WHERE id = $1 FOR UPDATE`,
			relocationID).Scan(&status, &contractID, &toNicheID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != "draft" {
			return ErrInvalidState
		}
		var cstatus string
		var endAt time.Time
		if err := tx.QueryRow(ctx, `
			SELECT status, end_at FROM contracts WHERE id = $1 FOR UPDATE`,
			contractID).Scan(&cstatus, &endAt); err != nil {
			return err
		}
		if cstatus != "active" {
			return ErrInvalidState
		}
		// 费用清偿核对：欠费则直接返回，草案原样保留，事务不做任何写操作
		if err := requireNoOutstandingFees(ctx, tx, contractID); err != nil {
			return err
		}
		var nstatus string
		if err := tx.QueryRow(ctx, `SELECT status FROM niches WHERE id = $1`, toNicheID).Scan(&nstatus); err != nil {
			return err
		}
		if nstatus != "available" {
			return ErrOccupancyConflict
		}
		// 结束原占位（恰好一条有效区间）
		tag, err := tx.Exec(ctx, `
			UPDATE occupancy_intervals
			SET status = 'ended', period = tstzrange(lower(period), now(), '[)')
			WHERE contract_id = $1 AND status = 'active'`, contractID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrInvalidState
		}
		// 建立新占位；排他约束冲突 → 回滚，由外层把草案标记为 failed
		if _, err := tx.Exec(ctx, `
			INSERT INTO occupancy_intervals (contract_id, niche_id, period, kind)
			VALUES ($1, $2, tstzrange(now(), $3, '[)'), 'relocation')`,
			contractID, toNicheID, endAt); err != nil {
			if isExclusionViolation(err) {
				return ErrOccupancyConflict
			}
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE contracts SET niche_id = $2, updated_at = now() WHERE id = $1`,
			contractID, toNicheID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE relocations SET status = 'committed', committed_at = now() WHERE id = $1`,
			relocationID)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrOccupancyConflict) {
			// 事务已回滚，原占位完好；另起事务把草案标记为失败
			_, _ = s.Pool.Exec(ctx, `
				UPDATE relocations SET status = 'failed', fail_reason = 'target niche occupied'
				WHERE id = $1 AND status = 'draft'`, relocationID)
		}
		return nil, err
	}
	return s.GetRelocation(ctx, relocationID)
}
