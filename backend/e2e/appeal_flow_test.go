package e2e

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lp/campus-market/internal/config"
	"github.com/lp/campus-market/internal/constants"
	"github.com/lp/campus-market/internal/model"
	"github.com/lp/campus-market/internal/router"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func setupEngine(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	// File-backed WAL database with a real connection pool so concurrent
	// HTTP requests contend on locks like they would against MySQL (unlike
	// :memory: with MaxOpenConns=1, which would serialize everything).
	dsn := "file:" + t.TempDir() + "/e2e.db?_busy_timeout=10000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(10)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&model.User{}, &model.Product{}, &model.Conversation{}, &model.Message{},
		&model.TradeOrder{}, &model.Review{}, &model.BookExchange{}, &model.ReviewAppeal{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("123456"), bcrypt.DefaultCost)
	adminHash, _ := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.DefaultCost)
	users := []model.User{
		{Phone: "13700000001", PasswordHash: string(hash), Nickname: "买家小明", Role: constants.UserRoleStudent, Campus: "东校区", CreditScore: 100},
		{Phone: "13700000002", PasswordHash: string(hash), Nickname: "卖家阿珍", Role: constants.UserRoleStudent, Campus: "西校区", CreditScore: 120},
		{Phone: "13700000003", PasswordHash: string(hash), Nickname: "路人达人", Role: constants.UserRoleStudent, Campus: "南校区", CreditScore: 90},
		{Phone: "13800000001", PasswordHash: string(adminHash), Nickname: "平台管理员", Role: constants.UserRoleAdmin, Campus: "东校区", CreditScore: 300},
		// Edge accounts for floor/ceiling rollback tests (ids 5 and 6).
		{Phone: "13700000005", PasswordHash: string(hash), Nickname: "触底卖家", Role: constants.UserRoleStudent, Campus: "东校区", CreditScore: 4},
		{Phone: "13700000006", PasswordHash: string(hash), Nickname: "触顶卖家", Role: constants.UserRoleStudent, Campus: "东校区", CreditScore: 298},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatalf("seed users: %v", err)
	}
	products := []model.Product{
		{SellerID: 2, Title: "差评场景商品", Price: 100, Category: constants.ProductCategoryBooks, Condition: "九成新", Campus: "西校区", TradeLocation: "三食堂", Status: constants.ProductStatusSold},
		{SellerID: 2, Title: "好评场景商品", Price: 50, Category: constants.ProductCategoryBooks, Condition: "全新", Campus: "西校区", TradeLocation: "三食堂", Status: constants.ProductStatusSold},
		{SellerID: 5, Title: "触底卖家的商品", Price: 30, Category: constants.ProductCategoryBooks, Condition: "九成新", Campus: "东校区", TradeLocation: "东门", Status: constants.ProductStatusSold},
		{SellerID: 6, Title: "触顶卖家的商品", Price: 30, Category: constants.ProductCategoryBooks, Condition: "九成新", Campus: "东校区", TradeLocation: "东门", Status: constants.ProductStatusSold},
	}
	if err := db.Create(&products).Error; err != nil {
		t.Fatalf("seed products: %v", err)
	}
	orders := []model.TradeOrder{
		{ProductID: 1, BuyerID: 1, SellerID: 2, Status: constants.TradeStatusCompleted},
		{ProductID: 2, BuyerID: 1, SellerID: 2, Status: constants.TradeStatusCompleted},
		{ProductID: 3, BuyerID: 1, SellerID: 5, Status: constants.TradeStatusCompleted},
		{ProductID: 4, BuyerID: 1, SellerID: 6, Status: constants.TradeStatusCompleted},
	}
	if err := db.Create(&orders).Error; err != nil {
		t.Fatalf("seed orders: %v", err)
	}
	cfg := &config.Config{
		Port: "8080", JWTSecret: "e2e-secret", JWTExpireHours: 12,
		RateLimitPerMin: 10000, LoginRateLimit: 10000,
		CORSOrigins:    []string{"*"},
		SeedingEnabled: false,
	}
	gin.SetMode(gin.TestMode)
	return router.New(cfg, db, slog.Default()), db
}

func doJSON(t *testing.T, r *gin.Engine, method, path, token string, body interface{}) (int, envelope) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		raw, _ := json.Marshal(body)
		buf.Write(raw)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var env envelope
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &env)
	}
	return w.Code, env
}

func login(t *testing.T, r *gin.Engine, phone, password string) string {
	t.Helper()
	status, env := doJSON(t, r, http.MethodPost, "/api/v1/users/login", "", map[string]string{"phone": phone, "password": password})
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("login %s failed: status=%d env=%+v", phone, status, env)
	}
	var data struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(env.Data, &data)
	if data.Token == "" {
		t.Fatalf("empty token for %s", phone)
	}
	return data.Token
}

func creditOf(t *testing.T, db *gorm.DB, userID uint) int {
	t.Helper()
	var u model.User
	if err := db.First(&u, userID).Error; err != nil {
		t.Fatalf("load user %d: %v", userID, err)
	}
	return u.CreditScore
}

func mustCode(t *testing.T, status int, env envelope, wantStatus, wantCode int, ctx string) {
	t.Helper()
	if status != wantStatus || env.Code != wantCode {
		t.Fatalf("%s: want http=%d code=%d, got http=%d code=%d msg=%s", ctx, wantStatus, wantCode, status, env.Code, env.Message)
	}
}

// TestAppealFlowApproved verifies: bad review -10 credit, single appeal by
// receiver only, progress query, admin approval rolls the credit back.
func TestAppealFlowApproved(t *testing.T) {
	r, db := setupEngine(t)
	buyer := login(t, r, "13700000001", "123456")
	seller := login(t, r, "13700000002", "123456")
	outsider := login(t, r, "13700000003", "123456")
	admin := login(t, r, "13800000001", "admin123")

	if got := creditOf(t, db, 2); got != 120 {
		t.Fatalf("seed credit seller want 120, got %d", got)
	}

	// Buyer leaves a bad review on trade 1: seller credit 120 -> 110.
	status, env := doJSON(t, r, http.MethodPost, "/api/v1/reviews", buyer,
		map[string]interface{}{"trade_id": 1, "rating": "bad", "content": "交易体验很差"})
	mustCode(t, status, env, http.StatusOK, 0, "create bad review")
	if got := creditOf(t, db, 2); got != 110 {
		t.Fatalf("after bad review credit want 110, got %d", got)
	}
	var review struct {
		ID uint `json:"id"`
	}
	_ = json.Unmarshal(env.Data, &review)
	if review.ID == 0 {
		t.Fatalf("missing review id")
	}

	// The reviewer (buyer) must NOT be allowed to appeal (not the receiver).
	status, env = doJSON(t, r, http.MethodPost, "/api/v1/appeals", buyer,
		map[string]interface{}{"review_id": review.ID, "reason": "我是评价人不该能申诉"})
	mustCode(t, status, env, http.StatusForbidden, constants.CodeForbidden, "reviewer appeal forbidden")

	// An unrelated user cannot appeal either.
	status, env = doJSON(t, r, http.MethodPost, "/api/v1/appeals", outsider,
		map[string]interface{}{"review_id": review.ID, "reason": "路人也不该能申诉这个评价"})
	mustCode(t, status, env, http.StatusForbidden, constants.CodeForbidden, "outsider appeal forbidden")

	// Unauthenticated request rejected.
	status, env = doJSON(t, r, http.MethodPost, "/api/v1/appeals", "",
		map[string]interface{}{"review_id": review.ID, "reason": "未登录提交申诉"})
	mustCode(t, status, env, http.StatusUnauthorized, constants.CodeUnauthorized, "anonymous appeal")

	// Invalid payload (reason too short) -> validation error.
	status, env = doJSON(t, r, http.MethodPost, "/api/v1/appeals", seller,
		map[string]interface{}{"review_id": review.ID, "reason": "x"})
	if status != http.StatusBadRequest {
		t.Fatalf("validation want 400, got %d (%s)", status, env.Message)
	}

	// The review receiver (seller) submits the one-and-only appeal.
	status, env = doJSON(t, r, http.MethodPost, "/api/v1/appeals", seller,
		map[string]interface{}{"review_id": review.ID, "reason": "该差评与事实不符，买家无理由恶意评价，申请撤销信誉分扣减"})
	mustCode(t, status, env, http.StatusOK, 0, "seller submit appeal")
	var appeal struct {
		ID     uint   `json:"id"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(env.Data, &appeal)
	if appeal.Status != constants.AppealStatusPending {
		t.Fatalf("new appeal want pending, got %s", appeal.Status)
	}

	// Each review can only be appealed once.
	status, env = doJSON(t, r, http.MethodPost, "/api/v1/appeals", seller,
		map[string]interface{}{"review_id": review.ID, "reason": "重复申诉应该被拒绝才行"})
	mustCode(t, status, env, http.StatusConflict, constants.CodeConflict, "duplicate appeal")

	// Progress query: owner sees pending; outsider is forbidden.
	status, env = doJSON(t, r, http.MethodGet, "/api/v1/appeals/"+itoa(appeal.ID), seller, nil)
	mustCode(t, status, env, http.StatusOK, 0, "owner progress")
	var progress struct {
		Status         string `json:"status"`
		CreditReversed bool   `json:"credit_reversed"`
	}
	_ = json.Unmarshal(env.Data, &progress)
	if progress.Status != "pending" || progress.CreditReversed {
		t.Fatalf("progress want pending/not reversed, got %+v", progress)
	}
	status, _ = doJSON(t, r, http.MethodGet, "/api/v1/appeals/"+itoa(appeal.ID), outsider, nil)
	if status != http.StatusForbidden {
		t.Fatalf("outsider progress want 403, got %d", status)
	}
	status, env = doJSON(t, r, http.MethodGet, "/api/v1/appeals/me", seller, nil)
	mustCode(t, status, env, http.StatusOK, 0, "list my appeals")
	var mine []map[string]interface{}
	_ = json.Unmarshal(env.Data, &mine)
	if len(mine) != 1 {
		t.Fatalf("my appeals want 1, got %d", len(mine))
	}

	// A student cannot access the admin queue.
	status, _ = doJSON(t, r, http.MethodGet, "/api/v1/admin/appeals?status=pending", seller, nil)
	if status != http.StatusForbidden {
		t.Fatalf("student admin list want 403, got %d", status)
	}

	// Invalid admin action rejected without touching credit.
	status, env = doJSON(t, r, http.MethodPost, "/api/v1/admin/appeals/"+itoa(appeal.ID)+"/review", admin,
		map[string]interface{}{"action": "maybe"})
	if status != http.StatusBadRequest {
		t.Fatalf("invalid action want 400, got %d", status)
	}
	if got := creditOf(t, db, 2); got != 110 {
		t.Fatalf("credit must stay 110 after failed action, got %d", got)
	}

	// Admin approves: the -10 must be rolled back, 110 -> 120.
	status, env = doJSON(t, r, http.MethodPost, "/api/v1/admin/appeals/"+itoa(appeal.ID)+"/review", admin,
		map[string]interface{}{"action": "approve", "comment": "差评证据不足，撤销信誉分扣减"})
	mustCode(t, status, env, http.StatusOK, 0, "admin approve")
	var decision struct {
		Status         string `json:"status"`
		CreditReversed bool   `json:"credit_reversed"`
		CreditDelta    int    `json:"credit_delta"`
	}
	_ = json.Unmarshal(env.Data, &decision)
	if decision.Status != "approved" || !decision.CreditReversed || decision.CreditDelta != 10 {
		t.Fatalf("approval decision mismatch: %+v", decision)
	}
	if got := creditOf(t, db, 2); got != 120 {
		t.Fatalf("after approval credit want 120, got %d", got)
	}

	// Decided appeals cannot be reviewed a second time.
	status, env = doJSON(t, r, http.MethodPost, "/api/v1/admin/appeals/"+itoa(appeal.ID)+"/review", admin,
		map[string]interface{}{"action": "reject"})
	mustCode(t, status, env, http.StatusConflict, constants.CodeConflict, "double review")

	// The student now sees the final progress.
	status, env = doJSON(t, r, http.MethodGet, "/api/v1/appeals/"+itoa(appeal.ID), seller, nil)
	mustCode(t, status, env, http.StatusOK, 0, "final progress")
	_ = json.Unmarshal(env.Data, &progress)
	if progress.Status != "approved" || !progress.CreditReversed {
		t.Fatalf("final progress want approved/reversed, got %+v", progress)
	}

	// Admin queue only keeps the appeal under approved now.
	status, env = doJSON(t, r, http.MethodGet, "/api/v1/admin/appeals", admin, nil)
	mustCode(t, status, env, http.StatusOK, 0, "admin list all")
	var all []map[string]interface{}
	_ = json.Unmarshal(env.Data, &all)
	if len(all) != 1 || all[0]["status"] != "approved" {
		t.Fatalf("admin list mismatch: %v", all)
	}
}

// TestAppealFlowRejected verifies that rejecting an appeal leaves the credit
// change from the original review untouched.
func TestAppealFlowRejected(t *testing.T) {
	r, db := setupEngine(t)
	buyer := login(t, r, "13700000001", "123456")
	seller := login(t, r, "13700000002", "123456")
	admin := login(t, r, "13800000001", "admin123")

	// Good review on trade 2: seller credit 120 -> 125.
	status, env := doJSON(t, r, http.MethodPost, "/api/v1/reviews", buyer,
		map[string]interface{}{"trade_id": 2, "rating": "good", "content": "好评"})
	mustCode(t, status, env, http.StatusOK, 0, "create good review")
	if got := creditOf(t, db, 2); got != 125 {
		t.Fatalf("after good review credit want 125, got %d", got)
	}
	var review struct {
		ID uint `json:"id"`
	}
	_ = json.Unmarshal(env.Data, &review)

	status, env = doJSON(t, r, http.MethodPost, "/api/v1/appeals", seller,
		map[string]interface{}{"review_id": review.ID, "reason": "好评是刷的我要申诉撤销掉这个加分"})
	mustCode(t, status, env, http.StatusOK, 0, "seller appeal good review")
	var appeal struct {
		ID uint `json:"id"`
	}
	_ = json.Unmarshal(env.Data, &appeal)

	status, env = doJSON(t, r, http.MethodPost, "/api/v1/admin/appeals/"+itoa(appeal.ID)+"/review", admin,
		map[string]interface{}{"action": "reject", "comment": "评价真实有效，驳回申诉"})
	mustCode(t, status, env, http.StatusOK, 0, "admin reject")
	var decision struct {
		Status         string `json:"status"`
		CreditReversed bool   `json:"credit_reversed"`
		CreditDelta    int    `json:"credit_delta"`
	}
	_ = json.Unmarshal(env.Data, &decision)
	if decision.Status != "rejected" || decision.CreditReversed || decision.CreditDelta != 0 {
		t.Fatalf("rejection decision mismatch: %+v", decision)
	}
	if got := creditOf(t, db, 2); got != 125 {
		t.Fatalf("after rejection credit must stay 125, got %d", got)
	}
}

// TestAppealConcurrentSubmit fires 10 simultaneous POST /appeals for one
// review and requires exactly one 200, the other nine business conflicts, and
// a single appeal row in the database.
func TestAppealConcurrentSubmit(t *testing.T) {
	r, db := setupEngine(t)
	buyer := login(t, r, "13700000001", "123456")
	seller := login(t, r, "13700000002", "123456")

	// Trade 1 has a completed order but no review yet -> create one review first.
	status, env := doJSON(t, r, http.MethodPost, "/api/v1/reviews", buyer,
		map[string]interface{}{"trade_id": 1, "rating": "bad", "content": "并发提交申诉前置差评"})
	mustCode(t, status, env, http.StatusOK, 0, "create review")
	var review struct {
		ID uint `json:"id"`
	}
	_ = json.Unmarshal(env.Data, &review)

	const n = 10
	var wg sync.WaitGroup
	codes := make([]int, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			body, _ := json.Marshal(map[string]interface{}{
				"review_id": review.ID, "reason": "并发重复提交的申诉理由必须只有一条成功",
			})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/appeals", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+seller)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			codes[idx] = w.Code
		}(i)
	}
	close(start)
	wg.Wait()

	var ok, conflict int
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		default:
			t.Fatalf("unexpected http code %d", c)
		}
	}
	if ok != 1 || conflict != n-1 {
		t.Fatalf("concurrent submit: want 1 ok / %d conflict, got %d / %d", n-1, ok, conflict)
	}
	var count int64
	db.Model(&model.ReviewAppeal{}).Where("review_id = ?", review.ID).Count(&count)
	if count != 1 {
		t.Fatalf("exactly one appeal row expected, got %d", count)
	}
}

// TestAppealConcurrentReview fires 10 simultaneous admin approvals for one
// pending appeal and requires exactly one to take effect, with the credit
// rollback applied exactly once.
func TestAppealConcurrentReview(t *testing.T) {
	r, db := setupEngine(t)
	buyer := login(t, r, "13700000001", "123456")
	seller := login(t, r, "13700000002", "123456")
	admin := login(t, r, "13800000001", "admin123")

	status, env := doJSON(t, r, http.MethodPost, "/api/v1/reviews", buyer,
		map[string]interface{}{"trade_id": 1, "rating": "bad", "content": "并发审核前置差评"})
	mustCode(t, status, env, http.StatusOK, 0, "create review")
	var review struct {
		ID uint `json:"id"`
	}
	_ = json.Unmarshal(env.Data, &review)
	if got := creditOf(t, db, 2); got != 110 {
		t.Fatalf("score after bad review want 110, got %d", got)
	}

	status, env = doJSON(t, r, http.MethodPost, "/api/v1/appeals", seller,
		map[string]interface{}{"review_id": review.ID, "reason": "并发审核的申诉理由需要足够长"})
	mustCode(t, status, env, http.StatusOK, 0, "submit appeal")
	var appeal struct {
		ID uint `json:"id"`
	}
	_ = json.Unmarshal(env.Data, &appeal)

	const n = 10
	var wg sync.WaitGroup
	codes := make([]int, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			body, _ := json.Marshal(map[string]interface{}{"action": "approve"})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/appeals/"+itoa(appeal.ID)+"/review", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+admin)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			codes[idx] = w.Code
		}(i)
	}
	close(start)
	wg.Wait()

	var ok, conflict int
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		default:
			t.Fatalf("unexpected http code %d", c)
		}
	}
	if ok != 1 || conflict != n-1 {
		t.Fatalf("concurrent review: want 1 ok / %d conflict, got %d / %d", n-1, ok, conflict)
	}
	// The +10 rollback must have happened exactly once: 110 -> 120.
	if got := creditOf(t, db, 2); got != 120 {
		t.Fatalf("rollback must apply once, score want 120, got %d", got)
	}
	var a model.ReviewAppeal
	db.First(&a, appeal.ID)
	if a.Status != constants.AppealStatusApproved || !a.CreditReversed || a.CreditDelta != 10 {
		t.Fatalf("unexpected appeal state: %+v", a)
	}
}

// TestAppealFloorCeilingRollback verifies that approval restores only the
// score change the review ACTUALLY caused after [0,300] clamping:
// at score 4 a bad review applies -4 (restore +4); at 298 a good review
// applies +2 (restore -2).
func TestAppealFloorCeilingRollback(t *testing.T) {
	r, db := setupEngine(t)
	buyer := login(t, r, "13700000001", "123456")
	floorSeller := login(t, r, "13700000005", "123456")
	ceilingSeller := login(t, r, "13700000006", "123456")
	admin := login(t, r, "13800000001", "admin123")

	// Floor: seller 5 starts at 4; bad review via the real endpoint clamps to 0.
	status, env := doJSON(t, r, http.MethodPost, "/api/v1/reviews", buyer,
		map[string]interface{}{"trade_id": 3, "rating": "bad", "content": "触底差评"})
	mustCode(t, status, env, http.StatusOK, 0, "floor bad review")
	var badReview struct {
		ID          uint `json:"id"`
		CreditDelta int  `json:"credit_delta"`
	}
	_ = json.Unmarshal(env.Data, &badReview)
	if badReview.CreditDelta != -4 {
		t.Fatalf("actual floor delta want -4, got %d", badReview.CreditDelta)
	}
	if got := creditOf(t, db, 5); got != 0 {
		t.Fatalf("floor score want 0, got %d", got)
	}
	status, env = doJSON(t, r, http.MethodPost, "/api/v1/appeals", floorSeller,
		map[string]interface{}{"review_id": badReview.ID, "reason": "触底差评申诉理由足够长"})
	mustCode(t, status, env, http.StatusOK, 0, "floor appeal submit")
	var floorAppeal struct {
		ID uint `json:"id"`
	}
	_ = json.Unmarshal(env.Data, &floorAppeal)
	status, env = doJSON(t, r, http.MethodPost, "/api/v1/admin/appeals/"+itoa(floorAppeal.ID)+"/review", admin,
		map[string]interface{}{"action": "approve"})
	mustCode(t, status, env, http.StatusOK, 0, "floor approve")
	var floorDecision struct {
		CreditDelta    int  `json:"credit_delta"`
		CreditReversed bool `json:"credit_reversed"`
	}
	_ = json.Unmarshal(env.Data, &floorDecision)
	if !floorDecision.CreditReversed || floorDecision.CreditDelta != 4 {
		t.Fatalf("floor rollback want +4/reversed, got %+v", floorDecision)
	}
	if got := creditOf(t, db, 5); got != 4 {
		t.Fatalf("floor score must restore to 4 (not 10), got %d", got)
	}

	// Ceiling: seller 6 starts at 298; good review clamps to 300 (actual +2).
	status, env = doJSON(t, r, http.MethodPost, "/api/v1/reviews", buyer,
		map[string]interface{}{"trade_id": 4, "rating": "good", "content": "触顶好评"})
	mustCode(t, status, env, http.StatusOK, 0, "ceiling good review")
	var goodReview struct {
		ID          uint `json:"id"`
		CreditDelta int  `json:"credit_delta"`
	}
	_ = json.Unmarshal(env.Data, &goodReview)
	if goodReview.CreditDelta != 2 {
		t.Fatalf("actual ceiling delta want +2, got %d", goodReview.CreditDelta)
	}
	if got := creditOf(t, db, 6); got != 300 {
		t.Fatalf("ceiling score want 300, got %d", got)
	}
	status, env = doJSON(t, r, http.MethodPost, "/api/v1/appeals", ceilingSeller,
		map[string]interface{}{"review_id": goodReview.ID, "reason": "触顶好评申诉理由足够长"})
	mustCode(t, status, env, http.StatusOK, 0, "ceiling appeal submit")
	var ceilingAppeal struct {
		ID uint `json:"id"`
	}
	_ = json.Unmarshal(env.Data, &ceilingAppeal)
	status, env = doJSON(t, r, http.MethodPost, "/api/v1/admin/appeals/"+itoa(ceilingAppeal.ID)+"/review", admin,
		map[string]interface{}{"action": "approve"})
	mustCode(t, status, env, http.StatusOK, 0, "ceiling approve")
	var ceilingDecision struct {
		CreditDelta    int  `json:"credit_delta"`
		CreditReversed bool `json:"credit_reversed"`
	}
	_ = json.Unmarshal(env.Data, &ceilingDecision)
	if !ceilingDecision.CreditReversed || ceilingDecision.CreditDelta != -2 {
		t.Fatalf("ceiling rollback want -2/reversed, got %+v", ceilingDecision)
	}
	if got := creditOf(t, db, 6); got != 298 {
		t.Fatalf("ceiling score must restore to 298 (not 295), got %d", got)
	}
}

// TestAppealBlankReasonHTTP rejects whitespace-only reasons over HTTP.
func TestAppealBlankReasonHTTP(t *testing.T) {
	r, _ := setupEngine(t)
	buyer := login(t, r, "13700000001", "123456")
	seller := login(t, r, "13700000002", "123456")

	status, env := doJSON(t, r, http.MethodPost, "/api/v1/reviews", buyer,
		map[string]interface{}{"trade_id": 1, "rating": "bad", "content": "空格理由前置差评"})
	mustCode(t, status, env, http.StatusOK, 0, "create review")
	var review struct {
		ID uint `json:"id"`
	}
	_ = json.Unmarshal(env.Data, &review)

	for _, reason := range []string{"   ", "\t\t", "\n \r\n"} {
		status, _ := doJSON(t, r, http.MethodPost, "/api/v1/appeals", seller,
			map[string]interface{}{"review_id": review.ID, "reason": reason})
		if status != http.StatusBadRequest {
			t.Fatalf("blank reason %q want 400, got %d", reason, status)
		}
	}
	// A real reason after trimming still works.
	status, env = doJSON(t, r, http.MethodPost, "/api/v1/appeals", seller,
		map[string]interface{}{"review_id": review.ID, "reason": "  正常申诉理由  "})
	mustCode(t, status, env, http.StatusOK, 0, "valid appeal after blank rejects")
}

func itoa(v uint) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
