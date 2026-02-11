package pdfextract

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestExtractText_SimplePDF(t *testing.T) {
	pdfData := makeSimplePDF("Hello PDF world")
	got, err := ExtractText(pdfData)
	if err != nil {
		t.Fatalf("ExtractText error: %v", err)
	}
	if !strings.Contains(got, "Hello PDF world") {
		t.Fatalf("expected extracted text to contain phrase, got: %q", got)
	}
}

func makeSimplePDF(text string) []byte {
	// Minimal 1-page PDF with a single Helvetica text draw.
	// Generates a proper xref table based on computed offsets.
	var buf bytes.Buffer
	write := func(s string) { _, _ = buf.WriteString(s) }

	write("%PDF-1.4\n")

	offsets := make([]int, 0, 6)
	offsets = append(offsets, 0) // object 0 is the free object

	writeObj := func(objNum int, body string) {
		offsets = append(offsets, buf.Len())
		write(fmt.Sprintf("%d 0 obj\n", objNum))
		write(body)
		if !strings.HasSuffix(body, "\n") {
			write("\n")
		}
		write("endobj\n")
	}

	// 1: Catalog
	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	// 2: Pages
	writeObj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	// 3: Page with resources and content
	writeObj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>")
	// 4: Content stream
	content := fmt.Sprintf("BT /F1 24 Tf 72 720 Td (%s) Tj ET\n", escapePDFString(text))
	stream := fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content)
	writeObj(4, stream)
	// 5: Helvetica font
	writeObj(5, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	xrefOffset := buf.Len()
	write("xref\n")
	write(fmt.Sprintf("0 %d\n", len(offsets)))
	// Object 0: free
	write("0000000000 65535 f \n")
	for i := 1; i < len(offsets); i++ {
		write(fmt.Sprintf("%010d 00000 n \n", offsets[i]))
	}

	write("trailer\n")
	write(fmt.Sprintf("<< /Size %d /Root 1 0 R >>\n", len(offsets)))
	write("startxref\n")
	write(fmt.Sprintf("%d\n", xrefOffset))
	write("%%EOF\n")
	return buf.Bytes()
}

func escapePDFString(s string) string {
	// Escape parentheses and backslash for literal strings.
	r := strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)")
	return r.Replace(s)
}
