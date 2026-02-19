package pdfextract

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"unicode"

	pdf "github.com/ledongthuc/pdf"
)

var ErrEmptyText = errors.New("no extractable text found in pdf")

// ExtractText extracts plain text from a PDF byte slice.
// It does not perform OCR. If the PDF has no text layer, ErrEmptyText may be returned.
func ExtractText(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("pdf data is empty")
	}

	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}

	plain, err := r.GetPlainText()
	if err != nil {
		return "", err
	}

	raw, err := io.ReadAll(plain)
	if err != nil {
		return "", err
	}

	text := normalizePDFText(string(raw))
	if strings.TrimSpace(text) == "" {
		return "", ErrEmptyText
	}
	return text, nil
}

func normalizePDFText(input string) string {
	var b strings.Builder
	b.Grow(len(input))

	lastSpace := false
	for _, r := range input {
		if r == '\u0000' {
			continue
		}
		if unicode.IsSpace(r) {
			if lastSpace {
				continue
			}
			lastSpace = true
			b.WriteByte(' ')
			continue
		}
		lastSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
