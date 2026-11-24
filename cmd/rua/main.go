// Command rua ingests DMARC aggregate and SMTP TLS reports and serves a web
// UI for them.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zekihan/go-commons/metrics"
	"github.com/zekihan/go-commons/middleware"
	"github.com/zekihan/go-commons/utils"

	// IANA zones must resolve inside the distroless image.
	_ "time/tzdata"

	"github.com/zekihan/rua/internal/config"
	"github.com/zekihan/rua/internal/demo"
	"github.com/zekihan/rua/internal/enrich"
	"github.com/zekihan/rua/internal/imapsrc"
	"github.com/zekihan/rua/internal/ingest"
	"github.com/zekihan/rua/internal/query"
	"github.com/zekihan/rua/internal/server"
	"github.com/zekihan/rua/internal/sources"
	"github.com/zekihan/rua/internal/store"
	"github.com/zekihan/rua/internal/telemetry"
	"github.com/zekihan/rua/web"
)

const (
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 120 * time.Second
	shutdownTimeout   = 10 * time.Second
	metricsInterval   = time.Minute
	pruneInterval     = 24 * time.Hour
	reparseRetry      = 15 * time.Minute
	exitUsage         = 2
)

const usage = `Usage: rua [command] [arguments]

Commands:
  serve               Server, fetch loop and enrichment (default)
  fetch               One fetch and exit
  import <path>...    Ingest local report, .eml and mbox files
  reparse             Reparse all stored blobs now
  demo                Serve a generated dataset from memory

Configuration is read from environment variables; see the README.
`

var errUsage = errors.New("usage")

func main() {
	err := run(os.Args[1:], os.Stdout)
	if errors.Is(err, errUsage) {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(exitUsage)
	}

	if err != nil {
		slog.Error("Command failed", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	command, args, err := parseCommand(args)
	if err != nil {
		return err
	}

	if command == "help" {
		fmt.Fprint(stdout, usage)

		return nil
	}

	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}

	loggerConfig := new(utils.LoggerConfig)
	loggerConfig.Level = cfg.LogLevel
	utils.InitLogger(loggerConfig)

	switch command {
	case "fetch":
		return withStore(ctx, cfg.DBPath, func(st *store.Store) error {
			if !cfg.IMAPEnabled() {
				return imapsrc.ErrDisabled
			}

			err := imapsrc.New(imapConfig(cfg), st, newPipeline(st, cfg, nil, nil), nil).
				FetchOnce(ctx)
			if err != nil {
				return fmt.Errorf("fetch: %w", err)
			}

			return nil
		})
	case "import":
		return withStore(ctx, cfg.DBPath, func(st *store.Store) error {
			summary, err := newPipeline(st, cfg, nil, nil).Import(ctx, args)
			fmt.Fprintf(stdout, "files %d, messages %d, already stored %d, failed %d\n",
				summary.Files, summary.Messages, summary.Known, summary.Failed)

			if err != nil {
				return fmt.Errorf("import: %w", err)
			}

			return nil
		})
	case "reparse":
		return withStore(ctx, cfg.DBPath, func(st *store.Store) error {
			n, err := newPipeline(st, cfg, nil, nil).Reparse(ctx)
			fmt.Fprintf(stdout, "reparsed %d messages\n", n)

			if err != nil {
				return fmt.Errorf("reparse: %w", err)
			}

			return nil
		})
	case "demo":
		return serveDemo(ctx, cfg)
	default:
		utils.LogVersionInfo(ctx)

		return serve(ctx, cfg)
	}
}

func parseCommand(args []string) (string, []string, error) {
	command := "serve"
	if len(args) > 0 {
		command, args = args[0], args[1:]
	}

	switch command {
	case "help", "-h", "--help":
		return "help", nil, nil
	case "serve", "fetch", "reparse", "demo":
		if len(args) > 0 {
			return "", nil, errUsage
		}
	case "import":
		if len(args) == 0 {
			return "", nil, errUsage
		}
	default:
		return "", nil, errUsage
	}

	return command, args, nil
}

func withStore(ctx context.Context, path string, fn func(*store.Store) error) error {
	st, err := store.Open(ctx, path)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	return errors.Join(fn(st), st.Close())
}

func newPipeline(
	st *store.Store,
	cfg *config.Config,
	telem *telemetry.Metrics,
	newIPs func(),
) *ingest.Pipeline {
	return ingest.New(st, ingest.Options{
		Metrics: telem, Now: time.Now, NewIPs: newIPs, RetentionDays: cfg.RetentionDays,
	})
}

func imapConfig(cfg *config.Config) imapsrc.Config {
	return imapsrc.Config{
		Addr:            cfg.IMAPAddr,
		TLSMode:         cfg.IMAPTLS,
		CAFile:          cfg.IMAPCAFile,
		Username:        cfg.IMAPUsername,
		Password:        cfg.IMAPPassword,
		Mailboxes:       cfg.IMAPMailboxes,
		Interval:        cfg.FetchInterval,
		MaxMessageBytes: cfg.MaxMessageBytes,
	}
}

func serve(ctx context.Context, cfg *config.Config) error {
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()

	loader, err := sources.NewLoader(cfg.SourcesFile)
	if err != nil {
		return fmt.Errorf("load sources: %w", err)
	}

	registry := metrics.New()
	telem := telemetry.New(registry)

	worker := enrich.New(st, enrich.Options{
		Enabled: cfg.Lookups, Resolver: nil, Metrics: telem, Now: time.Now,
	})
	pipeline := newPipeline(st, cfg, telem, worker.Notify)
	fetcher := imapsrc.New(imapConfig(cfg), st, pipeline, telem)

	background(ctx, "Source rules reload", loader.Run)
	background(ctx, "Enrichment", worker.Run)
	background(ctx, "Reparse", func(ctx context.Context) error {
		return untilDone(ctx, reparseRetry, func(ctx context.Context) error {
			_, err := pipeline.ReparseIfNeeded(ctx)
			if err != nil {
				return fmt.Errorf("reparse: %w", err)
			}

			return nil
		})
	})
	background(ctx, "Retention", func(ctx context.Context) error {
		return every(ctx, pruneInterval, func(ctx context.Context) error {
			_, err := pipeline.Prune(ctx, cfg.RetentionDays)
			if err != nil {
				return fmt.Errorf("prune: %w", err)
			}

			return nil
		})
	})

	if cfg.IMAPEnabled() {
		background(ctx, "Fetch loop", fetcher.Run)
	}

	svc := query.New(query.Deps{
		Store:       st,
		Sources:     loader.Current,
		Fetch:       fetcher.State,
		Reparsing:   pipeline.Reparsing,
		EnrichQueue: worker.QueueLength,
		IMAPEnabled: cfg.IMAPEnabled(),
		StaleAfter:  cfg.StaleAfter,
		Now:         time.Now,
		Mailboxes:   cfg.IMAPMailboxes,
	})

	background(ctx, "Metrics collection", func(ctx context.Context) error {
		return every(ctx, metricsInterval, func(ctx context.Context) error {
			return svc.CollectMetrics(ctx, telem)
		})
	})

	return listen(ctx, cfg, svc, fetcher, st, registry)
}

func serveDemo(ctx context.Context, cfg *config.Config) error {
	st, err := store.Open(ctx, store.Memory)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()

	set, err := sources.Parse([]byte(demo.Sources))
	if err != nil {
		return fmt.Errorf("demo sources: %w", err)
	}

	pipeline := newPipeline(st, cfg, nil, nil)

	err = demo.Generate(ctx, st, pipeline, time.Now())
	if err != nil {
		return fmt.Errorf("generate demo data: %w", err)
	}

	var disabled imapsrc.Config

	fetcher := imapsrc.New(disabled, st, pipeline, nil)
	svc := query.New(query.Deps{
		Store:       st,
		Sources:     func() *sources.Set { return set },
		Fetch:       fetcher.State,
		Reparsing:   pipeline.Reparsing,
		EnrichQueue: nil,
		IMAPEnabled: false,
		StaleAfter:  cfg.StaleAfter,
		Now:         time.Now,
		Mailboxes:   []string{demo.Mailbox},
	})

	demoCfg := *cfg
	demoCfg.MetricsAddr = ""

	return listen(ctx, &demoCfg, svc, fetcher, st, metrics.New())
}

func listen(
	ctx context.Context,
	cfg *config.Config,
	svc *query.Service,
	fetcher *imapsrc.Fetcher,
	st *store.Store,
	registry *metrics.Registry,
) error {
	webFS, err := web.DistFS()
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}

	srv := server.New(server.Options{
		Query:   svc,
		Fetcher: fetcher,
		Web:     webFS,
		Ping:    func(r *http.Request) error { return st.Ping(r.Context()) },
	})

	httpServer := new(http.Server)
	httpServer.Addr = cfg.ListenAddr
	httpServer.Handler = registry.Middleware(
		middleware.RecoverHandler(middleware.Logging(srv.Handler())),
	)
	httpServer.ReadHeaderTimeout = readHeaderTimeout
	httpServer.IdleTimeout = idleTimeout

	slog.InfoContext(ctx, "Listening", slog.String("addr", cfg.ListenAddr))

	if cfg.MetricsAddr != "" {
		slog.InfoContext(ctx, "Serving metrics", slog.String("addr", cfg.MetricsAddr))
		background(ctx, "Metrics server", func(ctx context.Context) error {
			return registry.ListenAndServe(ctx, cfg.MetricsAddr)
		})
	}

	errCh := make(chan error, 1)

	go func() {
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err = <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	err = httpServer.Shutdown(shutdownCtx)
	if err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	return nil
}

// background runs fn in a goroutine and logs a failure other than
// cancellation.
func background(ctx context.Context, name string, fn func(context.Context) error) {
	go func() {
		err := fn(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, name+" stopped", slog.Any("error", err))
		}
	}()
}

// every runs fn now and then at each interval until ctx is done. Errors are
// logged and do not stop the loop.
func every(ctx context.Context, interval time.Duration, fn func(context.Context) error) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		err := fn(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.WarnContext(ctx, "Periodic task failed", slog.Any("error", err))
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("periodic task: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// untilDone runs fn until it succeeds, retrying after interval.
func untilDone(ctx context.Context, interval time.Duration, fn func(context.Context) error) error {
	for {
		err := fn(ctx)
		if err == nil {
			return nil
		}

		slog.WarnContext(ctx, "Task failed, retrying", slog.Any("error", err))

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()

			return fmt.Errorf("retry: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
