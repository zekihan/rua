//go:build race

package demo

import (
	"testing"
	"time"
)

func checkBudget(t *testing.T, elapsed time.Duration) {
	t.Helper()

	if elapsed < 0 {
		t.Fatal(elapsed)
	}
}
