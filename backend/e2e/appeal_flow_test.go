package e2e

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_busy_timeout=5000"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
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
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatalf("seed users: %v", err)
	}
	products := []model.Product{
		{SellerID: 2, Title: "差评场景商品", Price: 100, Category: constants.ProductCategoryBooks, Condition: "九成新", Campus: "西校区", TradeLocation: "三食堂", Status: constants.ProductStatusSold},
		{SellerID: 2, Title: "好评场景商品", Price: 50, Category: constants.ProductCategoryBooks, Condition: "全新", Campus: "西校区", TradeLocation: "三食堂", Status: constants.ProductStatusSold},
	}
	if err := db.Create(&products).Error; err != nil {
		t.Fatalf("seed products: %v", err)
	}
	orders := []model.TradeOrder{
		{ProductID: 1, BuyerID: 1, SellerID: 2, Status: constants.TradeStatusCompleted},
		{ProductID: 2, BuyerID: 1, SellerID: 2, Status: constants.TradeStatusCompleted},
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
