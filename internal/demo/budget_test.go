//go:build !race

package demo

import (
	"testing"
	"time"
)

const generateBudget = 5 * time.Second

func checkBudget(t *testing.T, elapsed time.Duration) {
	t.Helper()

	if elapsed > generateBudget {
		t.Fatalf("Generate took %s, budget is %s", elapsed, generateBudget)
	}
}
