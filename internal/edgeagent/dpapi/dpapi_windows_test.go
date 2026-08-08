//go:build windows

package dpapi

import (
	"bytes"
	"strings"
	"testing"
)

// TestProtect_Unprotect_RoundTrip 验证加密→解密还原原始数据。
func TestProtect_Unprotect_RoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		plaintext []byte
	}{
		{"short_string", []byte("hello world")},
		{"broker_token_like", []byte("ed_bk_abc123xyz789")},
		{"unicode", []byte("中文token")},
		{"binary_data", []byte{0x00, 0x01, 0xFF, 0xFE, 0x80, 0x7F}},
		{"exact_1kb", bytes.Repeat([]byte("A"), 1024)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encrypted, err := Protect(tc.plaintext)
			if err != nil {
				t.Fatalf("Protect failed: %v", err)
			}
			if bytes.Equal(encrypted, tc.plaintext) {
				t.Error("encrypted output should differ from plaintext")
			}
			decrypted, err := Unprotect(encrypted)
			if err != nil {
				t.Fatalf("Unprotect failed: %v", err)
			}
			if !bytes.Equal(decrypted, tc.plaintext) {
				t.Errorf("round-trip mismatch:\n  want %x\n  got  %x", tc.plaintext, decrypted)
			}
		})
	}
}

// TestProtect_EmptyInput_ReturnsError 验证空输入返回错误。
func TestProtect_EmptyInput_ReturnsError(t *testing.T) {
	_, err := Protect(nil)
	if err == nil {
		t.Fatal("expected error for nil input, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention empty, got: %v", err)
	}

	_, err = Protect([]byte{})
	if err == nil {
		t.Fatal("expected error for empty slice, got nil")
	}
}

// TestUnprotect_EmptyInput_ReturnsError 验证空密文返回错误。
func TestUnprotect_EmptyInput_ReturnsError(t *testing.T) {
	_, err := Unprotect(nil)
	if err == nil {
		t.Fatal("expected error for nil input, got nil")
	}
	_, err = Unprotect([]byte{})
	if err == nil {
		t.Fatal("expected error for empty slice, got nil")
	}
}

// TestProtect_LargeInput 验证大块数据（1MB+）正确加密解密。
func TestProtect_LargeInput(t *testing.T) {
	plaintext := bytes.Repeat([]byte("ONGRID"), 200*1024) // ~1.2MB
	encrypted, err := Protect(plaintext)
	if err != nil {
		t.Fatalf("Protect 1MB+ failed: %v", err)
	}
	decrypted, err := Unprotect(encrypted)
	if err != nil {
		t.Fatalf("Unprotect 1MB+ failed: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("large input round-trip mismatch (len want=%d got=%d)", len(plaintext), len(decrypted))
	}
}

// TestUnprotect_InvalidPayload_ReturnsError 验证损坏数据返回错误。
func TestUnprotect_InvalidPayload_ReturnsError(t *testing.T) {
	// 完全无效的 payload
	_, err := Unprotect([]byte("not a valid dpapi blob"))
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	// 篡改有效密文
	original := []byte("secret token data")
	encrypted, err := Protect(original)
	if err != nil {
		t.Fatalf("Protect failed: %v", err)
	}
	tampered := make([]byte, len(encrypted))
	copy(tampered, encrypted)
	// 翻转最后一个字节
	tampered[len(tampered)-1] ^= 0xFF
	_, err = Unprotect(tampered)
	if err == nil {
		t.Error("expected error for tampered ciphertext, got nil")
	}
}

// TestProtect_OutputIsNonDeterministic 验证同一明文加密两次产生不同密文
// （DPAPI 使用随机 IV，确保相同输入不产生相同输出）。
func TestProtect_OutputIsNonDeterministic(t *testing.T) {
	plaintext := []byte("same input twice")
	enc1, err := Protect(plaintext)
	if err != nil {
		t.Fatalf("first Protect failed: %v", err)
	}
	enc2, err := Protect(plaintext)
	if err != nil {
		t.Fatalf("second Protect failed: %v", err)
	}
	if bytes.Equal(enc1, enc2) {
		t.Error("DPAPI output should be non-deterministic (random IV), but two calls produced identical output")
	}

	// 两者都应能正确解密
	dec1, err := Unprotect(enc1)
	if err != nil {
		t.Fatalf("Unprotect enc1 failed: %v", err)
	}
	dec2, err := Unprotect(enc2)
	if err != nil {
		t.Fatalf("Unprotect enc2 failed: %v", err)
	}
	if !bytes.Equal(dec1, plaintext) || !bytes.Equal(dec2, plaintext) {
		t.Error("both non-deterministic outputs should decrypt to original")
	}
}

// TestProtect_LocalMachineScope 验证使用 CRYPTPROTECT_LOCAL_MACHINE flag。
//
// LOCAL_MACHINE scope 的语义：加密绑定到机器而非用户 logon session。
// 本测试验证加密结果可以被同机器上的当前进程解密（基础验证）。
// 跨用户/跨机器解密失败是 adversarial test，需要多用户/多机器 CI 环境验证。
func TestProtect_LocalMachineScope(t *testing.T) {
	plaintext := []byte("machine-bound secret")
	encrypted, err := Protect(plaintext)
	if err != nil {
		t.Fatalf("Protect failed: %v", err)
	}

	// 同一进程解密应成功（LOCAL_MACHINE scope 下，同一机器的 System 进程都能解密）
	decrypted, err := Unprotect(encrypted)
	if err != nil {
		t.Fatalf("Unprotect failed: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("LOCAL_MACHINE round-trip mismatch")
	}
}

// TestProtect_DoesNotLeakPlaintextInOutput 验证加密输出不含明文。
func TestProtect_DoesNotLeakPlaintextInOutput(t *testing.T) {
	plaintext := []byte("supersecret_broker_token_12345")
	encrypted, err := Protect(plaintext)
	if err != nil {
		t.Fatalf("Protect failed: %v", err)
	}
	if bytes.Contains(encrypted, plaintext) {
		t.Error("encrypted output contains plaintext — DPAPI encryption failed to protect data")
	}
}
