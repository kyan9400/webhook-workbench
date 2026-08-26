package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	appserver "github.com/kyan9400/webhook-workbench/internal/server"
	"github.com/kyan9400/webhook-workbench/internal/store"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

type options struct {
	listen             string
	data               string
	token              string
	retention          int
	maxBody            int64
	allowPrivateReplay bool
	show               bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	defaults, err := environmentDefaults()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("webhook-workbench", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&defaults.listen, "listen", defaults.listen, "address to listen on")
	flags.StringVar(&defaults.data, "data", defaults.data, "JSON persistence path; empty keeps events in memory")
	flags.StringVar(&defaults.token, "token", defaults.token, "optional bearer token for inbox and API routes")
	flags.IntVar(&defaults.retention, "retention", defaults.retention, "maximum events to retain")
	flags.Int64Var(&defaults.maxBody, "max-body", defaults.maxBody, "maximum captured body size in bytes")
	flags.BoolVar(&defaults.allowPrivateReplay, "allow-private-replay", defaults.allowPrivateReplay, "allow replay targets on private and local networks")
	flags.BoolVar(&defaults.show, "version", false, "print version information")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if defaults.show {
		fmt.Fprintf(stdout, "webhook-workbench %s (%s, %s)\n", version, commit, date)
		return nil
	}
	if defaults.maxBody < 1 {
		return errors.New("max-body must be at least 1 byte")
	}

	events, err := store.New(defaults.data, defaults.retention)
	if err != nil {
		return fmt.Errorf("open event store: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(stdout, nil))
	application, err := appserver.New(appserver.Config{
		Store: events, Token: defaults.token, MaxBody: defaults.maxBody, Logger: logger,
		AllowPrivateReplay: defaults.allowPrivateReplay,
	})
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr: defaults.listen, Handler: application.Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	errorsChannel := make(chan error, 1)
	go func() {
		logger.Info("webhook workbench ready", "listen", defaults.listen, "retention", defaults.retention, "maxBody", defaults.maxBody, "authentication", defaults.token != "", "privateReplay", defaults.allowPrivateReplay)
		errorsChannel <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errorsChannel:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		logger.Info("webhook workbench stopped")
	}
	return nil
}

func environmentDefaults() (options, error) {
	defaults := options{
		listen: "127.0.0.1:8080", data: "data/events.json", token: os.Getenv("WEBHOOK_WORKBENCH_TOKEN"),
		retention: 200, maxBody: 1 << 20,
	}
	if value := os.Getenv("WEBHOOK_WORKBENCH_LISTEN"); value != "" {
		defaults.listen = value
	}
	if value, exists := os.LookupEnv("WEBHOOK_WORKBENCH_DATA"); exists {
		defaults.data = value
	}
	if value := os.Getenv("WEBHOOK_WORKBENCH_RETENTION"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return options{}, fmt.Errorf("invalid WEBHOOK_WORKBENCH_RETENTION: %w", err)
		}
		defaults.retention = parsed
	}
	if value := os.Getenv("WEBHOOK_WORKBENCH_MAX_BODY"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return options{}, fmt.Errorf("invalid WEBHOOK_WORKBENCH_MAX_BODY: %w", err)
		}
		defaults.maxBody = parsed
	}
	if value := os.Getenv("WEBHOOK_WORKBENCH_ALLOW_PRIVATE_REPLAY"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return options{}, fmt.Errorf("invalid WEBHOOK_WORKBENCH_ALLOW_PRIVATE_REPLAY: %w", err)
		}
		defaults.allowPrivateReplay = parsed
	}
	return defaults, nil
}
