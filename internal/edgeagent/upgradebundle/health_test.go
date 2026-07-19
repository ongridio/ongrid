package upgradebundle

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteHealthyMarker_WritesVersion 验证正常路径：写 healthy_marker 文件，
// 内容 = version + "\n"，权限 0640。
func TestWriteHealthyMarker_WritesVersion(t *testing.T) {
	dir := t.TempDir()

	if err := WriteHealthyMarker(dir, "v0.7.50"); err != nil {
		t.Fatalf("WriteHealthyMarker: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, HealthyMarkerFile))
	if err != nil {
		t.Fatalf("read healthy_marker: %v", err)
	}
	if string(got) != "v0.7.50\n" {
		t.Errorf("healthy_marker = %q, want %q", got, "v0.7.50\n")
	}
}

// TestWriteHealthyMarker_EmptyVersion 验证空版本号幂等跳过（不报错、不写文件）。
func TestWriteHealthyMarker_EmptyVersion(t *testing.T) {
	dir := t.TempDir()

	if err := WriteHealthyMarker(dir, ""); err != nil {
		t.Fatalf("empty version should not error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, HealthyMarkerFile)); !os.IsNotExist(err) {
		t.Error("healthy_marker should not be written for empty version")
	}
}

// TestWriteHealthyMarker_CreatesDir 验证 stage dir 不存在时自动创建。
func TestWriteHealthyMarker_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "upgrade")

	if err := WriteHealthyMarker(dir, "v1.0.0"); err != nil {
		t.Fatalf("should create nested dir: %v", err)
	}
}

// TestWriteHealthyMarker_OverwritesExisting 验证幂等覆盖（多次 register_edge 场景）。
func TestWriteHealthyMarker_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()

	if err := WriteHealthyMarker(dir, "v0.7.49"); err != nil {
		t.Fatal(err)
	}
	if err := WriteHealthyMarker(dir, "v0.7.50"); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, HealthyMarkerFile))
	if string(got) != "v0.7.50\n" {
		t.Errorf("after overwrite = %q, want %q", got, "v0.7.50\n")
	}
}

// TestDeleteRollbackSentinel_RemovesExisting 验证存在时删除。
func TestDeleteRollbackSentinel_RemovesExisting(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, RollbackDoneFile)
	if err := os.WriteFile(sentinel, []byte("done\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := DeleteRollbackSentinel(dir); err != nil {
		t.Fatalf("DeleteRollbackSentinel: %v", err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Error("rollback.done should be removed")
	}
}

// TestDeleteRollbackSentinel_Idempotent 验证不存在时幂等（不报错）。
func TestDeleteRollbackSentinel_Idempotent(t *testing.T) {
	dir := t.TempDir()
	// rollback.done 不存在

	if err := DeleteRollbackSentinel(dir); err != nil {
		t.Fatalf("should not error when sentinel missing: %v", err)
	}
}
