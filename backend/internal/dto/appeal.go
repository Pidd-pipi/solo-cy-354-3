package dto

import (
	"time"

	"github.com/lp/campus-market/internal/model"
)

// CreateAppealRequest is the payload for appealing a received review.
// The reason is trimmed and length-checked in the service so that whitespace-
// only submissions are rejected with a specific business message.
type CreateAppealRequest struct {
	ReviewID uint   `json:"review_id" binding:"required"`
	Reason   string `json:"reason" binding:"required,max=500"`
}

// ReviewAppealRequest is the admin payload for approving/rejecting an appeal.
type ReviewAppealRequest struct {
	Action  string `json:"action" binding:"required,oneof=approve reject"`
	Comment string `json:"comment" binding:"max=500"`
}

// AppealView is the appeal projection returned on all appeal endpoints.
type AppealView struct {
	ID             uint        `json:"id"`
	ReviewID       uint        `json:"review_id"`
	AppellantID    uint        `json:"appellant_id"`
	AppellantName  string      `json:"appellant_name"`
	Reason         string      `json:"reason"`
	Status         string      `json:"status"`
	AdminID        *uint       `json:"admin_id"`
	ReviewComment  string      `json:"review_comment"`
	CreditReversed bool        `json:"credit_reversed"`
	CreditDelta    int         `json:"credit_delta"`
	ReviewedAt     *time.Time  `json:"reviewed_at"`
	CreatedAt      time.Time   `json:"created_at"`
	Review         *ReviewView `json:"review,omitempty"`
}

// ReviewView is the embedded review snapshot inside an appeal view.
type ReviewView struct {
	ID         uint      `json:"id"`
	TradeID    uint      `json:"trade_id"`
	ReviewerID uint      `json:"reviewer_id"`
	RevieweeID uint      `json:"reviewee_id"`
	Rating     string    `json:"rating"`
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"created_at"`
}

// NewReviewView maps a review model to its API projection.
func NewReviewView(rv *model.Review) *ReviewView {
	if rv == nil {
		return nil
	}
	return &ReviewView{
		ID:         rv.ID,
		TradeID:    rv.TradeID,
		ReviewerID: rv.ReviewerID,
		RevieweeID: rv.RevieweeID,
		Rating:     rv.Rating,
		Content:    rv.Content,
		CreatedAt:  rv.CreatedAt,
	}
}
