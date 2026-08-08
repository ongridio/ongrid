package docextract

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDOCX2MD_WithEmbeddedImage_DoesNotWriteFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	document := `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="urn:w" xmlns:a="urn:a" xmlns:r="urn:r">
  <w:body><w:p><w:r><w:t>before</w:t></w:r><a:blip r:embed="rId1"/><w:r><w:t>after</w:t></w:r></w:p></w:body>
</w:document>`
	rels := `<?xml version="1.0" encoding="UTF-8"?>
<Relationships><Relationship Id="rId1" Type="image" Target="media/image1.png"/></Relationships>`
	data := testDOCX(t, map[string]string{
		"word/document.xml":            document,
		"word/_rels/document.xml.rels": rels,
		"word/media/image1.png":        "image bytes",
		"[Content_Types].xml":          `<Types/>`,
		"_rels/.rels":                  `<Relationships/>`,
	})

	got, err := docx2md(data)
	if err != nil {
		t.Fatalf("docx2md: %v", err)
	}
	if !strings.Contains(got, "beforeafter") {
		t.Fatalf("surrounding text missing: %q", got)
	}
	if _, err := os.Stat(filepath.Join("media", "image1.png")); !os.IsNotExist(err) {
		t.Fatalf("embedded image was written to working directory: %v", err)
	}
}

func TestDOCX2MD_WhenDocumentXMLExceedsLimit_ReturnsError(t *testing.T) {
	document := `<w:document xmlns:w="urn:w"><w:body><w:p><w:r><w:t>` +
		strings.Repeat("x", int(maxDOCXXMLBytes)) +
		`</w:t></w:r></w:p></w:body></w:document>`
	data := testDOCX(t, map[string]string{"word/document.xml": document})

	_, err := docx2md(data)
	if err == nil {
		t.Fatal("oversized document.xml returned no error")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDOCX2MD_UsesStyleOutlineLevelForHeading(t *testing.T) {
	document := `<w:document xmlns:w="urn:w"><w:body><w:p><w:pPr><w:pStyle w:val="CustomHeading"/></w:pPr><w:r><w:t>Release notes</w:t></w:r></w:p></w:body></w:document>`
	styles := `<w:styles xmlns:w="urn:w"><w:style w:type="paragraph" w:styleId="CustomHeading"><w:pPr><w:outlineLvl w:val="1"/></w:pPr></w:style></w:styles>`
	data := testDOCX(t, map[string]string{
		"word/document.xml": document,
		"word/styles.xml":   styles,
	})

	got, err := docx2md(data)
	if err != nil {
		t.Fatalf("docx2md: %v", err)
	}
	if !strings.Contains(got, "## Release notes") {
		t.Fatalf("custom outline level was not rendered as H2: %q", got)
	}
}

func TestDOCX2MD_TableFirstRowBecomesHeader(t *testing.T) {
	document := `<w:document xmlns:w="urn:w"><w:body><w:tbl>
<w:tr><w:tc><w:p><w:r><w:t>Name</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Version</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:p><w:r><w:t>Manager</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>v1</w:t></w:r></w:p></w:tc></w:tr>
</w:tbl></w:body></w:document>`

	got, err := docx2md(testDOCX(t, map[string]string{"word/document.xml": document}))
	if err != nil {
		t.Fatalf("docx2md: %v", err)
	}
	if !strings.Contains(got, "|Name   |Version|") || !strings.Contains(got, "|-------|-------|") {
		t.Fatalf("first table row was not rendered as a Markdown header: %q", got)
	}
}

func testDOCX(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create ZIP part %q: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write ZIP part %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close ZIP: %v", err)
	}
	return buf.Bytes()
}
