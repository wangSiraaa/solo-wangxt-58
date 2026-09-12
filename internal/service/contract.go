package service

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type ContactLinkInput struct {
	ContactID *int64 // 已有联系人；为空则按下列字段新建/复用
	Name      string
	IDNumber  string
	Phone     string
	Relation  string
	AuthLevel string // view / handle / full
}

type CreateContractInput struct {
	NicheID          int64
	DeceasedName     string
	DeceasedIDNumber string
	StartAt          time.Time
	EndAt            time.Time
	Contacts         []ContactLinkInput
}

const contractCols = `
	c.id, c.contract_no, c.niche_id, n.code, c.deceased_name, c.deceased_id_number, c.status,
	c.start_at, c.end_at, c.annual_fee_cents, c.review_note, c.reviewed_by, c.reviewed_at,
	c.created_by, c.created_at`

func scanContractDetail(row pgx.Row) (*ContractDetail, error) {
	var d ContractDetail
	err := row.Scan(
		&d.ID, &d.ContractNo, &d.NicheID, &d.NicheCode, &d.DeceasedName, &d.DeceasedIDNumber, &d.Status,
		&d.StartAt, &d.EndAt, &d.AnnualFeeCents, &d.ReviewNote, &d.ReviewedBy, &d.ReviewedAt,
		&d.CreatedBy, &d.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// CreateContract 登记合同（pending_review）。此时尚未复核，不建立任何占位区间；
// 仅在登记前做一次友好的空位预检，真正的防重叠由复核生效时的排他约束保证。
func (s *Service) CreateContract(ctx context.Context, staffID string, in CreateContractInput) (*ContractDetail, error) {
	if in.DeceasedName == "" || in.DeceasedIDNumber == "" || !in.EndAt.After(in.StartAt) {
		return nil, ErrBadRequest
	}
	var id int64
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var price int64
		var nstatus string
		err := tx.QueryRow(ctx,
			`SELECT price_per_year_cents, status FROM niches WHERE id = $1`, in.NicheID).
			Scan(&price, &nstatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if nstatus != "available" {
			return ErrInvalidState // 维护中的格位不可办理
		}
		var occupied int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM occupancy_intervals
			WHERE niche_id = $1 AND status = 'active' AND period && tstzrange($2, $3, '[)')`,
			in.NicheID, in.StartAt, in.EndAt).Scan(&occupied); err != nil {
			return err
		}
		if occupied > 0 {
			return ErrOccupancyConflict
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO contracts (contract_no, niche_id, deceased_name, deceased_id_number,
			                       start_at, end_at, annual_fee_cents, created_by)
			VALUES ('C-' || to_char(now(), 'YYYY') || '-' || lpad(nextval('contract_no_seq')::text, 6, '0'),
			        $1, $2, $3, $4, $5, $6, $7)
			RETURNING id`,
			in.NicheID, in.DeceasedName, in.DeceasedIDNumber, in.StartAt, in.EndAt, price, staffID).
			Scan(&id)
		if err != nil {
			return err
		}
		for _, cl := range in.Contacts {
			if _, err := linkContact(ctx, tx, id, cl); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetContract(ctx, id)
}

// linkContact 将联系人关联到合同；ContactID 为空时先按证件号新建/复用联系人。
// 返回 link id。新建关联一律 verified = false，须复核岗核实。
func linkContact(ctx context.Context, tx pgx.Tx, contractID int64, cl ContactLinkInput) (int64, error) {
	switch cl.AuthLevel {
	case "view", "handle", "full":
	default:
		return 0, ErrBadRequest
	}
	contactID := int64(0)
	if cl.ContactID != nil {
		contactID = *cl.ContactID
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM contacts WHERE id = $1)`, contactID).Scan(&exists); err != nil {
			return 0, err
		}
		if !exists {
			return 0, ErrNotFound
		}
	} else {
		if cl.Name == "" || cl.IDNumber == "" || cl.Phone == "" {
			return 0, ErrBadRequest
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO contacts (name, id_number, phone, relation)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (id_number) DO UPDATE
			  SET name = EXCLUDED.name, phone = EXCLUDED.phone, relation = EXCLUDED.relation
			RETURNING id`, cl.Name, cl.IDNumber, cl.Phone, cl.Relation).Scan(&contactID); err != nil {
			return 0, err
		}
	}
	var linkID int64
	err := tx.QueryRow(ctx, `
		INSERT INTO contract_contacts (contract_id, contact_id, auth_level)
		VALUES ($1, $2, $3)
		ON CONFLICT (contract_id, contact_id) DO UPDATE SET auth_level = EXCLUDED.auth_level
		RETURNING id`, contractID, contactID, cl.AuthLevel).Scan(&linkID)
	if err != nil {
		return 0, err
	}
	return linkID, nil
}

func (s *Service) GetContract(ctx context.Context, id int64) (*ContractDetail, error) {
	d, err := scanContractDetail(s.Pool.QueryRow(ctx,
		`SELECT `+contractCols+` FROM contracts c JOIN niches n ON n.id = c.niche_id WHERE c.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := s.loadContractRelations(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Service) loadContractRelations(ctx context.Context, d *ContractDetail) error {
	rows, err := s.Pool.Query(ctx, `
		SELECT cc.id, cc.contact_id, ct.name, ct.id_number, ct.phone, ct.relation,
		       cc.auth_level, cc.verified, cc.verified_by, cc.verified_at
		FROM contract_contacts cc JOIN contacts ct ON ct.id = cc.contact_id
		WHERE cc.contract_id = $1 ORDER BY cc.id`, d.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	d.Contacts = []ContractContact{}
	for rows.Next() {
		var cc ContractContact
		if err := rows.Scan(&cc.LinkID, &cc.ContactID, &cc.Name, &cc.IDNumber, &cc.Phone, &cc.Relation,
			&cc.AuthLevel, &cc.Verified, &cc.VerifiedBy, &cc.VerifiedAt); err != nil {
			return err
		}
		d.Contacts = append(d.Contacts, cc)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	orows, err := s.Pool.Query(ctx, `
		SELECT o.id, o.contract_id, o.niche_id, n.code, o.kind, o.status, lower(o.period), upper(o.period)
		FROM occupancy_intervals o JOIN niches n ON n.id = o.niche_id
		WHERE o.contract_id = $1 ORDER BY o.id`, d.ID)
	if err != nil {
		return err
	}
	defer orows.Close()
	d.Occupancies = []Occupancy{}
	for orows.Next() {
		var o Occupancy
		if err := orows.Scan(&o.ID, &o.ContractID, &o.NicheID, &o.NicheCode, &o.Kind, &o.Status, &o.StartAt, &o.EndAt); err != nil {
			return err
		}
		d.Occupancies = append(d.Occupancies, o)
	}
	return orows.Err()
}

// AttachContact 为合同追加联系人关联（未核实状态）。
func (s *Service) AttachContact(ctx context.Context, contractID int64, cl ContactLinkInput) (*ContractDetail, error) {
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM contracts WHERE id = $1 FOR UPDATE`, contractID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status == "ended" || status == "cancelled" {
			return ErrInvalidState
		}
		_, err = linkContact(ctx, tx, contractID, cl)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetContract(ctx, contractID)
}

// VerifyContractContact 复核岗核实联系人关系，核实后该联系人才具备相应办理权限。
func (s *Service) VerifyContractContact(ctx context.Context, staffID string, linkID int64) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE contract_contacts
		SET verified = true, verified_by = $2, verified_at = now()
		WHERE id = $1`, linkID, staffID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ReviewContract 信息复核。通过时在同一个事务内：置合同为 active、建立初始占位区间、
// 生成首年费用账单。占位区间的排他约束是防双窗口并发的最终防线：
// 两个窗口同时复核通过同一格位的合同，只有一个能提交成功。
func (s *Service) ReviewContract(ctx context.Context, staffID string, contractID int64, approve bool, note string) (*ContractDetail, error) {
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var status string
		var nicheID int64
		var startAt, endAt time.Time
		var annualFee int64
		err := tx.QueryRow(ctx, `
			SELECT status, niche_id, start_at, end_at, annual_fee_cents
			FROM contracts WHERE id = $1 FOR UPDATE`, contractID).
			Scan(&status, &nicheID, &startAt, &endAt, &annualFee)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != "pending_review" {
			return ErrInvalidState
		}
		if !approve {
			_, err := tx.Exec(ctx, `
				UPDATE contracts SET status = 'cancelled', review_note = $2,
				       reviewed_by = $3, reviewed_at = now(), updated_at = now()
				WHERE id = $1`, contractID, note, staffID)
			return err
		}
		// 复核通过的前置条件：至少一名已核实且具办理权限的联系人
		var verifiedHandlers int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM contract_contacts
			WHERE contract_id = $1 AND verified AND auth_level IN ('handle','full')`,
			contractID).Scan(&verifiedHandlers); err != nil {
			return err
		}
		if verifiedHandlers == 0 {
			return ErrContactNotVerified
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO occupancy_intervals (contract_id, niche_id, period, kind)
			VALUES ($1, $2, tstzrange($3, $4, '[)'), 'initial')`,
			contractID, nicheID, startAt, endAt); err != nil {
			if isExclusionViolation(err) {
				return ErrOccupancyConflict
			}
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE contracts SET status = 'active', review_note = $2,
			       reviewed_by = $3, reviewed_at = now(), updated_at = now()
			WHERE id = $1`, contractID, note, staffID); err != nil {
			return err
		}
		// 首年费用账单（未缴清将影响后续迁出/迁位）
		_, err = tx.Exec(ctx, `
			INSERT INTO payments (contract_id, kind, amount_cents) VALUES ($1, 'annual_fee', $2)`,
			contractID, annualFee)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetContract(ctx, contractID)
}

// Checkout 迁出：须费用全部清偿、经办联系人已核实且具办理权限。
// 在事务内结束有效占位区间并将合同置为 ended；任一前置条件不满足则整体不变。
func (s *Service) Checkout(ctx context.Context, staffID string, contractID, contactID int64) (*ContractDetail, error) {
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM contracts WHERE id = $1 FOR UPDATE`, contractID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != "active" {
			return ErrInvalidState
		}
		if err := requireVerifiedHandler(ctx, tx, contractID, contactID); err != nil {
			return err
		}
		if err := requireNoOutstandingFees(ctx, tx, contractID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE occupancy_intervals
			SET status = 'ended', period = tstzrange(lower(period), now(), '[)')
			WHERE contract_id = $1 AND status = 'active'`, contractID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE contracts SET status = 'ended', end_at = now(), updated_at = now() WHERE id = $1`, contractID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetContract(ctx, contractID)
}

// requireVerifiedHandler 校验经办联系人：已核实且授权级别为 handle/full。
func requireVerifiedHandler(ctx context.Context, tx pgx.Tx, contractID, contactID int64) error {
	var ok bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM contract_contacts
			WHERE contract_id = $1 AND contact_id = $2 AND verified AND auth_level IN ('handle','full')
		)`, contractID, contactID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrContactNotVerified
	}
	return nil
}

// requireNoOutstandingFees 校验合同名下无未付账单。
func requireNoOutstandingFees(ctx context.Context, tx pgx.Tx, contractID int64) error {
	var unpaid int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM payments WHERE contract_id = $1 AND status = 'unpaid'`,
		contractID).Scan(&unpaid); err != nil {
		return err
	}
	if unpaid > 0 {
		return ErrOutstandingFees
	}
	return nil
}
