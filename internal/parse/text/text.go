// Package text sanitizes untrusted report strings.
package text

import (
	"strings"
	"unicode/utf8"

	"github.com/zekihan/rua/internal/report"
)

// Limit trims whitespace, replaces invalid UTF-8 and limits the value to the given byte limit.
func Limit(value string, limit int) string {
	if limit <= 0 {
		return ""
	}

	value = strings.ToValidUTF8(strings.TrimSpace(value), "�")
	if len(value) <= limit {
		return value
	}

	for !utf8.RuneStart(value[limit]) {
		limit--
	}

	return value[:limit]
}

// LowerShort normalizes a domain or enum to lowercase and applies the short limit.
func LowerShort(value string) string {
	// Repair before lowercasing, which would replace each invalid byte separately.
	return Limit(strings.ToLower(strings.ToValidUTF8(value, "�")), report.MaxShort)
}
