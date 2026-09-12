package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/lp/campus-market/internal/constants"
	"github.com/lp/campus-market/internal/dto"
	"github.com/lp/campus-market/internal/model"
	"github.com/lp/campus-market/internal/util"
)

// fakeAppealStore is an in-memory AppealStore for service tests. It models the
// real repository's atomic guards (unique review_id + CAS on pending) with a
// mutex, so concurrent service calls behave like they would against MySQL.
type fakeAppealStore struct {
	mu      sync.Mutex
	appeals map[uint]*model.ReviewAppeal
	nextID  uint
}

func newFakeAppealStore() *fakeAppealStore {
	return &fakeAppealStore{appeals: map[uint]*model.ReviewAppeal{}, nextID: 1}
}

func (f *fakeAppealStore) Transaction(_ context.Context, fn func(txCtx context.Context) error) error {
	return fn(context.Background())
}

func (f *fakeAppealStore) CreateIfAbsent(_ context.Context, a *model.ReviewAppeal) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ex := range f.appeals {
		if ex.ReviewID == a.ReviewID {
			return false, nil
		}
	}
	a.ID = f.nextID
	f.nextID++
	a.CreatedAt = time.Now()
	cp := *a
	f.appeals[a.ID] = &cp
	return true, nil
}

func (f *fakeAppealStore) FindByID(_ context.Context, id uint) (*model.ReviewAppeal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.appeals[id]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, util.ErrNotFound
}

func (f *fakeAppealStore) FindByIDForUpdate(ctx context.Context, id uint) (*model.ReviewAppeal, error) {
	return f.FindByID(ctx, id)
}

func (f *fakeAppealStore) ListByAppellant(_ context.Context, appellantID uint) ([]model.ReviewAppeal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.ReviewAppeal
	for _, a := range f.appeals {
		if a.AppellantID == appellantID {
			out = append(out, *a)
		}
	}
	return out, nil
}

func (f *fakeAppealStore) ListByStatus(_ context.Context, status string) ([]model.ReviewAppeal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.ReviewAppeal
	for _, a := range f.appeals {
		if status == "" || a.Status == status {
			out = append(out, *a)
		}
	}
	return out, nil
}

// DecideIfPending mirrors UPDATE ... WHERE status='pending': only the first
// concurrent caller flips the row.
func (f *fakeAppealStore) DecideIfPending(_ context.Context, id uint, status string, adminID uint, comment string, reviewedAt time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.appeals[id]
	if !ok || a.Status != constants.AppealStatusPending {
		return 0, nil
	}
	a.Status = status
	a.AdminID = &adminID
	a.ReviewComment = comment
	a.ReviewedAt = &reviewedAt
	return 1, nil
}

func (f *fakeAppealStore) MarkCreditRollback(_ context.Context, id uint, delta int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.appeals[id]; ok {
		a.CreditReversed = true
		a.CreditDelta = delta
	}
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
		{name: "blank spaces rejected", user: receiver, reviewID: 10, reason: "    ", wantErr: true, wantCode: constants.CodeValidation},
		{name: "tabs and newlines rejected", user: receiver, reviewID: 10, reason: "\t\n  \n", wantErr: true, wantCode: constants.CodeValidation},
		{name: "reviewee submits", user: receiver, reviewID: 10, reason: "  评价与事实不符  ", wantErr: false},
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
			if view.Reason != "评价与事实不符" {
				t.Fatalf("reason must be trimmed, got %q", view.Reason)
			}
		})
	}
}

func TestAppealReviewCreditRollback(t *testing.T) {
	tests := []struct {
		name         string
		rating       string
		startScore   int
		actualDelta  int // delta the review REALLY applied after clamping
		action       string
		wantScore    int
		wantReversed bool
		wantDelta    int
		wantStatus   string
	}{
		{name: "approve good rolls back +5", rating: constants.ReviewRatingGood, startScore: 100, actualDelta: 5, action: constants.AppealActionApprove, wantScore: 100, wantReversed: true, wantDelta: -5, wantStatus: constants.AppealStatusApproved},
		{name: "approve bad rolls back -10", rating: constants.ReviewRatingBad, startScore: 100, actualDelta: -10, action: constants.AppealActionApprove, wantScore: 100, wantReversed: true, wantDelta: 10, wantStatus: constants.AppealStatusApproved},
		{name: "approve medium no credit change", rating: constants.ReviewRatingMedium, startScore: 100, actualDelta: 0, action: constants.AppealActionApprove, wantScore: 100, wantReversed: false, wantDelta: 0, wantStatus: constants.AppealStatusApproved},
		{name: "reject keeps score", rating: constants.ReviewRatingBad, startScore: 100, actualDelta: -10, action: constants.AppealActionReject, wantScore: 90, wantReversed: false, wantDelta: 0, wantStatus: constants.AppealStatusRejected},
		{name: "floor: bad review applied -4 only, approval restores +4", rating: constants.ReviewRatingBad, startScore: 4, actualDelta: -4, action: constants.AppealActionApprove, wantScore: 4, wantReversed: true, wantDelta: 4, wantStatus: constants.AppealStatusApproved},
		{name: "floor at zero: bad review changed nothing", rating: constants.ReviewRatingBad, startScore: 0, actualDelta: 0, action: constants.AppealActionApprove, wantScore: 0, wantReversed: false, wantDelta: 0, wantStatus: constants.AppealStatusApproved},
		{name: "ceiling: good review applied +2 only, approval restores -2", rating: constants.ReviewRatingGood, startScore: 298, actualDelta: 2, action: constants.AppealActionApprove, wantScore: 298, wantReversed: true, wantDelta: -2, wantStatus: constants.AppealStatusApproved},
		{name: "ceiling at 300: good review changed nothing", rating: constants.ReviewRatingGood, startScore: 300, actualDelta: 0, action: constants.AppealActionApprove, wantScore: 300, wantReversed: false, wantDelta: 0, wantStatus: constants.AppealStatusApproved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := newFakeUserRepo()
			// The review already happened: current score is start+actual (clamped).
			currentScore := util.ClampCredit(tt.startScore + tt.actualDelta)
			users.users["13700000002"] = &model.User{ID: 2, Phone: "13700000002", Nickname: "阿珍", Role: constants.UserRoleStudent, CreditScore: currentScore}
			users.users["13800000001"] = &model.User{ID: 9, Phone: "13800000001", Nickname: "管理员", Role: constants.UserRoleAdmin, CreditScore: 300}
			reviews := newFakeReviewStore(
				model.Review{ID: 10, TradeID: 1, ReviewerID: 1, RevieweeID: 2, Rating: tt.rating, CreditDelta: tt.actualDelta},
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

func TestAppliedDelta(t *testing.T) {
	tests := []struct {
		score, nominal, want int
	}{
		{100, 5, 5}, {100, -10, -10}, {4, -10, -4}, {0, -10, 0}, {298, 5, 2}, {300, 5, 0}, {300, -10, -10},
	}
	for _, tt := range tests {
		if got := util.AppliedDelta(tt.score, tt.nominal); got != tt.want {
			t.Fatalf("AppliedDelta(%d,%d)=%d want %d", tt.score, tt.nominal, got, tt.want)
		}
	}
}

// TestAppealConcurrentSubmit fires N concurrent submits for the same review
// and requires exactly one success and N-1 business conflicts.
func TestAppealConcurrentSubmit(t *testing.T) {
	reviews := newFakeReviewStore(
		model.Review{ID: 10, TradeID: 1, ReviewerID: 1, RevieweeID: 2, Rating: constants.ReviewRatingBad, CreditDelta: -10},
	)
	users := newFakeUserRepo()
	svc := newAppealTestService(users, reviews, nil)
	receiver := users.users["13700000002"]

	const n = 20
	var wg sync.WaitGroup
	var okN, conflictN, otherN int64
	var mu sync.Mutex
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Submit(context.Background(), receiver, &dto.CreateAppealRequest{ReviewID: 10, Reason: "并发申诉理由"})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				okN++
			case appErrCode(err) == constants.CodeConflict:
				conflictN++
			default:
				otherN++
			}
		}()
	}
	close(start)
	wg.Wait()
	if okN != 1 || conflictN != n-1 || otherN != 0 {
		t.Fatalf("want 1 ok / %d conflict / 0 other, got %d/%d/%d", n-1, okN, conflictN, otherN)
	}
	list, _ := svc.ListMine(context.Background(), receiver.ID)
	if len(list) != 1 {
		t.Fatalf("exactly one appeal row expected, got %d", len(list))
	}
}

// TestAppealConcurrentReview fires N concurrent admin decisions and requires
// exactly one to take effect; the credit rollback happens exactly once.
func TestAppealConcurrentReview(t *testing.T) {
	reviews := newFakeReviewStore(
		model.Review{ID: 10, TradeID: 1, ReviewerID: 1, RevieweeID: 2, Rating: constants.ReviewRatingBad, CreditDelta: -10},
	)
	users := newFakeUserRepo()
	svc := newAppealTestService(users, reviews, nil)
	receiver := users.users["13700000002"]
	admin := users.users["13800000001"]
	scoreBefore := receiver.CreditScore // 120 seeded by newAppealTestService; review applied -10 -> 110
	receiver.CreditScore = 110

	submitted, err := svc.Submit(context.Background(), receiver, &dto.CreateAppealRequest{ReviewID: 10, Reason: "并发审核理由"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	const n = 20
	var wg sync.WaitGroup
	var okN, conflictN, otherN int64
	var mu sync.Mutex
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Review(context.Background(), admin, submitted.ID, &dto.ReviewAppealRequest{Action: constants.AppealActionApprove})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				okN++
			case appErrCode(err) == constants.CodeConflict:
				conflictN++
			default:
				otherN++
			}
		}()
	}
	close(start)
	wg.Wait()
	if okN != 1 || conflictN != n-1 || otherN != 0 {
		t.Fatalf("want 1 ok / %d conflict / 0 other, got %d/%d/%d", n-1, okN, conflictN, otherN)
	}
	// 110 + rollback(+10) = 120 exactly once.
	if got := users.users["13700000002"].CreditScore; got != scoreBefore {
		t.Fatalf("rollback must apply once: want %d, got %d", scoreBefore, got)
	}
	final, _ := svc.GetProgress(context.Background(), receiver.ID, submitted.ID)
	if final.Status != constants.AppealStatusApproved || !final.CreditReversed || final.CreditDelta != 10 {
		t.Fatalf("unexpected final state: %+v", final)
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
