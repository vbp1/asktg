package urlfetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"asktg/internal/pdfextract"
	"asktg/internal/security"

	"github.com/PuerkitoBio/goquery"
)

var (
	urlRegex         = regexp.MustCompile(`https?://[^\s<>"'()]+`)
	spaceNormalize   = regexp.MustCompile(`\s+`)
	ErrURLBlocked    = errors.New("url blocked by security policy")
	ErrFetchTooLarge = errors.New("response body exceeds maximum size")
)

type Result struct {
	FinalURL      string
	Title         string
	ExtractedText string
	ContentType   string
	Hash          string
}

func ExtractURLs(text string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	matches := urlRegex.FindAllString(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	urls := make([]string, 0, minInt(limit, len(matches)))
	for _, item := range matches {
		cleaned := strings.TrimSpace(strings.Trim(item, ".,;:!?)[]{}\"'"))
		if cleaned == "" {
			continue
		}
		if _, exists := seen[cleaned]; exists {
			continue
		}
		seen[cleaned] = struct{}{}
		urls = append(urls, cleaned)
		if len(urls) >= limit {
			break
		}
	}
	return urls
}

func Fetch(ctx context.Context, rawURL string) (Result, error) {
	if err := security.ValidateFetchURL(rawURL); err != nil {
		return Result{}, errors.Join(ErrURLBlocked, err)
	}

	assumePDF := looksLikePDFURL(rawURL)
	client := &http.Client{
		Timeout: security.DefaultFetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= security.DefaultMaxRedirects {
				return errors.New("too many redirects")
			}
			if err := security.ValidateFetchURL(req.URL.String()); err != nil {
				return errors.Join(ErrURLBlocked, err)
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", "asktg-url-fetch/0.1")
	req.Header.Set("Accept", "application/pdf, text/html, text/plain;q=0.9, */*;q=0.5")

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Result{}, errors.New(resp.Status)
	}

	if err := security.ValidateFetchURL(resp.Request.URL.String()); err != nil {
		return Result{}, errors.Join(ErrURLBlocked, err)
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	isPDF := strings.Contains(contentType, "application/pdf") || strings.Contains(contentType, "application/x-pdf") || assumePDF

	limit := int64(security.DefaultMaxSizeBytes)
	if isPDF {
		limit = int64(security.DefaultMaxPDFSizeBytes)
	}
	reader := io.LimitReader(resp.Body, limit+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		return Result{}, err
	}
	if int64(len(body)) > limit {
		return Result{}, ErrFetchTooLarge
	}

	finalURL := normalizeURL(resp.Request.URL)
	title, extracted := extractContent(body, contentType, finalURL, isPDF)
	if extracted == "" {
		extracted = title
	}
	extracted = normalizeSpace(extracted)
	title = normalizeSpace(title)

	hashRaw := sha256.Sum256(body)
	return Result{
		FinalURL:      finalURL,
		Title:         title,
		ExtractedText: extracted,
		ContentType:   strings.TrimSpace(resp.Header.Get("Content-Type")),
		Hash:          hex.EncodeToString(hashRaw[:]),
	}, nil
}

func extractContent(body []byte, contentType string, finalURL string, isPDF bool) (string, string) {
	if isPDF {
		text, err := pdfextract.ExtractText(body)
		if err != nil {
			// Fallback to empty text; caller will keep Title if present.
			return inferTitleFromURL(finalURL), ""
		}
		// Keep it bounded for storage and downstream embedding.
		const maxRunes = 200000
		runes := []rune(text)
		if len(runes) > maxRunes {
			text = string(runes[:maxRunes])
		}
		return inferTitleFromURL(finalURL), text
	}

	rawBody := string(body)
	looksHTML := strings.Contains(contentType, "text/html") ||
		strings.Contains(contentType, "application/xhtml") ||
		strings.Contains(rawBody, "<html") ||
		strings.Contains(rawBody, "<body")
	if !looksHTML {
		raw := normalizeSpace(rawBody)
		if len(raw) > 10000 {
			raw = raw[:10000]
		}
		return "", raw
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(rawBody))
	if err != nil {
		raw := normalizeSpace(rawBody)
		if len(raw) > 10000 {
			raw = raw[:10000]
		}
		return "", raw
	}

	doc.Find("script,style,noscript").Each(func(_ int, selection *goquery.Selection) {
		selection.Remove()
	})

	title := doc.Find("title").First().Text()
	text := doc.Find("body").Text()
	if strings.TrimSpace(text) == "" {
		text = doc.Text()
	}
	if len(text) > 20000 {
		text = text[:20000]
	}
	return title, text
}

func normalizeSpace(value string) string {
	return strings.TrimSpace(spaceNormalize.ReplaceAllString(value, " "))
}

func looksLikePDFURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	ext := strings.ToLower(path.Ext(parsed.Path))
	return ext == ".pdf"
}

func inferTitleFromURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	base := path.Base(parsed.Path)
	if base == "." || base == "/" {
		return ""
	}
	return base
}

func normalizeURL(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	copyURL := *parsed
	copyURL.Fragment = ""
	return copyURL.String()
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
