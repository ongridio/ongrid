// Package docextract pulls plain text out of an uploaded knowledge file so
// the RAG pipeline (chunk → embed → upsert) has something to index.
// Pure-Go, no CGO: md/txt are passthrough, pdf via ledongthuc/pdf, docx via
// stdlib zip + a tiny XML walk. Scanned/image PDFs are rejected because OCR is
// out of scope (ADR-028 phase-2).
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
		return pdf2md(data)
	case ".docx":
		return docx2md(data)
	default:
		return "", fmt.Errorf("unsupported file type %q (allowed: .md, .txt, .pdf, .docx)", filepath.Ext(filename))
	}
}

// ExtractPlainText returns a non-Markdown preview for office documents. It is
// intentionally separate from Extract: the compiler needs Markdown-shaped
// structure, while a Raw DOCX preview must not pretend to be a Markdown file.
func ExtractPlainText(filename string, data []byte) (string, error) {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".docx":
		return docxPlainText(data)
	case ".pdf":
		return extractPDF(data)
	case ".md", ".markdown", ".txt", ".text":
		if !utf8.Valid(data) {
			return "", fmt.Errorf("file is not valid UTF-8 text")
		}
		return string(data), nil
	default:
		return "", fmt.Errorf("unsupported file type %q", filepath.Ext(filename))
	}
}

func docxPlainText(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("read docx (not a valid .docx zip): %w", err)
	}
	var document *zip.File
	for _, file := range zr.File {
		if file.Name == "word/document.xml" {
			document = file
			break
		}
	}
	if document == nil {
		return "", fmt.Errorf("docx missing word/document.xml")
	}
	rc, err := document.Open()
	if err != nil {
		return "", fmt.Errorf("open docx body: %w", err)
	}
	defer func() { _ = rc.Close() /* read-only cleanup */ }()

	var out strings.Builder
	decoder := xml.NewDecoder(rc)
	inText := false
	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return "", fmt.Errorf("parse docx xml: %w", tokenErr)
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "t", "delText":
				inText = true
			case "tab":
				out.WriteByte('\t')
			case "br", "cr":
				out.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				out.Write([]byte(value))
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "t", "delText":
				inText = false
			case "p":
				out.WriteByte('\n')
			}
		}
	}
	text := strings.TrimSpace(out.String())
	if text == "" {
		return "", fmt.Errorf("no extractable text in docx")
	}
	return text, nil
}

// extractPDF pulls the embedded text layer. Scanned/image-only PDFs return an
// error so the upload and compile flows never treat a placeholder as content.
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
	return validatePDFText(out)
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
