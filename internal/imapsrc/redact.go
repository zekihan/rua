package imapsrc

import (
	"strings"
	"unicode/utf8"
)

const maxErrorLen = 500

const redacted = "[redacted]"

func scrub(err error, secret string) error {
	return scrubSecrets(err, []string{secret})
}

func scrubSecrets(err error, secrets []string) error {
	if err == nil {
		return nil
	}

	text := scrubText(err.Error(), secrets)
	if text == err.Error() {
		return err
	}

	return redactedError(text)
}

func scrubText(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret == "" || !strings.Contains(text, secret) {
			continue
		}

		text = strings.ReplaceAll(text, secret, redacted)
	}

	return text
}

type redactedError string

func (err redactedError) Error() string {
	return string(err)
}

func clipError(err error, secrets ...string) string {
	if err == nil {
		return ""
	}

	return clip(scrubText(err.Error(), secrets), maxErrorLen)
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
