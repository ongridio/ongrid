package enrollment

import (
	"path/filepath"
	"testing"
)

func TestWriteEnrollmentEnvFileRejectsNewline(t *testing.T) {
	err := writeEnrollmentEnvFile(filepath.Join(t.TempDir(), "edge.env"), edgeEnrollResponse{
		EdgeID:           1,
		AccessKey:        "access",
		SecretKey:        "secret\ninjected=value",
		CloudAddr:        "manager:40012",
		ManagerPublicURL: "https://manager",
	})
	if err == nil {
		t.Fatal("expected invalid environment value error")
	}
}
