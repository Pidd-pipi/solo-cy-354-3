package repository

import (
	"context"
	"time"

	"github.com/lp/campus-market/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ReviewAppealRepository persists review appeal rows.
type ReviewAppealRepository struct {
	db *gorm.DB
}

// NewReviewAppealRepository builds a ReviewAppealRepository.
func NewReviewAppealRepository(db *gorm.DB) *ReviewAppealRepository {
	return &ReviewAppealRepository{db: db}
}

// Transaction runs fn inside a database transaction for cross-repository writes.
func (r *ReviewAppealRepository) Transaction(ctx context.Context, fn func(txCtx context.Context) error) error {
	return Transaction(ctx, r.db, fn)
}

// CreateIfAbsent atomically inserts an appeal only when none exists for the
// review yet. It relies on the unique index on review_id with
// INSERT ... ON CONFLICT DO NOTHING, so concurrent submitters serialize at the
// database: exactly one row is created (created=true); losers get
// created=false instead of a duplicate-key error.
func (r *ReviewAppealRepository) CreateIfAbsent(ctx context.Context, a *model.ReviewAppeal) (created bool, err error) {
	res := db(ctx, r.db).Clauses(clause.OnConflict{DoNothing: true}).Create(a)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// FindByID returns the appeal with the given id.
func (r *ReviewAppealRepository) FindByID(ctx context.Context, id uint) (*model.ReviewAppeal, error) {
	var a model.ReviewAppeal
	err := db(ctx, r.db).First(&a, id).Error
	if err != nil {
		return nil, normalizeError(err)
	}
	return &a, nil
}

// FindByIDForUpdate returns the appeal with a row lock, valid inside a tx.
func (r *ReviewAppealRepository) FindByIDForUpdate(ctx context.Context, id uint) (*model.ReviewAppeal, error) {
	var a model.ReviewAppeal
	err := lockForUpdate(db(ctx, r.db)).First(&a, id).Error
	if err != nil {
		return nil, normalizeError(err)
	}
	return &a, nil
}

// ListByAppellant returns appeals filed by a user, newest first.
func (r *ReviewAppealRepository) ListByAppellant(ctx context.Context, appellantID uint) ([]model.ReviewAppeal, error) {
	var items []model.ReviewAppeal
	err := db(ctx, r.db).Where("appellant_id = ?", appellantID).
		Order("created_at DESC").Find(&items).Error
	return items, err
}

// ListByStatus returns appeals in the given status for admin processing.
// An empty status lists all appeals.
func (r *ReviewAppealRepository) ListByStatus(ctx context.Context, status string) ([]model.ReviewAppeal, error) {
	var items []model.ReviewAppeal
	q := db(ctx, r.db).Order("created_at ASC")
	if status != "" {
		q = q.Where("status = ?", status)
	}
	err := q.Find(&items).Error
	return items, err
}

// DecideIfPending atomically moves an appeal out of pending only while it is
// still pending (CAS). It runs as a single UPDATE ... WHERE status='pending',
// so concurrent admin reviews race on this one row: exactly one sees
// affected=true and is allowed to mutate credit; the rest see false and must
// return a business conflict without touching the score.
func (r *ReviewAppealRepository) DecideIfPending(ctx context.Context, id uint, status string, adminID uint, comment string, reviewedAt time.Time) (affected int64, err error) {
	res := db(ctx, r.db).Model(&model.ReviewAppeal{}).
		Where("id = ? AND status = ?", id, "pending").
		Updates(map[string]interface{}{
			"status":         status,
			"admin_id":       adminID,
			"review_comment": comment,
			"reviewed_at":    reviewedAt,
		})
	return res.RowsAffected, res.Error
}

// MarkCreditRollback records the applied rollback amount after a successful
// credit adjustment on an already-decided appeal.
func (r *ReviewAppealRepository) MarkCreditRollback(ctx context.Context, id uint, delta int) error {
	return db(ctx, r.db).Model(&model.ReviewAppeal{}).Where("id = ?", id).
		Updates(map[string]interface{}{"credit_reversed": true, "credit_delta": delta}).Error
}
