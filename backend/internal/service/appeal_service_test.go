package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/lp/campus-market/internal/constants"
	"github.com/lp/campus-market/internal/dto"
	"github.com/lp/campus-market/internal/model"
	"github.com/lp/campus-market/internal/util"
)

// fakeAppealStore is an in-memory AppealStore for service tests.
type fakeAppealStore struct {
	appeals map[uint]*model.ReviewAppeal
	nextID  uint
}

func newFakeAppealStore() *fakeAppealStore {
	return &fakeAppealStore{appeals: map[uint]*model.ReviewAppeal{}, nextID: 1}
}

func (f *fakeAppealStore) Transaction(_ context.Context, fn func(txCtx context.Context) error) error {
	return fn(context.Background())
}

func (f *fakeAppealStore) Create(_ context.Context, a *model.ReviewAppeal) error {
	a.ID = f.nextID
	f.nextID++
	a.CreatedAt = time.Now()
	cp := *a
	f.appeals[a.ID] = &cp
	return nil
}

func (f *fakeAppealStore) FindByID(_ context.Context, id uint) (*model.ReviewAppeal, error) {
	if a, ok := f.appeals[id]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, util.ErrNotFound
}

func (f *fakeAppealStore) FindByReviewID(_ context.Context, reviewID uint) (*model.ReviewAppeal, error) {
	for _, a := range f.appeals {
		if a.ReviewID == reviewID {
			cp := *a
			return &cp, nil
		}
	}
	return nil, util.ErrNotFound
}

func (f *fakeAppealStore) FindByReviewIDForUpdate(ctx context.Context, reviewID uint) (*model.ReviewAppeal, error) {
	return f.FindByReviewID(ctx, reviewID)
}

func (f *fakeAppealStore) FindByIDForUpdate(ctx context.Context, id uint) (*model.ReviewAppeal, error) {
	return f.FindByID(ctx, id)
}

func (f *fakeAppealStore) ListByAppellant(_ context.Context, appellantID uint) ([]model.ReviewAppeal, error) {
	var out []model.ReviewAppeal
	for _, a := range f.appeals {
		if a.AppellantID == appellantID {
			out = append(out, *a)
		}
	}
	return out, nil
}

func (f *fakeAppealStore) ListByStatus(_ context.Context, status string) ([]model.ReviewAppeal, error) {
	var out []model.ReviewAppeal
	for _, a := range f.appeals {
		if status == "" || a.Status == status {
			out = append(out, *a)
		}
	}
	return out, nil
}

func (f *fakeAppealStore) SaveDecision(_ context.Context, a *model.ReviewAppeal) error {
	cp := *a
	f.appeals[a.ID] = &cp
	return nil
}

// fakeReviewStore is an in-memory AppealReviewStore for service tests.
type fakeReviewStore struct {
	reviews map[uint]*model.Review
}

func newFakeReviewStore(reviews ...model.Review) *fakeReviewStore {
	f := &fakeReviewStore{reviews: map[uint]*model.Review{}}
	for i := range reviews {
		cp := reviews[i]
		f.reviews[cp.ID] = &cp
	}
	return f
}

func (f *fakeReviewStore) FindByID(_ context.Context, id uint) (*model.Review, error) {
	if rv, ok := f.reviews[id]; ok {
		cp := *rv
		return &cp, nil
	}
	return nil, util.ErrNotFound
}

func newAppealTestService(users *fakeUserRepo, reviews AppealReviewStore, appeals AppealStore) *AppealService {
	if users == nil {
		users = newFakeUserRepo()
	}
	users.users["13700000001"] = &model.User{ID: 1, Phone: "13700000001", Nickname: "小明", Role: constants.UserRoleStudent, CreditScore: 100}
	users.users["13700000002"] = &model.User{ID: 2, Phone: "13700000002", Nickname: "阿珍", Role: constants.UserRoleStudent, CreditScore: 120}
	users.users["13800000001"] = &model.User{ID: 9, Phone: "13800000001", Nickname: "管理员", Role: constants.UserRoleAdmin, CreditScore: 300}
	if appeals == nil {
		appeals = newFakeAppealStore()
	}
	return NewAppealService(appeals, reviews, users, slog.Default())
}

func appErrCode(err error) int {
	var appErr *util.AppError
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	return -1
}

func TestAppealSubmit(t *testing.T) {
	reviews := newFakeReviewStore(
		model.Review{ID: 10, TradeID: 1, ReviewerID: 1, RevieweeID: 2, Rating: constants.ReviewRatingBad, Content: "差评测试"},
	)
	users := newFakeUserRepo()
	svc := newAppealTestService(users, reviews, nil)
	receiver := users.users["13700000002"]
	reviewer := users.users["13700000001"]

	tests := []struct {
		name     string
		user     *model.User
		reviewID uint
		reason   string
		wantErr  bool
		wantCode int
	}{
		{name: "reviewee submits", user: receiver, reviewID: 10, reason: "评价与事实不符", wantErr: false},
		{name: "duplicate appeal rejected", user: receiver, reviewID: 10, reason: "再次申诉", wantErr: true, wantCode: constants.CodeConflict},
		{name: "reviewer cannot appeal", user: reviewer, reviewID: 10, reason: "我是评价人", wantErr: true, wantCode: constants.CodeForbidden},
		{name: "review missing", user: receiver, reviewID: 999, reason: "不存在的评价也要有足够长的理由", wantErr: true, wantCode: constants.CodeNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view, err := svc.Submit(context.Background(), tt.user, &dto.CreateAppealRequest{ReviewID: tt.reviewID, Reason: tt.reason})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tt.wantCode >= 0 && appErrCode(err) != tt.wantCode {
					t.Fatalf("expected code %d, got %d (%v)", tt.wantCode, appErrCode(err), err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if view.Status != constants.AppealStatusPending {
				t.Fatalf("expected pending, got %s", view.Status)
			}
		})
	}
}

func TestAppealReviewCreditRollback(t *testing.T) {
	tests := []struct {
		name         string
		rating       string
		startScore   int
		action       string
		wantScore    int
		wantReversed bool
		wantDelta    int
		wantStatus   string
	}{
		{name: "approve good rolls back +5", rating: constants.ReviewRatingGood, startScore: 105, action: constants.AppealActionApprove, wantScore: 100, wantReversed: true, wantDelta: -5, wantStatus: constants.AppealStatusApproved},
		{name: "approve bad rolls back -10", rating: constants.ReviewRatingBad, startScore: 90, action: constants.AppealActionApprove, wantScore: 100, wantReversed: true, wantDelta: 10, wantStatus: constants.AppealStatusApproved},
		{name: "approve medium no credit change", rating: constants.ReviewRatingMedium, startScore: 100, action: constants.AppealActionApprove, wantScore: 100, wantReversed: false, wantDelta: 0, wantStatus: constants.AppealStatusApproved},
		{name: "reject keeps score", rating: constants.ReviewRatingBad, startScore: 90, action: constants.AppealActionReject, wantScore: 90, wantReversed: false, wantDelta: 0, wantStatus: constants.AppealStatusRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := newFakeUserRepo()
			users.users["13700000002"] = &model.User{ID: 2, Phone: "13700000002", Nickname: "阿珍", Role: constants.UserRoleStudent, CreditScore: tt.startScore}
			users.users["13800000001"] = &model.User{ID: 9, Phone: "13800000001", Nickname: "管理员", Role: constants.UserRoleAdmin, CreditScore: 300}
			reviews := newFakeReviewStore(
				model.Review{ID: 10, TradeID: 1, ReviewerID: 1, RevieweeID: 2, Rating: tt.rating},
			)
			appeals := newFakeAppealStore()
			svc := NewAppealService(appeals, reviews, users, slog.Default())
			receiver := users.users["13700000002"]
			admin := users.users["13800000001"]

			submitted, err := svc.Submit(context.Background(), receiver, &dto.CreateAppealRequest{ReviewID: 10, Reason: "申诉理由足够长"})
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			view, err := svc.Review(context.Background(), admin, submitted.ID, &dto.ReviewAppealRequest{Action: tt.action, Comment: "审核备注"})
			if err != nil {
				t.Fatalf("review: %v", err)
			}
			if view.Status != tt.wantStatus || view.CreditReversed != tt.wantReversed || view.CreditDelta != tt.wantDelta {
				t.Fatalf("unexpected decision: status=%s reversed=%v delta=%d", view.Status, view.CreditReversed, view.CreditDelta)
			}
			if got := users.users["13700000002"].CreditScore; got != tt.wantScore {
				t.Fatalf("credit score: want %d, got %d", tt.wantScore, got)
			}
			// A decided appeal cannot be reviewed again.
			if _, err := svc.Review(context.Background(), admin, submitted.ID, &dto.ReviewAppealRequest{Action: tt.action}); appErrCode(err) != constants.CodeConflict {
				t.Fatalf("expected conflict on second review, got %v", err)
			}
		})
	}
}

func TestAppealProgressAccess(t *testing.T) {
	reviews := newFakeReviewStore(
		model.Review{ID: 10, TradeID: 1, ReviewerID: 1, RevieweeID: 2, Rating: constants.ReviewRatingBad},
	)
	users := newFakeUserRepo()
	svc := newAppealTestService(users, reviews, nil)
	receiver := users.users["13700000002"]
	other := users.users["13700000001"]

	submitted, err := svc.Submit(context.Background(), receiver, &dto.CreateAppealRequest{ReviewID: 10, Reason: "申诉理由足够长"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := svc.GetProgress(context.Background(), receiver.ID, submitted.ID); err != nil {
		t.Fatalf("owner should read progress: %v", err)
	}
	if _, err := svc.GetProgress(context.Background(), other.ID, submitted.ID); appErrCode(err) != constants.CodeForbidden {
		t.Fatalf("non-owner should be forbidden, got %v", err)
	}
	if _, err := svc.GetProgress(context.Background(), receiver.ID, 999); appErrCode(err) != constants.CodeNotFound {
		t.Fatalf("missing appeal should be 404, got %v", err)
	}
	list, err := svc.ListMine(context.Background(), receiver.ID)
	if err != nil || len(list) != 1 || list[0].Review == nil || list[0].Review.Rating != constants.ReviewRatingBad {
		t.Fatalf("list mine failed: %v list=%v", err, list)
	}
}

func TestAppealAdminList(t *testing.T) {
	reviews := newFakeReviewStore(
		model.Review{ID: 10, TradeID: 1, ReviewerID: 1, RevieweeID: 2, Rating: constants.ReviewRatingGood},
		model.Review{ID: 11, TradeID: 2, ReviewerID: 1, RevieweeID: 2, Rating: constants.ReviewRatingBad},
	)
	users := newFakeUserRepo()
	svc := newAppealTestService(users, reviews, nil)
	receiver := users.users["13700000002"]
	admin := users.users["13800000001"]

	first, err := svc.Submit(context.Background(), receiver, &dto.CreateAppealRequest{ReviewID: 10, Reason: "第一条申诉理由"})
	if err != nil {
		t.Fatalf("submit 1: %v", err)
	}
	if _, err := svc.Submit(context.Background(), receiver, &dto.CreateAppealRequest{ReviewID: 11, Reason: "第二条申诉理由"}); err != nil {
		t.Fatalf("submit 2: %v", err)
	}
	if _, err := svc.Review(context.Background(), admin, first.ID, &dto.ReviewAppealRequest{Action: constants.AppealActionApprove}); err != nil {
		t.Fatalf("review: %v", err)
	}
	pending, err := svc.AdminList(context.Background(), constants.AppealStatusPending, admin.ID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending list: %v %v", pending, err)
	}
	all, err := svc.AdminList(context.Background(), "", admin.ID)
	if err != nil || len(all) != 2 {
		t.Fatalf("all list: %v %v", all, err)
	}
}
