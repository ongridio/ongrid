// Package docextract pulls plain text out of an uploaded knowledge file so
// the RAG pipeline (chunk → embed → upsert) has something to index.
// Pure-Go, no CGO: md/txt are passthrough, pdf via ledongthuc/pdf, docx via
// stdlib zip + a tiny XML walk. Scanned/image PDFs (no embedded text) and
// encrypted files yield empty/err — OCR is out of scope (ADR-028 phase-2).
package docextract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

// Supported reports whether the extension is one we can extract.
func Supported(filename string) bool {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".md", ".markdown", ".txt", ".text", ".pdf", ".docx":
		return true
	}
	return false
}

// Extract returns the plain-text body of an uploaded file, dispatched by
// extension. Errors are user-facing (surfaced on the upload response).
func Extract(filename string, data []byte) (string, error) {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".md", ".markdown", ".txt", ".text":
		if !utf8.Valid(data) {
			return "", fmt.Errorf("file is not valid UTF-8 text")
		}
		return string(data), nil
	case ".pdf":
		return extractPDF(data)
	case ".docx":
		return extractDOCX(data)
	default:
		return "", fmt.Errorf("unsupported file type %q (allowed: .md, .txt, .pdf, .docx)", filepath.Ext(filename))
	}
}

// extractPDF pulls the embedded text layer. Returns a friendly error when
// the PDF carries no extractable text (scanned/image-only) so the operator
// knows OCR isn't supported rather than seeing a blank doc.
func extractPDF(data []byte) (string, error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("read pdf: %w", err)
	}
	rc, err := r.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("extract pdf text: %w", err)
	}
	var b strings.Builder
	if _, err := io.Copy(&b, rc); err != nil {
		return "", fmt.Errorf("read pdf text: %w", err)
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", fmt.Errorf("no extractable text in pdf (scanned/image PDFs need OCR, not supported)")
	}
	return out, nil
}

// extractDOCX unzips the .docx (a zip of OOXML) and walks word/document.xml,
// concatenating <w:t> runs with paragraph (<w:p>) breaks. Avoids a heavy
// OOXML dependency — we only need the text, not styling.
func extractDOCX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("read docx (not a valid .docx zip): %w", err)
	}
	var docXML *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			docXML = f
			break
		}
	}
	if docXML == nil {
		return "", fmt.Errorf("docx missing word/document.xml")
	}
	rc, err := docXML.Open()
	if err != nil {
		return "", fmt.Errorf("open docx body: %w", err)
	}
	defer rc.Close()

	dec := xml.NewDecoder(rc)
	var b strings.Builder
	inText := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("parse docx xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" { // <w:t> text run
				inText = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p": // paragraph end → newline
				b.WriteByte('\n')
			case "tab":
				b.WriteByte('\t')
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", fmt.Errorf("no extractable text in docx")
	}
	return out, nil
}
