package urlfetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestExtractURLs(t *testing.T) {
	text := "links: https://example.com/page https://example.com/page http://example.org/test."
	urls := ExtractURLs(text, 5)
	if len(urls) != 2 {
		t.Fatalf("expected 2 unique urls, got %d", len(urls))
	}
	if urls[0] != "https://example.com/page" {
		t.Fatalf("unexpected first url: %s", urls[0])
	}
	if urls[1] != "http://example.org/test" {
		t.Fatalf("unexpected second url: %s", urls[1])
	}
}

func TestFetchRejectsLocalhost(t *testing.T) {
	_, err := Fetch(context.Background(), "http://127.0.0.1/test")
	if err == nil {
		t.Fatal("expected localhost URL to be blocked")
	}
	if !errors.Is(err, ErrURLBlocked) {
		t.Fatalf("expected ErrURLBlocked, got %v", err)
	}
}

func TestExtractContentHTML(t *testing.T) {
	title, text := extractContent([]byte(`<html><head><title>Example Title</title></head><body>Hello <b>world</b></body></html>`), "text/html", "https://example.com", false)
	if title != "Example Title" {
		t.Fatalf("unexpected title: %q", title)
	}
	if text == "" {
		t.Fatal("expected non-empty extracted text")
	}
}

func TestExtractContentPDF(t *testing.T) {
	pdfData := makeSimplePDF("Hello PDF world")
	title, extracted := extractContent(pdfData, "application/pdf", "https://example.com/test.pdf", true)
	if title != "test.pdf" {
		t.Fatalf("unexpected title: %q", title)
	}
	if !strings.Contains(extracted, "Hello PDF world") {
		t.Fatalf("expected extracted text to contain phrase, got: %q", extracted)
	}
}

func TestSetBrowserLikeHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatalf("unexpected error creating request: %v", err)
	}
	setBrowserLikeHeaders(req)

	if got := req.Header.Get("User-Agent"); got != browserLikeUserAgent {
		t.Fatalf("unexpected User-Agent: %q", got)
	}
	if got := req.Header.Get("Accept"); got != browserLikeAccept {
		t.Fatalf("unexpected Accept: %q", got)
	}
	if got := req.Header.Get("Accept-Language"); got == "" {
		t.Fatal("expected Accept-Language to be set")
	}
}

func makeSimplePDF(text string) []byte {
	var buf bytes.Buffer
	write := func(s string) { _, _ = buf.WriteString(s) }

	write("%PDF-1.4\n")

	offsets := make([]int, 0, 6)
	offsets = append(offsets, 0) // free object

	writeObj := func(objNum int, body string) {
		offsets = append(offsets, buf.Len())
		write(fmt.Sprintf("%d 0 obj\n", objNum))
		write(body)
		if !strings.HasSuffix(body, "\n") {
			write("\n")
		}
		write("endobj\n")
	}

	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>")

	content := fmt.Sprintf("BT /F1 24 Tf 72 720 Td (%s) Tj ET\n", escapePDFString(text))
	stream := fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content)
	writeObj(4, stream)
	writeObj(5, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	xrefOffset := buf.Len()
	write("xref\n")
	write(fmt.Sprintf("0 %d\n", len(offsets)))
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
	replacer := strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)")
	return replacer.Replace(s)
}
