package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const paymentCols = `id, contract_id, relocation_id, kind, amount_cents, status, receipt_no, paid_at, created_at`

func scanPayment(row pgx.Row) (*Payment, error) {
	var p Payment
	err := row.Scan(&p.ID, &p.ContractID, &p.RelocationID, &p.Kind, &p.AmountCents,
		&p.Status, &p.ReceiptNo, &p.PaidAt, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// CreatePayment 为合同开立账单。缴费状态只能由模拟回执推进，
// 本函数不接受、也不允许调用方指定任何“已付款”标志。
func (s *Service) CreatePayment(ctx context.Context, contractID int64, kind string, amountCents int64) (*Payment, error) {
	if amountCents <= 0 {
		return nil, ErrBadRequest
	}
	switch kind {
	case "annual_fee", "other": // relocation_fee 只能由迁位流程内部开立
	default:
		return nil, ErrBadRequest
	}
	var exists bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM contracts WHERE id = $1)`, contractID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	p, err := scanPayment(s.Pool.QueryRow(ctx, `
		INSERT INTO payments (contract_id, kind, amount_cents) VALUES ($1, $2, $3)
		RETURNING `+paymentCols, contractID, kind, amountCents))
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) GetPayment(ctx context.Context, id int64) (*Payment, error) {
	p, err := scanPayment(s.Pool.QueryRow(ctx, `SELECT `+paymentCols+` FROM payments WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) ListPayments(ctx context.Context, contractID int64) ([]Payment, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+paymentCols+` FROM payments WHERE contract_id = $1 ORDER BY id`, contractID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Payment{}
	for rows.Next() {
		var p Payment
		if err := rows.Scan(&p.ID, &p.ContractID, &p.RelocationID, &p.Kind, &p.AmountCents,
			&p.Status, &p.ReceiptNo, &p.PaidAt, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PayWithMockReceipt 本地模拟回执：服务端生成唯一回执号并把账单置为 paid。
// 幂等——并发或重复调用都返回同一张回执，账单只会被支付一次：
// 条件更新 WHERE status='unpaid' 保证只有一个执行流真正改写状态，
// 其余执行流读到已支付结果后原样返回。
func (s *Service) PayWithMockReceipt(ctx context.Context, paymentID int64) (*Payment, error) {
	for attempt := 0; attempt < 3; attempt++ {
		receipt := newReceiptNo(paymentID)
		p, err := scanPayment(s.Pool.QueryRow(ctx, `
			UPDATE payments SET status = 'paid', receipt_no = $2, paid_at = now()
			WHERE id = $1 AND status = 'unpaid'
			RETURNING `+paymentCols, paymentID, receipt))
		if errors.Is(err, pgx.ErrNoRows) {
			// 已被其他执行流支付（或不存在）：返回现状，保证幂等
			return s.GetPayment(ctx, paymentID)
		}
		if isUniqueViolation(err) {
			continue // 回执号撞号（极不可能），重试
		}
		if err != nil {
			return nil, err
		}
		return p, nil
	}
	return nil, fmt.Errorf("cannot allocate unique receipt number")
}

func newReceiptNo(paymentID int64) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("RCPT-%s-%06d-%s", time.Now().Format("20060102"), paymentID, hex.EncodeToString(b[:]))
}
