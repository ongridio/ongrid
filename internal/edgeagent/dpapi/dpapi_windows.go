//go:build windows

// Package dpapi 提供 DPAPI（Data Protection API）加密/解密功能，
// 用于保护 broker token 等敏感凭证（ADR-037 A2 CR4）。
//
// DPAPI scope：CRYPTPROTECT_LOCAL_MACHINE（绑定机器 + SystemCredential scope）。
//   - LocalSystem 和 NetworkService 都能解密（同为 System 身份）
//   - 拷贝到其他机器无法解密（防横向移动）
//   - 不防本机 System 服务被攻破（威胁模型见 ADR-037）
//
// 使用 golang.org/x/sys/windows 的高层 CryptProtectData/CryptUnprotectData 包装，
// 无需 raw syscall。
package dpapi

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// CRYPTPROTECT_LOCAL_MACHINE 绑定密文到机器而非用户 logon session。
// Windows Service 跑在 session 0，没有交互式 user logon，必须用此 flag。
const CRYPTPROTECT_LOCAL_MACHINE = 0x1

// Protect 使用 DPAPI 加密数据（CRYPTPROTECT_LOCAL_MACHINE scope）。
//
// 加密后的 blob 只能在同一机器上由 System 身份进程解密。
// 同一明文每次加密产生不同密文（DPAPI 使用随机 IV）。
func Protect(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, fmt.Errorf("dpapi: Protect: empty plaintext")
	}

	dataIn := windows.DataBlob{
		Size: uint32(len(plaintext)),
		Data: &plaintext[0],
	}
	var dataOut windows.DataBlob

	err := windows.CryptProtectData(
		&dataIn,
		nil, // name（可选描述，不设）
		nil, // optionalEntropy（不使用额外熵）
		0,   // reserved
		nil, // promptStruct（不弹 UI）
		CRYPTPROTECT_LOCAL_MACHINE,
		&dataOut,
	)
	if err != nil {
		return nil, fmt.Errorf("dpapi: CryptProtectData: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(dataOut.Data)))

	return blobToBytes(dataOut), nil
}

// Unprotect 使用 DPAPI 解密数据（需与加密时相同的机器 + System 身份）。
func Unprotect(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, fmt.Errorf("dpapi: Unprotect: empty ciphertext")
	}

	dataIn := windows.DataBlob{
		Size: uint32(len(ciphertext)),
		Data: &ciphertext[0],
	}
	var dataOut windows.DataBlob

	err := windows.CryptUnprotectData(
		&dataIn,
		nil, // name（可选描述指针，不接收）
		nil, // optionalEntropy
		0,   // reserved
		nil, // promptStruct
		0,   // flags（解密时不需要 flag）
		&dataOut,
	)
	if err != nil {
		return nil, fmt.Errorf("dpapi: CryptUnprotectData: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(dataOut.Data)))

	return blobToBytes(dataOut), nil
}

// blobToBytes 将 windows.DataBlob 转为 Go []byte（拷贝，不持有原生内存）。
func blobToBytes(blob windows.DataBlob) []byte {
	if blob.Data == nil || blob.Size == 0 {
		return nil
	}
	// unsafe.Slice 创建指向原生内存的切片，copy 拷贝到 Go 堆
	src := unsafe.Slice(blob.Data, blob.Size)
	result := make([]byte, blob.Size)
	copy(result, src)
	return result
}
