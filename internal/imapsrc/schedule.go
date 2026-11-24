package imapsrc

import (
	"math/rand/v2"
	"time"
)

const (
	triggerCooldown  = 5 * time.Minute
	maxBackoff       = 6 * time.Hour
	maxJitterPercent = 10
	jitterSteps      = maxJitterPercent*2 + 1
	percentScale     = 100
)

// connectBackoff is the wait before each retry inside one attempt.
// The first dial is immediate; these follow failed dials.
var connectBackoff = [...]time.Duration{
	time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
}

func randomJitter() int64 {
	//nolint:gosec // G404: jitter only spreads the schedule; it is not a secret.
	return rand.Int64N(jitterSteps) - maxJitterPercent
}

// scheduleDelay is the pause before the next attempt.
// A successful attempt and the first failure both wait Interval: the failed
// attempt is retried at the next interval, then the wait doubles, up to
// maxBackoff, while failures continue.
func scheduleDelay(interval time.Duration, failures int) time.Duration {
	delay := max(interval, 0)

	if delay > maxBackoff {
		return maxBackoff
	}

	steps := failures
	if steps > 0 {
		steps--
	}

	for range steps {
		if delay >= maxBackoff/2 {
			return maxBackoff
		}

		delay *= 2
		if delay > maxBackoff {
			return maxBackoff
		}
	}

	return delay
}

func applyJitter(base time.Duration, percent int64) time.Duration {
	if base <= 0 {
		return 0
	}

	adjusted := base + time.Duration(int64(base)*percent/percentScale)
	if adjusted < 0 {
		return 0
	}

	return adjusted
}

func (fetcher *Fetcher) nextDelay(failures int) time.Duration {
	delay := applyJitter(scheduleDelay(fetcher.cfg.Interval, failures), fetcher.jitterN())
	if delay > maxBackoff {
		return maxBackoff
	}

	return delay
}
