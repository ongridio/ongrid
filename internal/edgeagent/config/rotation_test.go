package config

import (
	"testing"
	"time"
)

// TestCheckTokenRotation_NotExpired 验证未过期返回 false。
func TestCheckTokenRotation_NotExpired(t *testing.T) {
	cases := []struct {
		name         string
		tokenAge     time.Duration
		rotationDays int
	}{
		{"89_days_default_90", 89 * 24 * time.Hour, 90},
		{"1_day_default_90", 1 * 24 * time.Hour, 90},
		{"0_age", 0, 90},
		{"29_days_custom_30", 29 * 24 * time.Hour, 30},
		{"364_days_custom_365", 364 * 24 * time.Hour, 365},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if CheckTokenRotation(tc.tokenAge, tc.rotationDays) {
				t.Errorf("CheckTokenRotation(%v, %d) = true, want false", tc.tokenAge, tc.rotationDays)
			}
		})
	}
}

// TestCheckTokenRotation_Expired 验证过期返回 true。
func TestCheckTokenRotation_Expired(t *testing.T) {
	cases := []struct {
		name         string
		tokenAge     time.Duration
		rotationDays int
	}{
		{"90_days_default_90", 90 * 24 * time.Hour, 90},
		{"91_days_default_90", 91 * 24 * time.Hour, 90},
		{"365_days_default_90", 365 * 24 * time.Hour, 90},
		{"30_days_custom_30", 30 * 24 * time.Hour, 30},
		{"365_days_custom_365", 365 * 24 * time.Hour, 365},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !CheckTokenRotation(tc.tokenAge, tc.rotationDays) {
				t.Errorf("CheckTokenRotation(%v, %d) = false, want true", tc.tokenAge, tc.rotationDays)
			}
		})
	}
}

// TestCheckTokenRotation_ClampDays 验证 rotationDays 钳制到 [30, 365]。
func TestCheckTokenRotation_ClampDays(t *testing.T) {
	// 负值 → 默认 90
	if CheckTokenRotation(89*24*time.Hour, -1) {
		t.Error("negative days should clamp to 90, 89 days should not rotate")
	}
	if !CheckTokenRotation(90*24*time.Hour, -1) {
		t.Error("negative days should clamp to 90, 90 days should rotate")
	}

	// 0 → 默认 90
	if CheckTokenRotation(89*24*time.Hour, 0) {
		t.Error("zero days should clamp to 90, 89 days should not rotate")
	}
	if !CheckTokenRotation(90*24*time.Hour, 0) {
		t.Error("zero days should clamp to 90, 90 days should rotate")
	}

	// <30 → 钳制到 30
	if CheckTokenRotation(29*24*time.Hour, 10) {
		t.Error("10 days should clamp to 30, 29 days should not rotate")
	}
	if !CheckTokenRotation(30*24*time.Hour, 10) {
		t.Error("10 days should clamp to 30, 30 days should rotate")
	}

	// >365 → 钳制到 365
	if CheckTokenRotation(364*24*time.Hour, 999) {
		t.Error("999 days should clamp to 365, 364 days should not rotate")
	}
	if !CheckTokenRotation(365*24*time.Hour, 999) {
		t.Error("999 days should clamp to 365, 365 days should rotate")
	}
}

// TestClampRotationDays 验证 clampRotationDays 独立函数。
func TestClampRotationDays(t *testing.T) {
	cases := []struct {
		input int
		want  int
	}{
		{-1, 90},       // 负值 → 默认 90
		{0, 90},        // 0 → 默认 90
		{1, 30},        // <30 → 30
		{29, 30},       // <30 → 30
		{30, 30},       // 边界 → 30
		{90, 90},       // 默认值
		{180, 180},     // 正常值
		{365, 365},     // 边界 → 365
		{366, 365},     // >365 → 365
		{9999, 365},    // 远超上限 → 365
	}
	for _, tc := range cases {
		t.Run("", func(t *testing.T) {
			got := clampRotationDays(tc.input)
			if got != tc.want {
				t.Errorf("clampRotationDays(%d) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}
