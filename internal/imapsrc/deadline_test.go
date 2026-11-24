package imapsrc

import (
	"testing"
	"time"
)

func TestBodyFetchTimeout(t *testing.T) {
	t.Parallel()

	if bodyFetchTimeout(0) != minBodyTimeout || bodyFetchTimeout(-1) != minBodyTimeout {
		t.Fatalf("empty %s negative %s", bodyFetchTimeout(0), bodyFetchTimeout(-1))
	}

	want := minBodyTimeout + 25*time.Second
	if bodyFetchTimeout(25<<20) != want {
		t.Fatalf("sized = %s, want %s", bodyFetchTimeout(25<<20), want)
	}

	if bodyFetchTimeout(1<<40) != maxBodyTimeout {
		t.Fatalf("capped = %s", bodyFetchTimeout(1<<40))
	}
}
