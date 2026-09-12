package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"niche-service/internal/service"
)

type handlers struct {
	svc *service.Service
}

// respondErr 把业务错误映射为稳定的 HTTP 状态码与错误码。
func respondErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error(), "code": "not_found"})
	case errors.Is(err, service.ErrBadRequest):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "bad_request"})
	case errors.Is(err, service.ErrOccupancyConflict):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "niche_unavailable"})
	case errors.Is(err, service.ErrOutstandingFees):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "outstanding_fees"})
	case errors.Is(err, service.ErrContactNotVerified):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "contact_not_verified"})
	case errors.Is(err, service.ErrInvalidState):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "invalid_state"})
	default:
		// 内部错误记录到服务端日志（脱敏后），不回传给客户端
		log.Printf("internal error: %s %s -> %s", c.Request.Method, Redact(c.Request.URL.Path), Redact(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error", "code": "internal"})
	}
}

func parseID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + name, "code": "bad_request"})
		return 0, false
	}
	return id, true
}

// canSeeFullID 复核岗与管理员可见完整证件号，其余角色一律脱敏。
func canSeeFullID(c *gin.Context) bool {
	role := identityOf(c).Role
	return role == "reviewer" || role == "admin"
}

func maskIDFor(c *gin.Context, idNumber string) string {
	if canSeeFullID(c) {
		return idNumber
	}
	return MaskIDNumber(idNumber)
}

func maskPhoneFor(c *gin.Context, phone string) string {
	if canSeeFullID(c) {
		return phone
	}
	return MaskPhone(phone)
}

func contactJSON(c *gin.Context, ct *service.Contact) gin.H {
	return gin.H{
		"id":         ct.ID,
		"name":       ct.Name,
		"id_number":  maskIDFor(c, ct.IDNumber),
		"phone":      maskPhoneFor(c, ct.Phone),
		"relation":   ct.Relation,
		"created_at": ct.CreatedAt,
	}
}

func contractJSON(c *gin.Context, d *service.ContractDetail) gin.H {
	contacts := make([]gin.H, 0, len(d.Contacts))
	for _, cc := range d.Contacts {
		contacts = append(contacts, gin.H{
			"link_id":     cc.LinkID,
			"contact_id":  cc.ContactID,
			"name":        cc.Name,
			"id_number":   maskIDFor(c, cc.IDNumber),
			"phone":       maskPhoneFor(c, cc.Phone),
			"relation":    cc.Relation,
			"auth_level":  cc.AuthLevel,
			"verified":    cc.Verified,
			"verified_by": cc.VerifiedBy,
			"verified_at": cc.VerifiedAt,
		})
	}
	occs := make([]gin.H, 0, len(d.Occupancies))
	for _, o := range d.Occupancies {
		occs = append(occs, gin.H{
			"id":         o.ID,
			"niche_id":   o.NicheID,
			"niche_code": o.NicheCode,
			"kind":       o.Kind,
			"status":     o.Status,
			"start_at":   o.StartAt,
			"end_at":     o.EndAt,
		})
	}
	return gin.H{
		"id":                 d.ID,
		"contract_no":        d.ContractNo,
		"niche_id":           d.NicheID,
		"niche_code":         d.NicheCode,
		"deceased_name":      d.DeceasedName,
		"deceased_id_number": maskIDFor(c, d.DeceasedIDNumber),
		"status":             d.Status,
		"start_at":           d.StartAt,
		"end_at":             d.EndAt,
		"annual_fee_cents":   d.AnnualFeeCents,
		"review_note":        d.ReviewNote,
		"reviewed_by":        d.ReviewedBy,
		"reviewed_at":        d.ReviewedAt,
		"created_by":         d.CreatedBy,
		"created_at":         d.CreatedAt,
		"contacts":           contacts,
		"occupancies":        occs,
	}
}

// ---------- 空位查询 ----------

func (h *handlers) listAvailableNiches(c *gin.Context) {
	zone := c.Query("zone")
	start := time.Now()
	end := start.AddDate(1, 0, 0)
	if v := c.Query("start_at"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start_at", "code": "bad_request"})
			return
		}
		start = t
	}
	if v := c.Query("end_at"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end_at", "code": "bad_request"})
			return
		}
		end = t
	}
	niches, err := h.svc.ListAvailableNiches(c.Request.Context(), zone, start, end)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"niches": niches, "start_at": start, "end_at": end})
}

func (h *handlers) getNiche(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	n, occ, err := h.svc.GetNiche(c.Request.Context(), id)
	if err != nil {
		respondErr(c, err)
		return
	}
	resp := gin.H{"niche": n}
	if occ != nil {
		resp["current_occupancy"] = occ
	} else {
		resp["current_occupancy"] = nil
	}
	c.JSON(http.StatusOK, resp)
}

// ---------- 联系人 ----------

type contactReq struct {
	Name     string `json:"name" binding:"required"`
	IDNumber string `json:"id_number" binding:"required"`
	Phone    string `json:"phone" binding:"required"`
	Relation string `json:"relation"`
}

func (h *handlers) createContact(c *gin.Context) {
	var req contactReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "bad_request"})
		return
	}
	ct, err := h.svc.CreateContact(c.Request.Context(), service.ContactInput{
		Name: req.Name, IDNumber: req.IDNumber, Phone: req.Phone, Relation: req.Relation,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, contactJSON(c, ct))
}

func (h *handlers) getContact(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	ct, err := h.svc.GetContact(c.Request.Context(), id)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, contactJSON(c, ct))
}

// ---------- 合同 ----------

type contactLinkReq struct {
	ContactID *int64 `json:"contact_id"`
	Name      string `json:"name"`
	IDNumber  string `json:"id_number"`
	Phone     string `json:"phone"`
	Relation  string `json:"relation"`
	AuthLevel string `json:"auth_level" binding:"required,oneof=view handle full"`
}

type createContractReq struct {
	NicheID          int64            `json:"niche_id" binding:"required"`
	DeceasedName     string           `json:"deceased_name" binding:"required"`
	DeceasedIDNumber string           `json:"deceased_id_number" binding:"required"`
	StartAt          time.Time        `json:"start_at" binding:"required"`
	EndAt            time.Time        `json:"end_at" binding:"required"`
	Contacts         []contactLinkReq `json:"contacts"`
}

func toLinkInputs(reqs []contactLinkReq) []service.ContactLinkInput {
	out := make([]service.ContactLinkInput, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, service.ContactLinkInput{
			ContactID: r.ContactID, Name: r.Name, IDNumber: r.IDNumber,
			Phone: r.Phone, Relation: r.Relation, AuthLevel: r.AuthLevel,
		})
	}
	return out
}

func (h *handlers) createContract(c *gin.Context) {
	var req createContractReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "bad_request"})
		return
	}
	d, err := h.svc.CreateContract(c.Request.Context(), identityOf(c).StaffID, service.CreateContractInput{
		NicheID:          req.NicheID,
		DeceasedName:     req.DeceasedName,
		DeceasedIDNumber: req.DeceasedIDNumber,
		StartAt:          req.StartAt,
		EndAt:            req.EndAt,
		Contacts:         toLinkInputs(req.Contacts),
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, contractJSON(c, d))
}

func (h *handlers) getContract(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	d, err := h.svc.GetContract(c.Request.Context(), id)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, contractJSON(c, d))
}

func (h *handlers) attachContact(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req contactLinkReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "bad_request"})
		return
	}
	d, err := h.svc.AttachContact(c.Request.Context(), id, toLinkInputs([]contactLinkReq{req})[0])
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, contractJSON(c, d))
}

func (h *handlers) verifyContact(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.svc.VerifyContractContact(c.Request.Context(), identityOf(c).StaffID, id); err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"link_id": id, "verified": true})
}

type reviewReq struct {
	Approve bool   `json:"approve"`
	Note    string `json:"note"`
}

func (h *handlers) reviewContract(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req reviewReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "bad_request"})
		return
	}
	d, err := h.svc.ReviewContract(c.Request.Context(), identityOf(c).StaffID, id, req.Approve, req.Note)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, contractJSON(c, d))
}

type checkoutReq struct {
	ContactID int64 `json:"contact_id" binding:"required"`
}

func (h *handlers) checkout(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req checkoutReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "bad_request"})
		return
	}
	d, err := h.svc.Checkout(c.Request.Context(), identityOf(c).StaffID, id, req.ContactID)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, contractJSON(c, d))
}

// ---------- 缴费 ----------

// forbiddenPaymentFields 缴费状态只能由本地模拟回执驱动，
// 请求体携带这些字段一律拒绝，杜绝“已付款布尔值放行”。
var forbiddenPaymentFields = []string{"paid", "status", "receipt_no", "paid_at"}

func (h *handlers) createPayment(c *gin.Context) {
	contractID, ok := parseID(c, "id")
	if !ok {
		return
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot read body", "code": "bad_request"})
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json", "code": "bad_request"})
		return
	}
	for _, f := range forbiddenPaymentFields {
		if _, present := raw[f]; present {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "payment status is receipt-driven; field '" + f + "' is not accepted",
				"code":  "bad_request",
			})
			return
		}
	}
	var req struct {
		Kind        string `json:"kind" binding:"required"`
		AmountCents int64  `json:"amount_cents" binding:"required"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "bad_request"})
		return
	}
	p, err := h.svc.CreatePayment(c.Request.Context(), contractID, req.Kind, req.AmountCents)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, p)
}

func (h *handlers) listPayments(c *gin.Context) {
	contractID, ok := parseID(c, "id")
	if !ok {
		return
	}
	ps, err := h.svc.ListPayments(c.Request.Context(), contractID)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"payments": ps})
}

// mockReceipt 本地模拟回执：服务端生成回执号并把账单置为已付，幂等。
func (h *handlers) mockReceipt(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	p, err := h.svc.PayWithMockReceipt(c.Request.Context(), id)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, p)
}

// ---------- 迁位 ----------

type createRelocationReq struct {
	ToNicheID int64 `json:"to_niche_id" binding:"required"`
	ContactID int64 `json:"contact_id" binding:"required"`
}

func (h *handlers) createRelocation(c *gin.Context) {
	contractID, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req createRelocationReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "bad_request"})
		return
	}
	r, err := h.svc.CreateRelocation(c.Request.Context(), identityOf(c).StaffID, contractID, req.ToNicheID, req.ContactID)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, r)
}

func (h *handlers) commitRelocation(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	r, err := h.svc.CommitRelocation(c.Request.Context(), identityOf(c).StaffID, id)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, r)
}

func (h *handlers) getRelocation(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	r, err := h.svc.GetRelocation(c.Request.Context(), id)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, r)
}
