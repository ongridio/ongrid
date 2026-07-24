package config

import "time"

// token 轮转周期常量（ADR-037 A2 CR4）。
const (
	defaultRotationDays = 90
	minRotationDays     = 30
	maxRotationDays     = 365
)

// CheckTokenRotation 检查 token 是否需要轮转。
//
// tokenAge 是当前 token 的存活时间（通常从 secrets.enc 文件修改时间计算）。
// rotationDays 是配置的轮转周期，钳制到 [30, 365]，0 或负值用默认 90。
//
// 返回 true 表示需要轮转（tokenAge >= rotationDays 天）。
func CheckTokenRotation(tokenAge time.Duration, rotationDays int) bool {
	threshold := time.Duration(clampRotationDays(rotationDays)) * 24 * time.Hour
	return tokenAge >= threshold
}

// clampRotationDays 将 rotationDays 钳制到 [30, 365]。
// 0 或负值 → 默认 90；<30 → 30；>365 → 365。
func clampRotationDays(days int) int {
	if days <= 0 {
		return defaultRotationDays
	}
	if days < minRotationDays {
		return minRotationDays
	}
	if days > maxRotationDays {
		return maxRotationDays
	}
	return days
}
