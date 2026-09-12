package constants

// AppealStatus defines credit-appeal state machine values shared with the frontend.
const (
	AppealStatusPending  = "pending"
	AppealStatusApproved = "approved"
	AppealStatusRejected = "rejected"
)

// AppealStatuses lists all valid appeal statuses in flow order.
var AppealStatuses = []string{
	AppealStatusPending, AppealStatusApproved, AppealStatusRejected,
}

// IsAppealStatus reports whether the given status is valid.
func IsAppealStatus(s string) bool {
	for _, v := range AppealStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// AppealStatusText returns the Chinese label of an appeal status.
func AppealStatusText(s string) string {
	switch s {
	case AppealStatusPending:
		return "待审核"
	case AppealStatusApproved:
		return "已通过"
	case AppealStatusRejected:
		return "已驳回"
	default:
		return "未知"
	}
}

// AppealAction defines admin review actions shared with the frontend.
const (
	AppealActionApprove = "approve"
	AppealActionReject  = "reject"
)

// AppealActions lists all valid admin actions.
var AppealActions = []string{AppealActionApprove, AppealActionReject}

// IsAppealAction reports whether the given review action is valid.
func IsAppealAction(a string) bool {
	for _, v := range AppealActions {
		if v == a {
			return true
		}
	}
	return false
}
