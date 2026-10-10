package knowledge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanRepoFiles_IncludesMarkdownExtension(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"guide.md":       "# Markdown\n\ncontent",
		"guide.markdown": "# Alternate\n\ncontent",
		"guide.txt":      "plain text",
		"guide.rst":      "plain rst",
		"guide.yaml":     "ignored: true",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	got, err := scanRepoFiles(root)
	if err != nil {
		t.Fatalf("scanRepoFiles: %v", err)
	}
	seen := make(map[string]bool, len(got))
	for _, file := range got {
		seen[file.URL] = true
	}
	for _, name := range []string{"guide.md", "guide.markdown", "guide.txt", "guide.rst"} {
		if !seen[name] {
			t.Errorf("scanRepoFiles omitted %q", name)
		}
	}
	if seen["guide.yaml"] {
		t.Error("scanRepoFiles indexed unsupported YAML")
	}
}
