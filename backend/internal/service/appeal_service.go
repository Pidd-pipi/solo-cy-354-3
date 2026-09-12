package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lp/campus-market/internal/constants"
	"github.com/lp/campus-market/internal/dto"
	"github.com/lp/campus-market/internal/model"
	"github.com/lp/campus-market/internal/util"
)

// AppealStore is the data access contract for appeal rows.
type AppealStore interface {
	Transaction(ctx context.Context, fn func(txCtx context.Context) error) error
	// CreateIfAbsent atomically inserts only when no appeal exists for the review.
	CreateIfAbsent(ctx context.Context, a *model.ReviewAppeal) (bool, error)
	FindByID(ctx context.Context, id uint) (*model.ReviewAppeal, error)
	FindByIDForUpdate(ctx context.Context, id uint) (*model.ReviewAppeal, error)
	ListByAppellant(ctx context.Context, appellantID uint) ([]model.ReviewAppeal, error)
	ListByStatus(ctx context.Context, status string) ([]model.ReviewAppeal, error)
	// DecideIfPending claims a pending appeal atomically (CAS); only one
	// concurrent decision affects one row.
	DecideIfPending(ctx context.Context, id uint, status string, adminID uint, comment string, reviewedAt time.Time) (int64, error)
	MarkCreditRollback(ctx context.Context, id uint, delta int) error
}

// AppealReviewStore is the data access contract for reviews read by appeals.
type AppealReviewStore interface {
	FindByID(ctx context.Context, id uint) (*model.Review, error)
}

// AppealService manages credit appeals: submit, progress query and admin review.
type AppealService struct {
	appeals AppealStore
	reviews AppealReviewStore
	users   UserRepository
	logger  *slog.Logger
}

// NewAppealService wires the appeal service dependencies.
func NewAppealService(appeals AppealStore, reviews AppealReviewStore, users UserRepository, logger *slog.Logger) *AppealService {
	return &AppealService{appeals: appeals, reviews: reviews, users: users, logger: logger}
}

// Submit files one appeal for a received review. Only the review receiver
// (reviewee) can appeal, and each review may be appealed exactly once. The
// uniqueness is enforced atomically by the database unique index
// (ON CONFLICT DO NOTHING), so concurrent submits never produce two rows.
func (s *AppealService) Submit(ctx context.Context, appellant *model.User, req *dto.CreateAppealRequest) (*dto.AppealView, error) {
	reason := strings.TrimSpace(req.Reason)
	if len([]rune(reason)) < 2 {
		return nil, util.NewAppError(400, constants.CodeValidation, constants.MsgAppealReasonRequired, nil)
	}
	rv, err := s.reviews.FindByID(ctx, req.ReviewID)
	if err != nil {
		if errors.Is(err, util.ErrNotFound) {
			return nil, util.NewAppError(404, constants.CodeNotFound, constants.MsgReviewNotFound, nil)
		}
		return nil, util.WrapAppError(fmt.Errorf("appeal[review=%d] review lookup: %w", req.ReviewID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	if rv.RevieweeID != appellant.ID {
		s.logger.Warn(fmt.Sprintf("appeal submit denied: review_id=%d appellant=%d role=reviewee_required", req.ReviewID, appellant.ID))
		return nil, util.NewAppError(403, constants.CodeForbidden, constants.MsgAppealNotReviewee, nil)
	}
	a := &model.ReviewAppeal{
		ReviewID: rv.ID, AppellantID: appellant.ID,
		Reason: reason, Status: constants.AppealStatusPending,
	}
	if err := s.appeals.Transaction(ctx, func(txCtx context.Context) error {
		created, err := s.appeals.CreateIfAbsent(txCtx, a)
		if err != nil {
			return err
		}
		if !created {
			return util.ErrConflict
		}
		return nil
	}); err != nil {
		if errors.Is(err, util.ErrConflict) {
			return nil, util.NewAppError(409, constants.CodeConflict, constants.MsgAppealAlreadyExists, nil)
		}
		s.logger.Error(fmt.Sprintf(constants.LogAppealSubmitFailed, req.ReviewID, appellant.ID, err))
		return nil, util.WrapAppError(fmt.Errorf("appeal[review=%d] create: %w", req.ReviewID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	s.logger.Info(fmt.Sprintf(constants.LogAppealSubmitSuccess, a.ID, rv.ID, appellant.ID))
	return s.buildView(ctx, a, rv, appellant.Nickname)
}

// GetProgress returns one appeal; only its appellant (or an admin, enforced at
// the route layer) may read it.
func (s *AppealService) GetProgress(ctx context.Context, userID uint, appealID uint) (*dto.AppealView, error) {
	a, err := s.appeals.FindByID(ctx, appealID)
	if err != nil {
		if errors.Is(err, util.ErrNotFound) {
			return nil, util.NewAppError(404, constants.CodeNotFound, constants.MsgAppealNotFound, nil)
		}
		return nil, util.WrapAppError(fmt.Errorf("appeal[id=%d] lookup: %w", appealID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	if a.AppellantID != userID {
		return nil, util.NewAppError(403, constants.CodeForbidden, constants.MsgAppealNotOwner, nil)
	}
	return s.buildView(ctx, a, nil, "")
}

// ListMine returns the appeals filed by the current user with review snapshots.
func (s *AppealService) ListMine(ctx context.Context, userID uint) ([]dto.AppealView, error) {
	items, err := s.appeals.ListByAppellant(ctx, userID)
	if err != nil {
		return nil, util.WrapAppError(fmt.Errorf("appeal[appellant=%d] list: %w", userID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	views := make([]dto.AppealView, 0, len(items))
	for i := range items {
		v, err := s.buildView(ctx, &items[i], nil, "")
		if err != nil {
			return nil, err
		}
		views = append(views, *v)
	}
	s.logger.Info(fmt.Sprintf(constants.LogAppealListSuccess, userID, len(views)))
	return views, nil
}

// AdminList returns appeals filtered by status (empty = all) with snapshots.
func (s *AppealService) AdminList(ctx context.Context, status string, adminID uint) ([]dto.AppealView, error) {
	if status != "" && !constants.IsAppealStatus(status) {
		return nil, util.NewAppError(400, constants.CodeBadRequest, constants.MsgAppealActionInvalid, nil)
	}
	items, err := s.appeals.ListByStatus(ctx, status)
	if err != nil {
		return nil, util.WrapAppError(fmt.Errorf("appeal admin list[status=%s]: %w", status, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	views := make([]dto.AppealView, 0, len(items))
	for i := range items {
		v, err := s.buildView(ctx, &items[i], nil, "")
		if err != nil {
			return nil, err
		}
		views = append(views, *v)
	}
	s.logger.Info(fmt.Sprintf(constants.LogAppealListSuccess, adminID, len(views)))
	return views, nil
}

// Review applies the admin decision. Approval rolls back the ACTUAL credit
// change recorded on the review (reviews.credit_delta, already clamped at
// creation), so a review that changed the score by only +2 near the ceiling is
// restored by -2 rather than the nominal +5. Rejection leaves the score
// untouched. The status flip is a compare-and-swap
// (UPDATE ... WHERE status='pending'), so concurrent reviews of the same
// appeal have exactly one winner; losers get a business conflict and never
// mutate credit.
func (s *AppealService) Review(ctx context.Context, admin *model.User, appealID uint, req *dto.ReviewAppealRequest) (*dto.AppealView, error) {
	if !constants.IsAppealAction(req.Action) {
		return nil, util.NewAppError(400, constants.CodeBadRequest, constants.MsgAppealActionInvalid, nil)
	}
	newStatus := constants.AppealStatusApproved
	if req.Action == constants.AppealActionReject {
		newStatus = constants.AppealStatusRejected
	}
	var rv *model.Review
	err := s.appeals.Transaction(ctx, func(txCtx context.Context) error {
		a, err := s.appeals.FindByIDForUpdate(txCtx, appealID)
		if err != nil {
			if errors.Is(err, util.ErrNotFound) {
				return util.NewAppError(404, constants.CodeNotFound, constants.MsgAppealNotFound, nil)
			}
			return err
		}
		review, err := s.reviews.FindByID(txCtx, a.ReviewID)
		if err != nil {
			return fmt.Errorf("appeal[id=%d] review lookup: %w", a.ID, err)
		}
		rv = review
		// Claim the appeal atomically; if another concurrent decision already
		// flipped it, affected==0 and we return without touching credit.
		affected, err := s.appeals.DecideIfPending(txCtx, a.ID, newStatus, admin.ID, strings.TrimSpace(req.Comment), time.Now())
		if err != nil {
			return fmt.Errorf("appeal[id=%d] decide: %w", a.ID, err)
		}
		if affected == 0 {
			return util.ErrConflict
		}
		if req.Action == constants.AppealActionApprove {
			// Restore exactly what the review really changed (may differ from
			// the nominal rating delta near the 0/300 bounds).
			rollback := -review.CreditDelta
			if rollback != 0 {
				if err := s.users.AddCredit(txCtx, review.RevieweeID, rollback); err != nil {
					return fmt.Errorf("appeal[id=%d] credit rollback: %w", a.ID, err)
				}
				if err := s.appeals.MarkCreditRollback(txCtx, a.ID, rollback); err != nil {
					return fmt.Errorf("appeal[id=%d] mark rollback: %w", a.ID, err)
				}
				s.logger.Info(fmt.Sprintf(constants.LogAppealCreditRollback, a.ID, review.RevieweeID, rollback))
			}
		}
		return nil
	})
	if err != nil {
		var appErr *util.AppError
		switch {
		case errors.As(err, &appErr):
			return nil, err
		case errors.Is(err, util.ErrConflict):
			return nil, util.NewAppError(409, constants.CodeConflict, constants.MsgAppealNotPending, nil)
		}
		s.logger.Error(fmt.Sprintf(constants.LogAppealReviewFailed, appealID, admin.ID, err))
		return nil, util.WrapAppError(fmt.Errorf("appeal[id=%d] review: %w", appealID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	final, ferr := s.appeals.FindByID(ctx, appealID)
	if ferr != nil {
		return nil, util.WrapAppError(fmt.Errorf("appeal[id=%d] reload: %w", appealID, ferr), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	s.logger.Info(fmt.Sprintf(constants.LogAppealReviewSuccess, final.ID, final.ReviewID, req.Action, admin.ID))
	return s.buildView(ctx, final, rv, "")
}

// buildView enriches an appeal with its appellant nickname and review snapshot.
func (s *AppealService) buildView(ctx context.Context, a *model.ReviewAppeal, rv *model.Review, appellantName string) (*dto.AppealView, error) {
	if appellantName == "" {
		u, err := s.users.FindByID(ctx, a.AppellantID)
		if err != nil {
			return nil, util.WrapAppError(fmt.Errorf("appeal[id=%d] appellant lookup: %w", a.ID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
		}
		appellantName = u.Nickname
	}
	if rv == nil {
		found, err := s.reviews.FindByID(ctx, a.ReviewID)
		if err == nil {
			rv = found
		}
	}
	return &dto.AppealView{
		ID:             a.ID,
		ReviewID:       a.ReviewID,
		AppellantID:    a.AppellantID,
		AppellantName:  appellantName,
		Reason:         a.Reason,
		Status:         a.Status,
		AdminID:        a.AdminID,
		ReviewComment:  a.ReviewComment,
		CreditReversed: a.CreditReversed,
		CreditDelta:    a.CreditDelta,
		ReviewedAt:     a.ReviewedAt,
		CreatedAt:      a.CreatedAt,
		Review:         dto.NewReviewView(rv),
	}, nil
}
