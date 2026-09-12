// Package migration runs one-off, versioned data migrations after AutoMigrate.
package migration

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/lp/campus-market/internal/model"
	"github.com/lp/campus-market/internal/util"
	"gorm.io/gorm"
)

// registrationBaseline is the credit score every account starts with
// (matches the users.credit_score default and UserService.Register).
const registrationBaseline = 100

// currentVersion is the latest data-migration version. Each historical data
// fix must bump this constant and append its step to steps.
const currentVersion = 1

// schemaMigration records which one-off data migrations have already run, so
// backfills are never applied twice.
type schemaMigration struct {
	Version   int       `gorm:"primaryKey"`
	Name      string    `gorm:"size:128;not null"`
	AppliedAt time.Time `gorm:"not null"`
}

// step is a single historical data migration.
type step struct {
	version int
	name    string
	fn      func(ctx context.Context, db *gorm.DB, logger *slog.Logger) error
}

// steps lists data migrations in application order.
func steps(logger *slog.Logger) []step {
	return []step{
		{version: 1, name: "backfill_review_credit_delta", fn: backfillReviewCreditDelta(logger)},
	}
}

// Run executes all pending data migrations inside a transaction each. It is
// idempotent: completed versions are persisted in schema_migrations.
func Run(ctx context.Context, db *gorm.DB, logger *slog.Logger) error {
	if err := db.WithContext(ctx).AutoMigrate(&schemaMigration{}); err != nil {
		return fmt.Errorf("migrate schema_migrations table: %w", err)
	}
	var applied []schemaMigration
	if err := db.WithContext(ctx).Find(&applied).Error; err != nil {
		return fmt.Errorf("load applied migrations: %w", err)
	}
	done := make(map[int]bool, len(applied))
	for _, m := range applied {
		done[m.Version] = true
	}
	for _, st := range steps(logger) {
		if st.version > currentVersion {
			return fmt.Errorf("migration step %d exceeds binary version %d", st.version, currentVersion)
		}
		if done[st.version] {
			continue
		}
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := st.fn(ctx, tx, logger); err != nil {
				return err
			}
			return tx.Create(&schemaMigration{Version: st.version, Name: st.name, AppliedAt: time.Now()}).Error
		}); err != nil {
			return fmt.Errorf("migration %d (%s): %w", st.version, st.name, err)
		}
		logger.Info("data migration applied", slog.Int("version", st.version), slog.String("name", st.name))
	}
	return nil
}

// backfillReviewCreditDelta reconstructs the ACTUAL per-review credit delta
// for rows written before reviews.credit_delta existed (old rows come out of
// AutoMigrate with 0). It replays each user's review history in chronological
// order from the registration baseline of 100, applying the same [0,300]
// clamp the live service uses today. That makes an appeal on a legacy bad
// review restore exactly the score the user had before that review, including
// reviews whose nominal delta was partially clamped at the floor/ceiling.
func backfillReviewCreditDelta(logger *slog.Logger) func(ctx context.Context, db *gorm.DB, logger *slog.Logger) error {
	return func(ctx context.Context, db *gorm.DB, logger *slog.Logger) error {
		var userIDs []uint
		if err := db.WithContext(ctx).Model(&model.Review{}).
			Distinct().Pluck("reviewee_id", &userIDs).Error; err != nil {
			return fmt.Errorf("list legacy reviewees: %w", err)
		}
		totalBackfilled := 0
		for _, uid := range userIDs {
			// Replay the user's FULL history from the 100 registration
			// baseline. Rows already carrying a delta (written by the new
			// code or a previous partial run) participate in the replay but
			// are never rewritten; only legacy zero rows are backfilled.
			var all []model.Review
			if err := db.WithContext(ctx).Where("reviewee_id = ?", uid).
				Order("id ASC").Find(&all).Error; err != nil {
				return fmt.Errorf("load reviews for user %d: %w", uid, err)
			}
			score := registrationBaseline
			updates := 0
			for i := range all {
				rv := &all[i]
				actual := util.AppliedDelta(score, util.CreditDelta(rv.Rating))
				if rv.CreditDelta == 0 {
					// Legacy row: write the reconstructed delta. Zero is a
					// legitimate value (medium, or good/bad fully clamped at
					// the bound), in which case the UPDATE is a harmless
					// no-op and the row remains a legacy zero indistinguishable
					// from a real zero — both roll back by exactly zero.
					if err := db.WithContext(ctx).Model(&model.Review{}).
						Where("id = ? AND credit_delta = 0", rv.ID).
						UpdateColumn("credit_delta", actual).Error; err != nil {
						return fmt.Errorf("backfill review %d: %w", rv.ID, err)
					}
					if actual != 0 {
						updates++
					}
				}
				score = util.ClampCredit(score + actual)
			}
			totalBackfilled += updates
			var current model.User
			if err := db.WithContext(ctx).First(&current, uid).Error; err == nil && current.CreditScore != score {
				// A mismatch means the history was affected by something other
				// than review deltas (e.g. approved legacy appeals); deltas are
				// still reconstructed from the registration baseline.
				logger.Warn("legacy credit history replay diverges from current score",
					slog.Uint64("user_id", uint64(uid)),
					slog.Int("replayed_score", score),
					slog.Int("current_score", current.CreditScore))
			}
		}
		logger.Info("legacy review credit_delta backfill completed", slog.Int("reviews_backfilled", totalBackfilled))
		return nil
	}
}
