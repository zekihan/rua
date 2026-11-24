package sources

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"
)

const defaultPollInterval = 10 * time.Second

// Loader keeps the current Set for a file and reloads it when the file
// changes. A file that fails to parse keeps the previous Set and is logged.
type Loader struct {
	current  atomic.Pointer[Set]
	logger   *slog.Logger
	path     string
	loaded   fileStamp
	broken   fileStamp
	interval time.Duration
}

type fileStamp struct {
	modTime time.Time
	size    int64
	missing bool
}

func (stamp fileStamp) equal(other fileStamp) bool {
	return stamp.missing == other.missing &&
		stamp.size == other.size &&
		stamp.modTime.Equal(other.modTime)
}

func zeroStamp() fileStamp {
	return fileStamp{modTime: time.Time{}, size: 0, missing: false}
}

func missingStamp() fileStamp {
	return fileStamp{modTime: time.Time{}, size: 0, missing: true}
}

// NewLoader loads path. An empty path yields a Loader that always returns
// Empty(). A missing or invalid file is returned as an error.
func NewLoader(path string) (*Loader, error) {
	loader := &Loader{
		current:  atomic.Pointer[Set]{},
		logger:   nil,
		path:     "",
		loaded:   zeroStamp(),
		broken:   zeroStamp(),
		interval: defaultPollInterval,
	}
	loader.current.Store(Empty())

	if path == "" {
		return loader, nil
	}

	loader.path = path

	set, stamp, err := loadPath(path)
	if err != nil {
		return nil, err
	}

	loader.current.Store(set)
	loader.loaded = stamp

	return loader, nil
}

// SetInterval sets how often Run polls the file. The default is 10 seconds.
// Call it before Run.
func (loader *Loader) SetInterval(interval time.Duration) {
	if interval <= 0 {
		return
	}

	loader.interval = interval
}

// Current returns the latest Set. Safe for concurrent use.
func (loader *Loader) Current() *Set { return loader.current.Load() }

// Run polls the file for changes until ctx is done. An empty path waits for
// ctx and does not read a file.
func (loader *Loader) Run(ctx context.Context) error {
	if loader.path == "" {
		<-ctx.Done()

		return fmt.Errorf("watch sources: %w", ctx.Err())
	}

	interval := loader.interval
	if interval <= 0 {
		interval = defaultPollInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		err := ctx.Err()
		if err != nil {
			return fmt.Errorf("watch sources: %w", err)
		}

		loader.reload(ctx)

		select {
		case <-ctx.Done():
			return fmt.Errorf("watch sources: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (loader *Loader) reload(ctx context.Context) {
	stamp, err := statFile(loader.path)
	if err != nil {
		loader.reject(ctx, missingStamp(), err)

		return
	}

	if stamp.equal(loader.loaded) {
		return
	}

	set, err := parsePath(loader.path)
	if err != nil {
		loader.reject(ctx, stamp, err)

		return
	}

	loader.loaded = stamp
	loader.broken = zeroStamp()
	loader.current.Store(set)
}

func (loader *Loader) reject(ctx context.Context, stamp fileStamp, err error) {
	if stamp.equal(loader.broken) {
		return
	}

	loader.broken = stamp
	loader.logFailure(ctx, err)
}

func (loader *Loader) logFailure(ctx context.Context, err error) {
	logger := loader.logger
	if logger == nil {
		logger = slog.Default()
	}

	logger.ErrorContext(
		ctx,
		"Source rules reload failed",
		slog.String("path", loader.path),
		slog.Any("error", err),
	)
}

func loadPath(path string) (*Set, fileStamp, error) {
	stamp, err := statFile(path)
	if err != nil {
		return nil, zeroStamp(), err
	}

	set, err := parsePath(path)
	if err != nil {
		return nil, zeroStamp(), err
	}

	return set, stamp, nil
}

func statFile(path string) (fileStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		return zeroStamp(), fmt.Errorf("stat sources: %w", err)
	}

	return fileStamp{modTime: info.ModTime(), size: info.Size(), missing: false}, nil
}

func parsePath(path string) (*Set, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sources: %w", err)
	}

	set, err := Parse(data)
	if err != nil {
		return nil, err
	}

	return set, nil
}
