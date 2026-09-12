// Command realserver boots the production Gin router against a file-backed
// SQLite database for end-to-end verification without MySQL/Docker. It is
// excluded from normal builds via the "realserver" tag:
//
//	go run -tags realserver ./e2e/cmd/realserver
//
// Modes:
//
//	LEGACY_SEED=1  build an OLD-schema database (reviews without credit_delta)
//	               with legacy review histories, print key review ids, then exit
//	(default)      run the production startup sequence (AutoMigrate +
//	               migration.Run backfill + seed) and serve HTTP
//
//go:build realserver

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

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

func main() {
	port := getenv("PORT", "29514")
	dbPath := getenv("DB_PATH", "/tmp/e2e_campus.db")
	secret := getenv("JWT_SECRET", "e2e-secret")
	dsn := dbPath + "?_busy_timeout=10000&_journal_mode=WAL"

	if os.Getenv("LEGACY_SEED") == "1" {
		// Plain rollback journal so committed legacy data is fully in the main
		// database file before the server later opens it in WAL mode.
		seedLegacyDatabase(dbPath)
		return
	}

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
	})
	if err != nil {
		slog.Error("open db", "error", err)
		os.Exit(1)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(10)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Product{}, &model.Conversation{}, &model.Message{},
		&model.TradeOrder{}, &model.Review{}, &model.BookExchange{}, &model.ReviewAppeal{},
	); err != nil {
		slog.Error("migrate", "error", err)
		os.Exit(1)
	}
	// Same one-off data migration sequence as cmd/server/main.go.
	if err := migration.Run(context.Background(), db, slog.Default()); err != nil {
		slog.Error("data migration", "error", err)
		os.Exit(1)
	}
	seed(db)

	cfg := &config.Config{
		Port: port, JWTSecret: secret, JWTExpireHours: 12,
		RateLimitPerMin: 10000, LoginRateLimit: 10000,
		CORSOrigins:    []string{"*"},
		SeedingEnabled: false,
	}
	if err := router.New(cfg, db, slog.Default()).Run(":" + port); err != nil {
		slog.Error("server", "error", err)
		os.Exit(1)
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type legacyReview struct {
	ID         uint   `gorm:"primaryKey"`
	TradeID    uint   `gorm:"column:trade_id"`
	ReviewerID uint   `gorm:"column:reviewer_id"`
	RevieweeID uint   `gorm:"column:reviewee_id"`
	Rating     string `gorm:"column:rating"`
	Content    string `gorm:"column:content"`
}

func (legacyReview) TableName() string { return "reviews" }

// seedLegacyDatabase builds an OLD-schema file (reviews WITHOUT credit_delta)
// with review histories whose clamping is only reconstructable by replay:
//   - seller 2: 1 bad review (100->90)
//   - seller 5: 11 bad reviews (100->0; the 11th changed nothing at the floor)
//   - seller 6: 41 good reviews (100->300; the 41st changed nothing at the ceiling)
func seedLegacyDatabase(dsn string) {
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		slog.Error("legacy open", "error", err)
		os.Exit(1)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Product{}, &model.TradeOrder{}); err != nil {
		slog.Error("legacy base migrate", "error", err)
		os.Exit(1)
	}
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
		slog.Error("legacy reviews table", "error", err)
		os.Exit(1)
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
		slog.Error("legacy users", "error", err)
		os.Exit(1)
	}
	// Force exact stored scores via an independent map: GORM backfills the
	// column default (100) into the zero-valued CreditScore struct field on
	// Create, which would otherwise erase the intended floor score of 0.
	intended := map[uint]int{1: 100, 2: 90, 3: 100, 4: 300, 5: 0, 6: 300}
	for _, u := range users {
		if err := db.Model(&model.User{}).Where("id = ?", u.ID).UpdateColumn("credit_score", intended[u.ID]).Error; err != nil {
			slog.Error("legacy score", "error", err)
			os.Exit(1)
		}
	}
	ids := map[string]uint{}
	addChain := func(name string, sellerID uint, n int, rating string) []uint {
		out := make([]uint, 0, n)
		for i := 0; i < n; i++ {
			p := model.Product{SellerID: sellerID, Title: fmt.Sprintf("legacy-%s-%d", name, i), Price: 10,
				Category: constants.ProductCategoryBooks, Status: constants.ProductStatusSold}
			if err := db.Create(&p).Error; err != nil {
				slog.Error("legacy product", "error", err)
				os.Exit(1)
			}
			o := model.TradeOrder{ProductID: p.ID, BuyerID: 1, SellerID: sellerID, Status: constants.TradeStatusCompleted}
			if err := db.Create(&o).Error; err != nil {
				slog.Error("legacy order", "error", err)
				os.Exit(1)
			}
			rv := legacyReview{TradeID: o.ID, ReviewerID: 1, RevieweeID: sellerID, Rating: rating, Content: "legacy"}
			if err := db.Create(&rv).Error; err != nil {
				slog.Error("legacy review", "error", err)
				os.Exit(1)
			}
			out = append(out, rv.ID)
		}
		return out
	}
	normal := addChain("normal", 2, 1, constants.ReviewRatingBad)
	floor := addChain("floor", 5, 11, constants.ReviewRatingBad)
	ceil := addChain("ceil", 6, 41, constants.ReviewRatingGood)
	ids["normal_bad"] = normal[0]
	ids["floor_10th_effective_minus10"] = floor[9]
	ids["floor_11th_clamped_zero"] = floor[10]
	ids["ceil_40th_effective_plus5"] = ceil[39]
	ids["ceil_41st_clamped_zero"] = ceil[40]
	raw, _ := json.Marshal(ids)
	fmt.Printf("LEGACY_SEED_READY reviews=%s\n", string(raw))
}

func seed(db *gorm.DB) {
	var count int64
	db.Model(&model.User{}).Count(&count)
	if count > 0 {
		slog.Info("seed skipped: users exist", "count", count)
		return
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("123456"), bcrypt.DefaultCost)
	adminHash, _ := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.DefaultCost)
	users := []model.User{
		{Phone: "13700000001", PasswordHash: string(hash), Nickname: "买家小明", Role: constants.UserRoleStudent, Campus: "东校区", CreditScore: 100},
		{Phone: "13700000002", PasswordHash: string(hash), Nickname: "卖家阿珍", Role: constants.UserRoleStudent, Campus: "西校区", CreditScore: 120},
		{Phone: "13700000003", PasswordHash: string(hash), Nickname: "路人达人", Role: constants.UserRoleStudent, Campus: "南校区", CreditScore: 90},
		{Phone: "13800000001", PasswordHash: string(adminHash), Nickname: "平台管理员", Role: constants.UserRoleAdmin, Campus: "东校区", CreditScore: 300},
		{Phone: "13700000005", PasswordHash: string(hash), Nickname: "触底卖家", Role: constants.UserRoleStudent, Campus: "东校区", CreditScore: 4},
		{Phone: "13700000006", PasswordHash: string(hash), Nickname: "触顶卖家", Role: constants.UserRoleStudent, Campus: "东校区", CreditScore: 298},
	}
	if err := db.Create(&users).Error; err != nil {
		slog.Error("seed users", "error", err)
		os.Exit(1)
	}
	products := []model.Product{
		{SellerID: 2, Title: "差评场景商品", Price: 100, Category: constants.ProductCategoryBooks, Condition: "九成新", Campus: "西校区", TradeLocation: "三食堂", Status: constants.ProductStatusSold},
		{SellerID: 2, Title: "好评场景商品", Price: 50, Category: constants.ProductCategoryBooks, Condition: "全新", Campus: "西校区", TradeLocation: "三食堂", Status: constants.ProductStatusSold},
		{SellerID: 5, Title: "触底卖家的商品", Price: 30, Category: constants.ProductCategoryBooks, Condition: "九成新", Campus: "东校区", TradeLocation: "东门", Status: constants.ProductStatusSold},
		{SellerID: 6, Title: "触顶卖家的商品", Price: 30, Category: constants.ProductCategoryBooks, Condition: "九成新", Campus: "东校区", TradeLocation: "东门", Status: constants.ProductStatusSold},
	}
	if err := db.Create(&products).Error; err != nil {
		slog.Error("seed products", "error", err)
		os.Exit(1)
	}
	orders := []model.TradeOrder{
		{ProductID: 1, BuyerID: 1, SellerID: 2, Status: constants.TradeStatusCompleted},
		{ProductID: 2, BuyerID: 1, SellerID: 2, Status: constants.TradeStatusCompleted},
		{ProductID: 3, BuyerID: 1, SellerID: 5, Status: constants.TradeStatusCompleted},
		{ProductID: 4, BuyerID: 1, SellerID: 6, Status: constants.TradeStatusCompleted},
	}
	if err := db.Create(&orders).Error; err != nil {
		slog.Error("seed orders", "error", err)
		os.Exit(1)
	}
	slog.Info("seed completed", "users", len(users), "products", len(products), "orders", len(orders))
}
