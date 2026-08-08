package docextract

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestPDF2MD_FontSizesBecomeHeadings(t *testing.T) {
	data := testPDF(t, "BT\n/F1 24 Tf\n72 720 Td\n(Overview) Tj\n/F1 18 Tf\n0 -40 Td\n(Install) Tj\n/F1 12 Tf\n0 -30 Td\n(Install the agent on every node.) Tj\n0 -16 Td\n(The agent reports metrics.) Tj\nET")

	got, err := pdf2md(data)
	if err != nil {
		t.Fatalf("pdf2md: %v", err)
	}
	if !strings.Contains(got, "# Overview") {
		t.Fatalf("largest font was not an H1: %q", got)
	}
	if !strings.Contains(got, "## Install") {
		t.Fatalf("second-largest font was not an H2: %q", got)
	}
	if !strings.Contains(got, "Install the agent on every node.") {
		t.Fatalf("body text missing: %q", got)
	}
}

func TestPDF2MD_WithoutHeadingsFallsBackToPlainText(t *testing.T) {
	data := testPDF(t, "BT\n/F1 12 Tf\n72 720 Td\n(First body sentence.) Tj\n0 -16 Td\n(Second body sentence.) Tj\nET")

	got, err := pdf2md(data)
	if err != nil {
		t.Fatalf("pdf2md: %v", err)
	}
	if strings.Contains(got, "# ") {
		t.Fatalf("plain PDF unexpectedly gained headings: %q", got)
	}
	if !strings.Contains(got, "Second body sentence.") {
		t.Fatalf("plain text missing: %q", got)
	}
}

func TestPDF2MD_InvalidPDFReturnsError(t *testing.T) {
	if _, err := pdf2md([]byte("not a pdf")); err == nil {
		t.Fatal("invalid PDF returned no error")
	}
}

func TestPDF2MD_CapsHeadingLevelAtH3(t *testing.T) {
	data := testPDF(t, "BT\n/F1 36 Tf\n72 720 Td\n(Top) Tj\n/F1 30 Tf\n0 -40 Td\n(Second) Tj\n/F1 24 Tf\n0 -40 Td\n(Third) Tj\n/F1 18 Tf\n0 -40 Td\n(Fourth) Tj\n/F1 12 Tf\n0 -40 Td\n(Body text.) Tj\nET")

	got, err := pdf2md(data)
	if err != nil {
		t.Fatalf("pdf2md: %v", err)
	}
	for _, want := range []string{"# Top", "## Second", "### Third", "### Fourth"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing heading %q in %q", want, got)
		}
	}
	if strings.Contains(got, "#### Fourth") {
		t.Fatalf("heading level exceeds H3: %q", got)
	}
}

func TestPDF2MD_WithoutTextReturnsError(t *testing.T) {
	_, err := pdf2md(testPDF(t, ""))
	if err == nil || !strings.Contains(err.Error(), "no extractable text") {
		t.Fatalf("empty PDF error = %v, want no extractable text", err)
	}
}

// testPDF builds a minimal one-page Helvetica PDF with caller-controlled
// content, so font sizes are explicit without committing a binary fixture.
func testPDF(t *testing.T, stream string) []byte {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, 1, 6)
	writeObject := func(n int, body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", n, body)
	}
	writeObject(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObject(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObject(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>")
	writeObject(4, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	writeObject(5, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return b.Bytes()
}
