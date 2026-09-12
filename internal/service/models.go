// Package service 实现格位合同服务的全部业务规则。
//
// 核心规则（与 README「办理规则」一致）：
//  1. 合同登记后为 pending_review，复核通过才生效并建立占位区间；
//  2. 同一格位的有效占位区间互不重叠（数据库排他约束兜底）；
//  3. 缴费只能由本地模拟回执驱动，请求中的已付款布尔值一律无效；
//  4. 迁出/迁位前须清偿全部未付费用，且经办联系人须已核实并具办理权限；
//  5. 迁位在单个事务内结束原占位并建立新占位，失败则整体回滚，
//     欠费时保留迁位草案，绝不留半完成迁出。
package service

import (
	"errors"
	"time"
)

// RelocationFeeCents 迁位手续费（示例规则：固定 200.00 元）。
const RelocationFeeCents int64 = 20000

var (
	ErrNotFound           = errors.New("not found")
	ErrBadRequest         = errors.New("bad request")
	ErrInvalidState       = errors.New("invalid state")
	ErrOccupancyConflict  = errors.New("niche occupancy conflict")
	ErrOutstandingFees    = errors.New("outstanding fees")
	ErrContactNotVerified = errors.New("contact not verified or not authorized")
)

type Niche struct {
	ID                int64  `json:"id"`
	Code              string `json:"code"`
	Zone              string `json:"zone"`
	Level             int    `json:"level"`
	PricePerYearCents int64  `json:"price_per_year_cents"`
	Status            string `json:"status"`
}

type Contact struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	IDNumber  string    `json:"id_number"`
	Phone     string    `json:"phone"`
	Relation  string    `json:"relation"`
	CreatedAt time.Time `json:"created_at"`
}

type Contract struct {
	ID               int64      `json:"id"`
	ContractNo       string     `json:"contract_no"`
	NicheID          int64      `json:"niche_id"`
	DeceasedName     string     `json:"deceased_name"`
	DeceasedIDNumber string     `json:"deceased_id_number"`
	Status           string     `json:"status"`
	StartAt          time.Time  `json:"start_at"`
	EndAt            time.Time  `json:"end_at"`
	AnnualFeeCents   int64      `json:"annual_fee_cents"`
	ReviewNote       *string    `json:"review_note"`
	ReviewedBy       *string    `json:"reviewed_by"`
	ReviewedAt       *time.Time `json:"reviewed_at"`
	CreatedBy        string     `json:"created_by"`
	CreatedAt        time.Time  `json:"created_at"`
}

type ContractContact struct {
	LinkID     int64      `json:"link_id"`
	ContactID  int64      `json:"contact_id"`
	Name       string     `json:"name"`
	IDNumber   string     `json:"id_number"`
	Phone      string     `json:"phone"`
	Relation   string     `json:"relation"`
	AuthLevel  string     `json:"auth_level"`
	Verified   bool       `json:"verified"`
	VerifiedBy *string    `json:"verified_by"`
	VerifiedAt *time.Time `json:"verified_at"`
}

type Occupancy struct {
	ID         int64     `json:"id"`
	ContractID int64     `json:"contract_id"`
	NicheID    int64     `json:"niche_id"`
	NicheCode  string    `json:"niche_code"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	StartAt    time.Time `json:"start_at"`
	EndAt      time.Time `json:"end_at"`
}

type ContractDetail struct {
	Contract
	NicheCode   string            `json:"niche_code"`
	Contacts    []ContractContact `json:"contacts"`
	Occupancies []Occupancy       `json:"occupancies"`
}

type Payment struct {
	ID           int64      `json:"id"`
	ContractID   int64      `json:"contract_id"`
	RelocationID *int64     `json:"relocation_id"`
	Kind         string     `json:"kind"`
	AmountCents  int64      `json:"amount_cents"`
	Status       string     `json:"status"`
	ReceiptNo    *string    `json:"receipt_no"`
	PaidAt       *time.Time `json:"paid_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

type Relocation struct {
	ID            int64      `json:"id"`
	ContractID    int64      `json:"contract_id"`
	FromNicheID   int64      `json:"from_niche_id"`
	ToNicheID     int64      `json:"to_niche_id"`
	FromNicheCode string     `json:"from_niche_code"`
	ToNicheCode   string     `json:"to_niche_code"`
	ContactID     int64      `json:"contact_id"`
	Status        string     `json:"status"`
	FailReason    *string    `json:"fail_reason"`
	RequestedBy   string     `json:"requested_by"`
	CreatedAt     time.Time  `json:"created_at"`
	CommittedAt   *time.Time `json:"committed_at"`
	FeePaymentID  *int64     `json:"fee_payment_id"`
}
