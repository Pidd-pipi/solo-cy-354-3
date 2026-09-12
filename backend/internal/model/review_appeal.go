package model

import "time"

// ReviewAppeal is a credit appeal filed by the receiver of a review.
// Exactly one appeal is allowed per review; on approval the credit
// change caused by the underlying review is rolled back.
type ReviewAppeal struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	ReviewID       uint       `gorm:"uniqueIndex;not null" json:"review_id"`
	AppellantID    uint       `gorm:"index;not null" json:"appellant_id"`
	Reason         string     `gorm:"type:text;not null" json:"reason"`
	Status         string     `gorm:"size:16;index;not null;default:pending" json:"status"`
	AdminID        *uint      `gorm:"index" json:"admin_id"`
	ReviewComment  string     `gorm:"type:text" json:"review_comment"`
	CreditReversed bool       `gorm:"not null;default:false" json:"credit_reversed"`
	CreditDelta    int        `gorm:"not null;default:0" json:"credit_delta"`
	ReviewedAt     *time.Time `json:"reviewed_at"`
	CreatedAt      time.Time  `json:"created_at"`
}
