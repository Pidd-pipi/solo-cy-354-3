// Command realserver boots the production Gin router against a file-backed
// SQLite database for end-to-end verification without MySQL/Docker. It is
// excluded from normal builds via the "realserver" tag:
//
//	go run -tags realserver ./e2e/cmd/realserver
//
//go:build realserver

package main

import (
	"log/slog"
	"os"

	"github.com/lp/campus-market/internal/config"
	"github.com/lp/campus-market/internal/constants"
	"github.com/lp/campus-market/internal/model"
	"github.com/lp/campus-market/internal/router"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "29514"
	}
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "/tmp/e2e_campus.db"
	}
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		secret = "e2e-secret"
	}

	db, err := gorm.Open(sqlite.Open(dbPath+"?_busy_timeout=10000&_journal_mode=WAL"), &gorm.Config{
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
	seed(db)

	cfg := &config.Config{
		Port: port, JWTSecret: secret, JWTExpireHours: 12,
		RateLimitPerMin: 10000, LoginRateLimit: 10000,
		CORSOrigins:    []string{"*"},
		SeedingEnabled: false,
	}
	router.New(cfg, db, slog.Default()).Run(":" + port)
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
