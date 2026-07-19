//go:build !windows

// Package dpapi 提供 DPAPI（Data Protection API）加密/解密功能。
//
// 在非 Windows 平台上，所有操作返回 ErrUnsupportedPlatform。
// 这使得 manager（Linux）可以引用 dpapi 包的符号而不编译失败。
package dpapi

import "errors"

// ErrUnsupportedPlatform 表示当前平台不支持 DPAPI（仅 Windows 可用）。
var ErrUnsupportedPlatform = errors.New("dpapi: unsupported platform (Windows only)")

// Protect 在非 Windows 平台始终返回 ErrUnsupportedPlatform。
func Protect(_ []byte) ([]byte, error) {
	return nil, ErrUnsupportedPlatform
}

// Unprotect 在非 Windows 平台始终返回 ErrUnsupportedPlatform。
func Unprotect(_ []byte) ([]byte, error) {
	return nil, ErrUnsupportedPlatform
}
