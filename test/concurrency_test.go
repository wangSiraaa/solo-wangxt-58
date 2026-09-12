// Package test 端到端并发测试：使用 embedded-postgres 启动真实 PostgreSQL，
// 通过 httptest 走完整 HTTP 栈，覆盖两个窗口争用格位、重复回执、
// 迁位事务失败等场景，并验证失败后原合同与原位置仍然有效。
package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"niche-service/internal/assets"
	"niche-service/internal/config"
	"niche-service/internal/db"
	"niche-service/internal/httpapi"
	"niche-service/internal/service"
)

const (
	tokClerk1   = "dev-clerk-1"
	tokClerk2   = "dev-clerk-2"
	tokReviewer = "dev-reviewer"
	tokFinance  = "dev-finance"
	tokAdmin    = "dev-admin"
)

var (
	testPool   *pgxpool.Pool
	testServer *httptest.Server
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	port := uint32(lis.Addr().(*net.TCPAddr).Port)
	lis.Close()

	workDir, err := os.MkdirTemp("", "niche-pg-")
	if err != nil {
		panic(err)
	}
	binDir := filepath.Join(os.Getenv("HOME"), ".cache", "niche-test-pg-binaries")
	pg := embeddedpostgres.NewDatabase(
		embeddedpostgres.DefaultConfig().
			Port(port).
			BinariesPath(binDir).
			RuntimePath(filepath.Join(workDir, "runtime")).
			DataPath(filepath.Join(workDir, "data")).
			Logger(io.Discard),
	)
	if err := pg.Start(); err != nil {
		panic(fmt.Sprintf("start embedded postgres: %v", err))
	}

	dsn := fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", port)
	testPool, err = db.Connect(ctx, dsn)
	if err != nil {
		panic(err)
	}
	if err := db.ApplySQLDir(ctx, testPool, assets.Migrations, "migrations"); err != nil {
		panic(err)
	}

	router := httpapi.NewRouter(service.New(testPool), config.Load().Tokens, io.Discard)
	testServer = httptest.NewServer(router)

	code := m.Run()

	testServer.Close()
	testPool.Close()
	_ = pg.Stop()
	_ = os.RemoveAll(workDir)
	os.Exit(code)
}

// ---------- 测试辅助 ----------

func doReq(method, path, token string, body any) (int, map[string]any) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, testServer.URL+path, rdr)
	if err != nil {
		panic(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	m := map[string]any{}
	_ = json.Unmarshal(data, &m)
	return resp.StatusCode, m
}

func resetDB(t *testing.T) {
	t.Helper()
	_, err := testPool.Exec(context.Background(), `
		TRUNCATE payments, relocations, occupancy_intervals,
		         contract_contacts, contracts, contacts, niches
		RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("reset db: %v", err)
	}
}

func addNiche(t *testing.T, code string, priceCents int64) int64 {
	t.Helper()
	var id int64
	err := testPool.QueryRow(context.Background(), `
		INSERT INTO niches (code, zone, level, price_per_year_cents)
		VALUES ($1, 'T', 1, $2) RETURNING id`, code, priceCents).Scan(&id)
	if err != nil {
		t.Fatalf("add niche: %v", err)
	}
	return id
}

func mustStatus(t *testing.T, got, want int, resp map[string]any) {
	t.Helper()
	if got != want {
		t.Fatalf("status = %d, want %d, body = %v", got, want, resp)
	}
}

// createContract 走 API 登记合同（含一名 full 权限联系人），
// 返回 (contractID, linkID, contactID)。
func createContract(t *testing.T, token string, nicheID int64) (int64, int64, int64) {
	t.Helper()
	start := time.Now().UTC().Truncate(time.Second)
	st, m := doReq("POST", "/api/v1/contracts", token, map[string]any{
		"niche_id":           nicheID,
		"deceased_name":      "测试逝者",
		"deceased_id_number": "110101194901011234",
		"start_at":           start,
		"end_at":             start.AddDate(10, 0, 0),
		"contacts": []map[string]any{{
			"name": "测试家属", "id_number": "110101196001011234",
			"phone": "13800000000", "relation": "子", "auth_level": "full",
		}},
	})
	mustStatus(t, st, http.StatusCreated, m)
	contacts, _ := m["contacts"].([]any)
	if len(contacts) == 0 {
		t.Fatalf("contract created without contacts: %v", m)
	}
	link, _ := contacts[0].(map[string]any)
	return int64(m["id"].(float64)),
		int64(link["link_id"].(float64)),
		int64(link["contact_id"].(float64))
}

func verifyContact(t *testing.T, linkID int64) {
	t.Helper()
	st, m := doReq("POST", fmt.Sprintf("/api/v1/contract-contacts/%d/verify", linkID), tokReviewer, nil)
	mustStatus(t, st, http.StatusOK, m)
}

// activateContract 核实联系人并复核通过，使合同生效。
func activateContract(t *testing.T, contractID, linkID int64) {
	t.Helper()
	verifyContact(t, linkID)
	st, m := doReq("POST", fmt.Sprintf("/api/v1/contracts/%d/review", contractID), tokReviewer,
		map[string]any{"approve": true})
	mustStatus(t, st, http.StatusOK, m)
}

// payAllUnpaid 用模拟回执缴清合同名下全部未付账单。
func payAllUnpaid(t *testing.T, contractID int64) {
	t.Helper()
	st, m := doReq("GET", fmt.Sprintf("/api/v1/contracts/%d/payments", contractID), tokFinance, nil)
	mustStatus(t, st, http.StatusOK, m)
	for _, item := range m["payments"].([]any) {
		p := item.(map[string]any)
		if p["status"] == "unpaid" {
			pid := int64(p["id"].(float64))
			st, rm := doReq("POST", fmt.Sprintf("/api/v1/payments/%d/mock-receipt", pid), tokFinance, nil)
			mustStatus(t, st, http.StatusOK, rm)
		}
	}
}

func activeOccupancyCount(t *testing.T, nicheID int64) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM occupancy_intervals WHERE niche_id = $1 AND status = 'active'`,
		nicheID).Scan(&n); err != nil {
		t.Fatalf("count occupancy: %v", err)
	}
	return n
}

func contractState(t *testing.T, contractID int64) (status string, nicheID int64) {
	t.Helper()
	err := testPool.QueryRow(context.Background(),
		`SELECT status, niche_id FROM contracts WHERE id = $1`, contractID).Scan(&status, &nicheID)
	if err != nil {
		t.Fatalf("contract state: %v", err)
	}
	return
}

func relocationStatus(t *testing.T, relocationID int64) string {
	t.Helper()
	var s string
	if err := testPool.QueryRow(context.Background(),
		`SELECT status FROM relocations WHERE id = $1`, relocationID).Scan(&s); err != nil {
		t.Fatalf("relocation status: %v", err)
	}
	return s
}

// ---------- 并发场景 ----------

// 两个窗口同时复核通过同一格位、同一期间的两份合同：排他约束只放行一个。
func TestConcurrentActivationSameNiche(t *testing.T) {
	resetDB(t)
	niche := addNiche(t, "T-C1-001", 100000)
	c1, l1, _ := createContract(t, tokClerk1, niche)
	c2, l2, _ := createContract(t, tokClerk2, niche)
	verifyContact(t, l1)
	verifyContact(t, l2)

	var wg sync.WaitGroup
	results := make(chan int, 2)
	for _, cid := range []int64{c1, c2} {
		wg.Add(1)
		go func(cid int64) {
			defer wg.Done()
			st, _ := doReq("POST", fmt.Sprintf("/api/v1/contracts/%d/review", cid), tokReviewer,
				map[string]any{"approve": true})
			results <- st
		}(cid)
	}
	wg.Wait()
	close(results)

	var got []int
	for s := range results {
		got = append(got, s)
	}
	sort.Ints(got)
	if len(got) != 2 || got[0] != http.StatusOK || got[1] != http.StatusConflict {
		t.Fatalf("concurrent activation statuses = %v, want [200 409]", got)
	}
	if n := activeOccupancyCount(t, niche); n != 1 {
		t.Fatalf("active occupancies on niche = %d, want 1", n)
	}
	var activeContracts int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM contracts WHERE niche_id = $1 AND status = 'active'`,
		niche).Scan(&activeContracts); err != nil {
		t.Fatal(err)
	}
	if activeContracts != 1 {
		t.Fatalf("active contracts on niche = %d, want 1", activeContracts)
	}
}

// 同一账单并发收到多次模拟回执：只支付一次，回执号一致，全部请求幂等成功。
func TestDuplicateMockReceiptConcurrent(t *testing.T) {
	resetDB(t)
	niche := addNiche(t, "T-P1-001", 100000)
	cid, link, _ := createContract(t, tokClerk1, niche)
	activateContract(t, cid, link) // 生效时自动生成首年账单

	var paymentID int64
	if err := testPool.QueryRow(context.Background(), `
		SELECT id FROM payments WHERE contract_id = $1 AND status = 'unpaid'`, cid).Scan(&paymentID); err != nil {
		t.Fatalf("find unpaid payment: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	type res struct {
		status  int
		receipt string
	}
	results := make(chan res, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, m := doReq("POST", fmt.Sprintf("/api/v1/payments/%d/mock-receipt", paymentID), tokFinance, nil)
			r, _ := m["receipt_no"].(string)
			results <- res{st, r}
		}()
	}
	wg.Wait()
	close(results)

	var receipt string
	for r := range results {
		if r.status != http.StatusOK {
			t.Fatalf("mock receipt status = %d, want 200", r.status)
		}
		if receipt == "" {
			receipt = r.receipt
		} else if r.receipt != receipt {
			t.Fatalf("duplicate receipts produced different receipt_no: %q vs %q", r.receipt, receipt)
		}
	}
	var status string
	var count int
	if err := testPool.QueryRow(context.Background(), `
		SELECT status, count(*) OVER () FROM payments WHERE id = $1 GROUP BY status`,
		paymentID).Scan(&status, &count); err != nil {
		t.Fatal(err)
	}
	if status != "paid" || count != 1 {
		t.Fatalf("payment status=%s rows=%d, want paid/1", status, count)
	}
}

// 请求体中的已付款布尔值不得放行缴费。
func TestPaidBooleanRejected(t *testing.T) {
	resetDB(t)
	niche := addNiche(t, "T-P2-001", 100000)
	cid, _, _ := createContract(t, tokClerk1, niche)

	st, m := doReq("POST", fmt.Sprintf("/api/v1/contracts/%d/payments", cid), tokClerk1,
		map[string]any{"kind": "annual_fee", "amount_cents": 100, "paid": true})
	mustStatus(t, st, http.StatusBadRequest, m)

	st, m = doReq("POST", fmt.Sprintf("/api/v1/contracts/%d/payments", cid), tokClerk1,
		map[string]any{"kind": "annual_fee", "amount_cents": 100, "status": "paid"})
	mustStatus(t, st, http.StatusBadRequest, m)

	st, m = doReq("POST", fmt.Sprintf("/api/v1/contracts/%d/payments", cid), tokClerk1,
		map[string]any{"kind": "annual_fee", "amount_cents": 100})
	mustStatus(t, st, http.StatusCreated, m)
	if m["status"] != "unpaid" {
		t.Fatalf("payment status = %v, want unpaid (request booleans must not leak through)", m["status"])
	}
}

// 复核未完成：合同不得生效，不能迁出、不能迁位，格位仍为空位。
func TestReviewRequiredBeforeEffective(t *testing.T) {
	resetDB(t)
	niche := addNiche(t, "T-R1-001", 100000)
	target := addNiche(t, "T-R1-002", 100000)
	cid, _, contactID := createContract(t, tokClerk1, niche)

	st, m := doReq("POST", fmt.Sprintf("/api/v1/contracts/%d/checkout", cid), tokClerk1,
		map[string]any{"contact_id": contactID})
	mustStatus(t, st, http.StatusConflict, m)

	st, m = doReq("POST", fmt.Sprintf("/api/v1/contracts/%d/relocations", cid), tokClerk1,
		map[string]any{"to_niche_id": target, "contact_id": contactID})
	mustStatus(t, st, http.StatusConflict, m)

	st, m = doReq("GET", "/api/v1/niches/available", tokClerk1, nil)
	mustStatus(t, st, http.StatusOK, m)
	for _, item := range m["niches"].([]any) {
		if int64(item.(map[string]any)["id"].(float64)) == niche {
			return // 格位仍为空位，符合预期
		}
	}
	t.Fatalf("niche %d not listed as available while contract is pending_review", niche)
}

// 欠费提交迁位：保留草案，不产生半完成迁出；缴清后同一草案可提交成功。
func TestRelocationArrearsKeepsDraft(t *testing.T) {
	resetDB(t)
	n1 := addNiche(t, "T-M1-001", 100000)
	n2 := addNiche(t, "T-M1-002", 100000)
	cid, link, contactID := createContract(t, tokClerk1, n1)
	activateContract(t, cid, link) // 首年账单未付 → 欠费状态

	st, m := doReq("POST", fmt.Sprintf("/api/v1/contracts/%d/relocations", cid), tokClerk1,
		map[string]any{"to_niche_id": n2, "contact_id": contactID})
	mustStatus(t, st, http.StatusCreated, m)
	rid := int64(m["id"].(float64))
	if m["status"] != "draft" {
		t.Fatalf("relocation status = %v, want draft", m["status"])
	}

	// 欠费提交：409，草案保留，原占位不受影响
	st, m = doReq("POST", fmt.Sprintf("/api/v1/relocations/%d/commit", rid), tokClerk1, nil)
	mustStatus(t, st, http.StatusConflict, m)
	if m["code"] != "outstanding_fees" {
		t.Fatalf("error code = %v, want outstanding_fees", m["code"])
	}
	if s := relocationStatus(t, rid); s != "draft" {
		t.Fatalf("relocation status after arrears commit = %s, want draft", s)
	}
	if status, nid := contractState(t, cid); status != "active" || nid != n1 {
		t.Fatalf("contract = (%s, niche %d), want (active, %d)", status, nid, n1)
	}
	if n := activeOccupancyCount(t, n1); n != 1 {
		t.Fatalf("active occupancies on original niche = %d, want 1 (no half-done checkout)", n)
	}

	// 缴清全部费用（首年费 + 迁位手续费）后提交成功
	payAllUnpaid(t, cid)
	st, m = doReq("POST", fmt.Sprintf("/api/v1/relocations/%d/commit", rid), tokClerk1, nil)
	mustStatus(t, st, http.StatusOK, m)
	if m["status"] != "committed" {
		t.Fatalf("relocation status = %v, want committed", m["status"])
	}
	if status, nid := contractState(t, cid); status != "active" || nid != n2 {
		t.Fatalf("contract = (%s, niche %d), want (active, %d)", status, nid, n2)
	}
	if n := activeOccupancyCount(t, n1); n != 0 {
		t.Fatalf("old niche still has %d active occupancies, want 0", n)
	}
	if n := activeOccupancyCount(t, n2); n != 1 {
		t.Fatalf("new niche has %d active occupancies, want 1", n)
	}
}

// 目标格位被占用时迁位失败：草案置 failed，原合同与原位置仍然有效。
func TestRelocationConflictKeepsOriginal(t *testing.T) {
	resetDB(t)
	n1 := addNiche(t, "T-M2-001", 100000)
	n2 := addNiche(t, "T-M2-002", 100000)
	cA, linkA, contactA := createContract(t, tokClerk1, n1)
	activateContract(t, cA, linkA)
	cB, linkB, _ := createContract(t, tokClerk2, n2)
	activateContract(t, cB, linkB) // B 占用目标格位 n2

	st, m := doReq("POST", fmt.Sprintf("/api/v1/contracts/%d/relocations", cA), tokClerk1,
		map[string]any{"to_niche_id": n2, "contact_id": contactA})
	mustStatus(t, st, http.StatusCreated, m)
	rid := int64(m["id"].(float64))
	payAllUnpaid(t, cA)

	st, m = doReq("POST", fmt.Sprintf("/api/v1/relocations/%d/commit", rid), tokClerk1, nil)
	mustStatus(t, st, http.StatusConflict, m)
	if m["code"] != "niche_unavailable" {
		t.Fatalf("error code = %v, want niche_unavailable", m["code"])
	}
	if s := relocationStatus(t, rid); s != "failed" {
		t.Fatalf("relocation status = %s, want failed", s)
	}
	// 事务回滚：原合同与原位置仍然有效
	if status, nid := contractState(t, cA); status != "active" || nid != n1 {
		t.Fatalf("contract A = (%s, niche %d), want (active, %d)", status, nid, n1)
	}
	if n := activeOccupancyCount(t, n1); n != 1 {
		t.Fatalf("original niche active occupancies = %d, want 1", n)
	}
	if n := activeOccupancyCount(t, n2); n != 1 {
		t.Fatalf("target niche active occupancies = %d, want 1 (B unaffected)", n)
	}
}

// 两份合同并发迁往同一空位：一个成功一个失败，失败方原占位完好。
func TestConcurrentRelocationsSameTarget(t *testing.T) {
	resetDB(t)
	n1 := addNiche(t, "T-M3-001", 100000)
	n2 := addNiche(t, "T-M3-002", 100000)
	n3 := addNiche(t, "T-M3-003", 100000)
	cA, linkA, contactA := createContract(t, tokClerk1, n1)
	activateContract(t, cA, linkA)
	cB, linkB, contactB := createContract(t, tokClerk2, n2)
	activateContract(t, cB, linkB)

	st, m := doReq("POST", fmt.Sprintf("/api/v1/contracts/%d/relocations", cA), tokClerk1,
		map[string]any{"to_niche_id": n3, "contact_id": contactA})
	mustStatus(t, st, http.StatusCreated, m)
	rA := int64(m["id"].(float64))
	st, m = doReq("POST", fmt.Sprintf("/api/v1/contracts/%d/relocations", cB), tokClerk2,
		map[string]any{"to_niche_id": n3, "contact_id": contactB})
	mustStatus(t, st, http.StatusCreated, m)
	rB := int64(m["id"].(float64))
	payAllUnpaid(t, cA)
	payAllUnpaid(t, cB)

	var wg sync.WaitGroup
	results := make(chan int, 2)
	for _, rid := range []int64{rA, rB} {
		wg.Add(1)
		go func(rid int64) {
			defer wg.Done()
			st, _ := doReq("POST", fmt.Sprintf("/api/v1/relocations/%d/commit", rid), tokClerk1, nil)
			results <- st
		}(rid)
	}
	wg.Wait()
	close(results)

	var got []int
	for s := range results {
		got = append(got, s)
	}
	sort.Ints(got)
	if len(got) != 2 || got[0] != http.StatusOK || got[1] != http.StatusConflict {
		t.Fatalf("concurrent relocation commits = %v, want [200 409]", got)
	}
	if n := activeOccupancyCount(t, n3); n != 1 {
		t.Fatalf("target niche active occupancies = %d, want 1", n)
	}
	// 失败方：草案 failed，原合同与原位置仍然有效
	failR, winR := rA, rB
	if relocationStatus(t, rA) == "committed" {
		failR, winR = rB, rA
	}
	if s := relocationStatus(t, failR); s != "failed" {
		t.Fatalf("loser relocation status = %s, want failed", s)
	}
	if s := relocationStatus(t, winR); s != "committed" {
		t.Fatalf("winner relocation status = %s, want committed", s)
	}
	loserContract, loserNiche := cA, n1
	if failR == rB {
		loserContract, loserNiche = cB, n2
	}
	if status, nid := contractState(t, loserContract); status != "active" || nid != loserNiche {
		t.Fatalf("loser contract = (%s, niche %d), want (active, %d)", status, nid, loserNiche)
	}
	if n := activeOccupancyCount(t, loserNiche); n != 1 {
		t.Fatalf("loser original niche active occupancies = %d, want 1", n)
	}
}

// 迁出规则：欠费不可迁出；未核实联系人不可迁出；合规迁出后占位结束。
func TestCheckoutRules(t *testing.T) {
	resetDB(t)
	n1 := addNiche(t, "T-O1-001", 100000)
	cid, link, contactID := createContract(t, tokClerk1, n1)
	activateContract(t, cid, link)

	// 欠费 → 409，占位保留
	st, m := doReq("POST", fmt.Sprintf("/api/v1/contracts/%d/checkout", cid), tokClerk1,
		map[string]any{"contact_id": contactID})
	mustStatus(t, st, http.StatusConflict, m)
	if m["code"] != "outstanding_fees" {
		t.Fatalf("code = %v, want outstanding_fees", m["code"])
	}
	if n := activeOccupancyCount(t, n1); n != 1 {
		t.Fatalf("occupancy after arrears checkout = %d, want 1", n)
	}

	payAllUnpaid(t, cid)

	// 未核实/无权限联系人 → 409
	st, m = doReq("POST", fmt.Sprintf("/api/v1/contracts/%d/checkout", cid), tokClerk1,
		map[string]any{"contact_id": 999999})
	mustStatus(t, st, http.StatusConflict, m)
	if m["code"] != "contact_not_verified" {
		t.Fatalf("code = %v, want contact_not_verified", m["code"])
	}

	// 合规迁出
	st, m = doReq("POST", fmt.Sprintf("/api/v1/contracts/%d/checkout", cid), tokClerk1,
		map[string]any{"contact_id": contactID})
	mustStatus(t, st, http.StatusOK, m)
	if m["status"] != "ended" {
		t.Fatalf("contract status = %v, want ended", m["status"])
	}
	if n := activeOccupancyCount(t, n1); n != 0 {
		t.Fatalf("active occupancies after checkout = %d, want 0", n)
	}
}

// 角色限制：敏感查询与操作按办理角色放行/拒绝。
func TestRoleEnforcement(t *testing.T) {
	resetDB(t)
	niche := addNiche(t, "T-A1-001", 100000)
	cid, _, _ := createContract(t, tokClerk1, niche)

	st, _ := doReq("GET", "/api/v1/niches/available", "", nil)
	mustStatus(t, st, http.StatusUnauthorized, nil)

	st, m := doReq("POST", "/api/v1/contracts", tokFinance, map[string]any{
		"niche_id": niche, "deceased_name": "x", "deceased_id_number": "1",
		"start_at": time.Now(), "end_at": time.Now().AddDate(1, 0, 0),
	})
	mustStatus(t, st, http.StatusForbidden, m)

	st, m = doReq("GET", fmt.Sprintf("/api/v1/contracts/%d/payments", cid), tokReviewer, nil)
	mustStatus(t, st, http.StatusForbidden, m)

	st, m = doReq("GET", "/api/v1/contacts/1", tokFinance, nil)
	mustStatus(t, st, http.StatusForbidden, m)

	st, m = doReq("POST", "/api/v1/payments/1/mock-receipt", tokClerk1, nil)
	mustStatus(t, st, http.StatusForbidden, m)
}

// 证件脱敏：窗口角色看到的证件号必须打码，复核岗可见完整号码用于核对。
func TestMaskingByRole(t *testing.T) {
	resetDB(t)
	niche := addNiche(t, "T-S1-001", 100000)
	cid, _, _ := createContract(t, tokClerk1, niche)
	const fullID = "110101194901011234"

	st, m := doReq("GET", fmt.Sprintf("/api/v1/contracts/%d", cid), tokClerk1, nil)
	mustStatus(t, st, http.StatusOK, m)
	got := m["deceased_id_number"].(string)
	if got == fullID {
		t.Fatalf("clerk must not see full id number, got %q", got)
	}
	if got != "1101************34" {
		t.Fatalf("masked id = %q, want 1101************34", got)
	}

	st, m = doReq("GET", fmt.Sprintf("/api/v1/contracts/%d", cid), tokReviewer, nil)
	mustStatus(t, st, http.StatusOK, m)
	if m["deceased_id_number"] != fullID {
		t.Fatalf("reviewer should see full id for verification, got %v", m["deceased_id_number"])
	}
}

// 日志脱敏：访问日志中不得出现完整证件号。
func TestAccessLogRedaction(t *testing.T) {
	resetDB(t)
	var buf bytes.Buffer
	router := httpapi.NewRouter(service.New(testPool), config.Load().Tokens, &buf)
	srv := httptest.NewServer(router)
	defer srv.Close()

	req, _ := http.NewRequest("GET",
		srv.URL+"/api/v1/niches/available?ref=110101194901011234", nil)
	req.Header.Set("Authorization", "Bearer "+tokClerk1)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	logLine := buf.String()
	if bytes.Contains(buf.Bytes(), []byte("110101194901011234")) {
		t.Fatalf("access log leaks full id number: %s", logLine)
	}
	if logLine == "" {
		t.Fatal("expected access log line")
	}
}

// 种子数据可重复应用且内容正确。
func TestSeedDataApplies(t *testing.T) {
	resetDB(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ { // 应用两次，验证幂等
		if err := db.ApplySQLDir(ctx, testPool, assets.Seed, "seed"); err != nil {
			t.Fatalf("apply seed (run %d): %v", i+1, err)
		}
	}
	var niches int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM niches`).Scan(&niches); err != nil {
		t.Fatal(err)
	}
	if niches != 10 {
		t.Fatalf("seeded niches = %d, want 10", niches)
	}
	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM contracts WHERE id = 100`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("seeded contract 100 status = %s, want active", status)
	}
	if n := activeOccupancyCount(t, 1); n != 1 {
		t.Fatalf("seeded occupancy on niche 1 = %d, want 1", n)
	}
}
