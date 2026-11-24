package imapsrc

import (
	"strings"
	"unicode/utf8"
)

const maxErrorLen = 500

const redacted = "[redacted]"

func scrub(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}

	text := err.Error()
	if !strings.Contains(text, secret) {
		return err
	}

	return redactedError(strings.ReplaceAll(text, secret, redacted))
}

type redactedError string

func (err redactedError) Error() string {
	return string(err)
}

func clipError(err error, secret string) string {
	if err == nil {
		return ""
	}

	return clip(scrub(err, secret).Error(), maxErrorLen)
}

func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}

	cut := text[:limit]
	for cut != "" && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}

	return cut
}
