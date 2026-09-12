package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lp/campus-market/internal/config"
	"github.com/lp/campus-market/internal/constants"
	"github.com/lp/campus-market/internal/migration"
	"github.com/lp/campus-market/internal/model"
	"github.com/lp/campus-market/internal/router"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// legacyReview mirrors the reviews table as it existed BEFORE the
// credit_delta column was introduced.
type legacyReview struct {
	ID         uint   `gorm:"primaryKey"`
	TradeID    uint   `gorm:"column:trade_id"`
	ReviewerID uint   `gorm:"column:reviewer_id"`
	RevieweeID uint   `gorm:"column:reviewee_id"`
	Rating     string `gorm:"column:rating"`
	Content    string `gorm:"column:content"`
}

func (legacyReview) TableName() string { return "reviews" }

// seedLegacyDatabase builds an OLD-schema database (reviews without
// credit_delta) containing realistic legacy review histories whose clamping is
// only reconstructable by replaying the whole history from the baseline of 100:
//   - seller 2: one bad review, score 90
//   - seller 5: eleven bad reviews, score 0 (the 11th was fully clamped at floor)
//   - seller 6: forty-one good reviews, score 300 (the 41st was fully clamped at ceiling)
func seedLegacyDatabase(t *testing.T, dbPath string) (floorReview10ID, floorReview11ID, ceilReview40ID, ceilReview41ID, normalReviewID uint) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Product{}, &model.TradeOrder{}); err != nil {
		t.Fatalf("legacy base migrate: %v", err)
	}
	// Create the OLD reviews table explicitly (no credit_delta column). The
	// inline named unique constraint mirrors what the GORM-produced (and
	// production MySQL) schema carries; without it, the SQLite migrator would
	// rebuild the table on every AutoMigrate and drop the newly-added column
	// when copying rows.
	if err := db.Exec(`CREATE TABLE reviews (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		trade_id INTEGER NOT NULL,
		reviewer_id INTEGER NOT NULL,
		reviewee_id INTEGER NOT NULL,
		rating TEXT NOT NULL,
		content TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		CONSTRAINT uniq_review_trade_reviewer UNIQUE(trade_id, reviewer_id)
	)`).Error; err != nil {
		t.Fatalf("create legacy reviews table: %v", err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("123456"), bcrypt.DefaultCost)
	adminHash, _ := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.DefaultCost)
	users := []model.User{
		{Phone: "13700000001", PasswordHash: string(hash), Nickname: "买家", Role: constants.UserRoleStudent, CreditScore: 100},
		{Phone: "13700000002", PasswordHash: string(hash), Nickname: "普通卖家", Role: constants.UserRoleStudent, CreditScore: 90},
		{Phone: "13700000003", PasswordHash: string(hash), Nickname: "路人", Role: constants.UserRoleStudent, CreditScore: 100},
		{Phone: "13800000001", PasswordHash: string(adminHash), Nickname: "管理员", Role: constants.UserRoleAdmin, CreditScore: 300},
		{Phone: "13700000005", PasswordHash: string(hash), Nickname: "触底卖家", Role: constants.UserRoleStudent, CreditScore: 0},
		{Phone: "13700000006", PasswordHash: string(hash), Nickname: "触顶卖家", Role: constants.UserRoleStudent, CreditScore: 300},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatalf("seed legacy users: %v", err)
	}
	// GORM Create omits zero-valued CreditScore and the column default (100)
	// would otherwise replace the intended legacy floor score 0; force the
	// exact stored values for every account.
	intended := map[uint]int{1: 100, 2: 90, 3: 100, 4: 300, 5: 0, 6: 300}
	for _, u := range users {
		if err := db.Model(&model.User{}).Where("id = ?", u.ID).
			UpdateColumn("credit_score", intended[u.ID]).Error; err != nil {
			t.Fatalf("force legacy score for %d: %v", u.ID, err)
		}
	}
	addChain := func(sellerID uint, n int, rating string) []uint {
		ids := make([]uint, 0, n)
		for i := 0; i < n; i++ {
			p := model.Product{SellerID: sellerID, Title: fmt.Sprintf("legacy-%d-%d", sellerID, i), Price: 10,
				Category: constants.ProductCategoryBooks, Status: constants.ProductStatusSold}
			if err := db.Create(&p).Error; err != nil {
				t.Fatalf("legacy product: %v", err)
			}
			o := model.TradeOrder{ProductID: p.ID, BuyerID: 1, SellerID: sellerID, Status: constants.TradeStatusCompleted}
			if err := db.Create(&o).Error; err != nil {
				t.Fatalf("legacy order: %v", err)
			}
			rv := legacyReview{TradeID: o.ID, ReviewerID: 1, RevieweeID: sellerID, Rating: rating, Content: "legacy"}
			if err := db.Create(&rv).Error; err != nil {
				t.Fatalf("legacy review: %v", err)
			}
			ids = append(ids, rv.ID)
		}
		return ids
	}
	normalIDs := addChain(2, 1, constants.ReviewRatingBad)
	floorIDs := addChain(5, 11, constants.ReviewRatingBad)
	ceilIDs := addChain(6, 41, constants.ReviewRatingGood)
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()
	return floorIDs[9], floorIDs[10], ceilIDs[39], ceilIDs[40], normalIDs[0]
}

// openUpgradedDatabase opens the legacy file with the NEW binary's startup
// sequence: AutoMigrate adds the column and new tables, then data migrations
// backfill legacy deltas.
func openUpgradedDatabase(t *testing.T, dbPath string) (*gin.Engine, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath+"?_busy_timeout=10000&_journal_mode=WAL"),
		&gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open upgraded db: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(10)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Product{}, &model.Conversation{}, &model.Message{},
		&model.TradeOrder{}, &model.Review{}, &model.BookExchange{}, &model.ReviewAppeal{},
	); err != nil {
		t.Fatalf("upgrade automigrate: %v", err)
	}
	if err := migration.Run(context.Background(), db, slog.Default()); err != nil {
		t.Fatalf("migration run: %v", err)
	}
	cfg := &config.Config{JWTSecret: "e2e-secret", JWTExpireHours: 12,
		RateLimitPerMin: 10000, LoginRateLimit: 10000, CORSOrigins: []string{"*"}}
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	return router.New(cfg, db, slog.Default()), db
}

// TestLegacyDatabaseUpgradeBackfill verifies the one-time backfill rebuilds
// ACTUAL per-review deltas for pre-upgrade rows, is idempotent, and that
// approving legacy appeals through the real API restores the pre-review score.
func TestLegacyDatabaseUpgradeBackfill(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	floor10, floor11, ceil40, ceil41, normalID := seedLegacyDatabase(t, dbPath)
	r, db := openUpgradedDatabase(t, dbPath)

	// The new column exists and legacy rows were backfilled.
	var normal model.Review
	if err := db.First(&normal, normalID).Error; err != nil {
		t.Fatalf("load normal review: %v", err)
	}
	if normal.CreditDelta != -10 {
		t.Fatalf("normal legacy bad review delta want -10, got %d", normal.CreditDelta)
	}
	var floorDeltas, ceilDeltas []int
	db.Model(&model.Review{}).Where("reviewee_id = ?", 5).Order("id ASC").Pluck("credit_delta", &floorDeltas)
	db.Model(&model.Review{}).Where("reviewee_id = ?", 6).Order("id ASC").Pluck("credit_delta", &ceilDeltas)
	if len(floorDeltas) != 11 || len(ceilDeltas) != 41 {
		t.Fatalf("unexpected delta counts: %d %d", len(floorDeltas), len(ceilDeltas))
	}
	for i, d := range floorDeltas {
		want := -10
		if i == 10 {
			want = 0 // fully clamped at floor
		}
		if d != want {
			t.Fatalf("floor review %d delta want %d, got %d", i+1, want, d)
		}
	}
	for i, d := range ceilDeltas {
		want := 5
		if i == 40 {
			want = 0 // fully clamped at ceiling
		}
		if d != want {
			t.Fatalf("ceiling review %d delta want %d, got %d", i+1, want, d)
		}
	}

	// Migration is versioned and idempotent: a second run changes nothing.
	if err := migration.Run(context.Background(), db, slog.Default()); err != nil {
		t.Fatalf("second migration run: %v", err)
	}
	var versions []int
	db.Table("schema_migrations").Order("version ASC").Pluck("version", &versions)
	if len(versions) != 1 || versions[0] != 1 {
		t.Fatalf("schema_migrations want [1], got %v", versions)
	}

	// Real API: appeal legacy reviews and approve; scores must return to the
	// score the user had right before each of those reviews.
	buyer := login(t, r, "13700000001", "123456")
	normalSeller := login(t, r, "13700000002", "123456")
	floorSeller := login(t, r, "13700000005", "123456")
	ceilSeller := login(t, r, "13700000006", "123456")
	admin := login(t, r, "13800000001", "admin123")

	approve := func(sellerToken string, reviewID uint, expectDelta int) {
		t.Helper()
		status, env := doJSON(t, r, http.MethodPost, "/api/v1/appeals", sellerToken,
			map[string]interface{}{"review_id": reviewID, "reason": "旧库存量评价的申诉理由需要足够长"})
		mustCode(t, status, env, http.StatusOK, 0, fmt.Sprintf("appeal legacy review %d", reviewID))
		var appeal struct {
			ID uint `json:"id"`
		}
		_ = json.Unmarshal(env.Data, &appeal)
		status, env = doJSON(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/appeals/%d/review", appeal.ID), admin,
			map[string]interface{}{"action": "approve"})
		mustCode(t, status, env, http.StatusOK, 0, "approve legacy appeal")
		var decision struct {
			CreditDelta    int  `json:"credit_delta"`
			CreditReversed bool `json:"credit_reversed"`
		}
		_ = json.Unmarshal(env.Data, &decision)
		if decision.CreditDelta != expectDelta {
			t.Fatalf("legacy rollback review %d want delta %d, got %d", reviewID, expectDelta, decision.CreditDelta)
		}
		if (expectDelta != 0) != decision.CreditReversed {
			t.Fatalf("legacy rollback review %d reversed flag mismatch: %+v", reviewID, decision)
		}
	}

	// Normal seller: 90 -> back to pre-review 100.
	approve(normalSeller, normalID, 10)
	if got := creditOf(t, db, 2); got != 100 {
		t.Fatalf("normal legacy seller score want 100, got %d", got)
	}
	// Floor seller: 10th bad review moved 10 -> 0; rollback restores 10.
	approve(floorSeller, floor10, 10)
	if got := creditOf(t, db, 5); got != 10 {
		t.Fatalf("floor legacy seller score want 10, got %d", got)
	}
	// The fully-clamped 11th review changed nothing; approval is a no-op.
	approve(floorSeller, floor11, 0)
	if got := creditOf(t, db, 5); got != 10 {
		t.Fatalf("clamped review must not change score, got %d", got)
	}
	// Ceiling seller: 40th good review moved 295 -> 300; rollback restores 295.
	approve(ceilSeller, ceil40, -5)
	if got := creditOf(t, db, 6); got != 295 {
		t.Fatalf("ceiling legacy seller score want 295, got %d", got)
	}
	// Fully-clamped 41st review: no-op.
	approve(ceilSeller, ceil41, 0)
	if got := creditOf(t, db, 6); got != 295 {
		t.Fatalf("clamped ceiling review must not change score, got %d", got)
	}
	_ = buyer

	// Regression: a SECOND boot of the new binary (AutoMigrate + migrations
	// again) must not rebuild the reviews table and wipe the backfilled
	// credit_delta column. Open a fresh handle to the same file like a restart.
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()
	reopened, err := gorm.Open(sqlite.Open(dbPath+"?_busy_timeout=10000&_journal_mode=WAL"),
		&gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	if err := reopened.AutoMigrate(
		&model.User{}, &model.Product{}, &model.Conversation{}, &model.Message{},
		&model.TradeOrder{}, &model.Review{}, &model.BookExchange{}, &model.ReviewAppeal{},
	); err != nil {
		t.Fatalf("second-boot automigrate: %v", err)
	}
	if err := migration.Run(context.Background(), reopened, slog.Default()); err != nil {
		t.Fatalf("second-boot migration: %v", err)
	}
	for _, tc := range []struct {
		id, want int
	}{
		{int(normalID), -10}, {int(floor10), -10}, {int(floor11), 0},
		{int(ceil40), 5}, {int(ceil41), 0},
	} {
		var rv model.Review
		if err := reopened.First(&rv, tc.id).Error; err != nil {
			t.Fatalf("reload review %d: %v", tc.id, err)
		}
		if rv.CreditDelta != tc.want {
			t.Fatalf("after restart review %d delta want %d, got %d", tc.id, tc.want, rv.CreditDelta)
		}
	}
	if r2, _ := reopened.DB(); r2 != nil {
		_ = r2.Close()
	}
}

// TestNewDatabaseMatchesUpgradedBehavior seeds the SAME post-review state on a
// brand-new schema (reviews carrying the deltas the live code writes) and
// asserts identical appeal outcomes, proving old and new databases behave the
// same after upgrade.
func TestNewDatabaseMatchesUpgradedBehavior(t *testing.T) {
	r, db := setupEngine(t)
	buyer := login(t, r, "13700000001", "123456")
	admin := login(t, r, "13800000001", "admin123")
	floorSeller := login(t, r, "13700000005", "123456")

	// New DB, single floor review like the original clamp test: 4 -> 0.
	status, env := doJSON(t, r, http.MethodPost, "/api/v1/reviews", buyer,
		map[string]interface{}{"trade_id": 3, "rating": "bad", "content": "new db floor"})
	mustCode(t, status, env, http.StatusOK, 0, "new db floor review")
	var rv struct {
		ID          uint `json:"id"`
		CreditDelta int  `json:"credit_delta"`
	}
	_ = json.Unmarshal(env.Data, &rv)
	if rv.CreditDelta != -4 {
		t.Fatalf("new db actual delta want -4, got %d", rv.CreditDelta)
	}
	if got := creditOf(t, db, 5); got != 0 {
		t.Fatalf("new db floor score want 0, got %d", got)
	}
	status, env = doJSON(t, r, http.MethodPost, "/api/v1/appeals", floorSeller,
		map[string]interface{}{"review_id": rv.ID, "reason": "新库触底评价申诉理由足够长"})
	mustCode(t, status, env, http.StatusOK, 0, "new db appeal")
	var appeal struct {
		ID uint `json:"id"`
	}
	_ = json.Unmarshal(env.Data, &appeal)
	status, env = doJSON(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/appeals/%d/review", appeal.ID), admin,
		map[string]interface{}{"action": "approve"})
	mustCode(t, status, env, http.StatusOK, 0, "new db approve")
	var decision struct {
		CreditDelta int `json:"credit_delta"`
	}
	_ = json.Unmarshal(env.Data, &decision)
	if decision.CreditDelta != 4 || creditOf(t, db, 5) != 4 {
		t.Fatalf("new db rollback want +4 -> score 4, got delta=%d score=%d", decision.CreditDelta, creditOf(t, db, 5))
	}

	// Parity with the upgraded legacy DB: a nominal bad review away from the
	// bounds reconstructs the same -10/+10 the backfill assigns to old rows.
	status, env = doJSON(t, r, http.MethodPost, "/api/v1/reviews", buyer,
		map[string]interface{}{"trade_id": 1, "rating": "bad", "content": "new db nominal"})
	mustCode(t, status, env, http.StatusOK, 0, "new db nominal review")
	var normal struct {
		ID          uint `json:"id"`
		CreditDelta int  `json:"credit_delta"`
	}
	_ = json.Unmarshal(env.Data, &normal)
	if normal.CreditDelta != -10 {
		t.Fatalf("new db nominal delta want -10, got %d", normal.CreditDelta)
	}
	normalSeller := login(t, r, "13700000002", "123456")
	if got := creditOf(t, db, 2); got != 110 {
		t.Fatalf("new db seller2 score want 110, got %d", got)
	}
	status, env = doJSON(t, r, http.MethodPost, "/api/v1/appeals", normalSeller,
		map[string]interface{}{"review_id": normal.ID, "reason": "新库普通差评申诉理由足够长"})
	mustCode(t, status, env, http.StatusOK, 0, "new db nominal appeal")
	var normalAppeal struct {
		ID uint `json:"id"`
	}
	_ = json.Unmarshal(env.Data, &normalAppeal)
	status, env = doJSON(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/appeals/%d/review", normalAppeal.ID), admin,
		map[string]interface{}{"action": "approve"})
	mustCode(t, status, env, http.StatusOK, 0, "new db nominal approve")
	var normalDecision struct {
		CreditDelta int `json:"credit_delta"`
	}
	_ = json.Unmarshal(env.Data, &normalDecision)
	if normalDecision.CreditDelta != 10 || creditOf(t, db, 2) != 120 {
		t.Fatalf("new db nominal rollback want +10 -> 120, got delta=%d score=%d",
			normalDecision.CreditDelta, creditOf(t, db, 2))
	}

	// Running migrations on a fresh database must be a no-op for existing deltas.
	if err := migration.Run(context.Background(), db, slog.Default()); err != nil {
		t.Fatalf("migration on new db: %v", err)
	}
	var rv2 model.Review
	if err := db.First(&rv2, normal.ID).Error; err != nil {
		t.Fatalf("reload new db review: %v", err)
	}
	if rv2.CreditDelta != -10 {
		t.Fatalf("migration must not rewrite correct deltas, got %d", rv2.CreditDelta)
	}
}
