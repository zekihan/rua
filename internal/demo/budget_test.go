//go:build !race

package demo

import (
	"testing"
	"time"
)

// generateBudget catches gross regressions only. Generate takes about 2.5s
// on a workstation and more than twice that on shared CI runners.
const generateBudget = 15 * time.Second

func checkBudget(t *testing.T, elapsed time.Duration) {
	t.Helper()

	if elapsed > generateBudget {
		t.Fatalf("Generate took %s, budget is %s", elapsed, generateBudget)
	}
}
