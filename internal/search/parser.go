package search

import (
	"errors"
	"strings"
	"unicode"
)

type token struct {
	negative bool
	prefix   bool
	text     string
}

func BuildFTSMatch(raw string, advanced bool) (string, error) {
	_ = advanced
	tokens := parseTokens(raw)
	if len(tokens) == 0 {
		return "", errors.New("empty query")
	}

	var positives []string
	var negatives []string
	for _, tok := range tokens {
		term := tokenToTerm(tok)
		if term == "" {
			continue
		}
		if tok.negative {
			negatives = append(negatives, term)
		} else {
			positives = append(positives, term)
		}
	}
	if len(positives) == 0 {
		return "", errors.New("query requires at least one positive term")
	}

	expression := strings.Join(positives, " AND ")
	for _, neg := range negatives {
		expression += " NOT " + neg
	}
	return expression, nil
}

func parseTokens(raw string) []token {
	var (
		result   []token
		current  strings.Builder
		inQuotes bool
	)

	flush := func(neg bool) {
		text := strings.TrimSpace(current.String())
		current.Reset()
		if text == "" {
			return
		}
		tok := token{negative: neg}
		if strings.HasSuffix(text, "*") {
			tok.prefix = true
			text = strings.TrimSuffix(text, "*")
		}
		tok.text = sanitize(text)
		if tok.text != "" {
			result = append(result, tok)
		}
	}

	negative := false
	for _, r := range raw {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			if !inQuotes {
				flush(negative)
				negative = false
			}
		case unicode.IsSpace(r) && !inQuotes:
			flush(negative)
			negative = false
		case r == '-' && !inQuotes && current.Len() == 0:
			negative = true
		default:
			current.WriteRune(r)
		}
	}
	flush(negative)
	return result
}

func tokenToTerm(tok token) string {
	if tok.text == "" {
		return ""
	}
	if tok.prefix {
		return quote(tok.text) + "*"
	}
	return quote(tok.text)
}

func sanitize(input string) string {
	var b strings.Builder
	for _, r := range input {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(" _-./@:", r) {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func quote(v string) string {
	escaped := strings.ReplaceAll(v, `"`, `""`)
	return `"` + escaped + `"`
}
