package imbridge

import "time"

// DeliveryError 仅包含可安全记录的分类；不要加入原始响应、token 或 webhook。
type DeliveryError struct {
	Code       string
	RetryAfter time.Duration
	CannotEdit bool
	Permanent  bool
}

func (e *DeliveryError) Error() string { return "IM delivery: " + e.Code }
