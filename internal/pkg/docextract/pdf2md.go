package docextract

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

const pdfHeadingRatio = 1.2

var errPDFNoText = errors.New("PDF has no reliably extractable body text; image-only PDFs are not supported")

// pdf2md converts an embedded PDF text layer to Markdown. Text whose font
// size is at least pdfHeadingRatio times the document's dominant body size is
// emitted as a heading; remaining text is emitted as ordinary paragraphs.
func pdf2md(data []byte) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			out = ""
			err = fmt.Errorf("read styled pdf text: %v", r)
		}
	}()

	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("read pdf: %w", err)
	}
	texts, err := r.GetStyledTexts()
	if err != nil {
		return "", fmt.Errorf("read styled pdf text: %w", err)
	}
	if len(texts) == 0 {
		return pdfPlainText(r)
	}

	bodySize := pdfBodyFontSize(texts)
	if bodySize == 0 {
		return pdfPlainText(r)
	}
	headingSizes := pdfHeadingSizes(texts, bodySize)
	if len(headingSizes) == 0 {
		return pdfPlainText(r)
	}

	levels := make(map[float64]int, len(headingSizes))
	for i, size := range headingSizes {
		levels[size] = min(i+1, 3)
	}

	var b strings.Builder
	for _, text := range texts {
		line := strings.TrimSpace(text.S)
		if line == "" {
			continue
		}
		if level, ok := levels[pdfFontSizeKey(text.FontSize)]; ok {
			b.WriteString(strings.Repeat("#", level))
			b.WriteByte(' ')
		}
		b.WriteString(line)
		b.WriteString("\n\n")
	}
	out = strings.TrimSpace(b.String())
	if out == "" {
		return pdfPlainText(r)
	}
	return validatePDFText(out)
}

func pdfPlainText(r *pdf.Reader) (string, error) {
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

// validatePDFText rejects empty text and text layers that contain a
// substantial amount of control data. Some scanned PDFs embed a bogus
// character map alongside their page images; ledongthuc/pdf can then return a
// non-empty string made mostly of control characters and isolated glyphs.
// Treating that string as source text produces plausible-looking but
// fabricated Wiki summaries.
func validatePDFText(text string) (string, error) {
	var (
		runes        int
		controlRunes int
		replacement  int
	)
	for _, r := range text {
		runes++
		if r == '\uFFFD' {
			replacement++
		}
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' && r != '\f' {
			controlRunes++
		}
	}
	if runes == 0 || replacement > 0 || controlRunes > 32 || controlRunes*100 > runes*5 {
		return "", errPDFNoText
	}
	return text, nil
}

func pdfBodyFontSize(texts []pdf.Text) float64 {
	counts := make(map[float64]int)
	for _, text := range texts {
		if strings.TrimSpace(text.S) == "" || text.FontSize <= 0 {
			continue
		}
		counts[pdfFontSizeKey(text.FontSize)] += utf8.RuneCountInString(text.S)
	}
	var (
		bodySize float64
		maxCount int
	)
	for size, count := range counts {
		if count > maxCount || (count == maxCount && size < bodySize) {
			bodySize, maxCount = size, count
		}
	}
	return bodySize
}

func pdfHeadingSizes(texts []pdf.Text, bodySize float64) []float64 {
	sizes := make(map[float64]struct{})
	for _, text := range texts {
		size := pdfFontSizeKey(text.FontSize)
		if strings.TrimSpace(text.S) != "" && size >= bodySize*pdfHeadingRatio {
			sizes[size] = struct{}{}
		}
	}
	out := make([]float64, 0, len(sizes))
	for size := range sizes {
		out = append(out, size)
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(out)))
	return out
}

// PDF producers often encode harmless floating-point noise in a font size.
func pdfFontSizeKey(size float64) float64 { return math.Round(size*10) / 10 }
