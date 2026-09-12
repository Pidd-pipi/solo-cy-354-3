package repository

import (
	"context"

	"github.com/lp/campus-market/internal/model"
	"gorm.io/gorm"
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

// Create inserts a new appeal.
func (r *ReviewAppealRepository) Create(ctx context.Context, a *model.ReviewAppeal) error {
	return db(ctx, r.db).Create(a).Error
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

// FindByReviewID returns the appeal filed for the given review, if any.
func (r *ReviewAppealRepository) FindByReviewID(ctx context.Context, reviewID uint) (*model.ReviewAppeal, error) {
	var a model.ReviewAppeal
	err := db(ctx, r.db).Where("review_id = ?", reviewID).First(&a).Error
	if err != nil {
		return nil, normalizeError(err)
	}
	return &a, nil
}

// FindByReviewIDForUpdate returns the appeal with a row lock, valid inside a tx.
func (r *ReviewAppealRepository) FindByReviewIDForUpdate(ctx context.Context, reviewID uint) (*model.ReviewAppeal, error) {
	var a model.ReviewAppeal
	err := lockForUpdate(db(ctx, r.db)).Where("review_id = ?", reviewID).First(&a).Error
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

// SaveDecision persists the admin decision and its audit fields.
func (r *ReviewAppealRepository) SaveDecision(ctx context.Context, a *model.ReviewAppeal) error {
	return db(ctx, r.db).Model(&model.ReviewAppeal{}).Where("id = ?", a.ID).
		Updates(map[string]interface{}{
			"status":          a.Status,
			"admin_id":        a.AdminID,
			"review_comment":  a.ReviewComment,
			"credit_reversed": a.CreditReversed,
			"credit_delta":    a.CreditDelta,
			"reviewed_at":     a.ReviewedAt,
		}).Error
}
