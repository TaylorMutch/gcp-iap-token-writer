package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"

	stdlog "log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"golang.org/x/oauth2"
	"google.golang.org/api/idtoken"
)

const (
	// httpTimeout is how long a read or write can take for a
	// specific request
	httpTimeout = time.Second * 30

	// httpIdleTimeout is how long before an idle connection
	// is timed out and closed
	httpIdleTimeout = time.Minute * 5

	// httpShutdownTimeout is how long the context will wait
	// for graceful http server shutdown to complete. This
	// should be under the terminationGracePeriodSeconds k8s
	// setting for the pod (default is 30 seconds)
	httpShutdownTimeout = time.Second * 20
)

type Settings struct {
	LogLevel   string
	Filepath   string
	Audience   string
	MetricPort string
}

func (s *Settings) Validate() error {
	if s.LogLevel == "" {
		return errors.New("log level must be non-empty")
	}
	if s.Filepath == "" {
		return errors.New("filepath must be non-empty")
	}
	if s.Audience == "" {
		return errors.New("audience must be non-empty")
	}
	if s.MetricPort == "" {
		return errors.New("metricport must be non-empty")
	}
	return nil
}

var (
	flagLogLevel   = flag.String("v", "info", "Set the log level. Options are [debug, info, warn, error].")
	flagMetricPort = flag.String("metricport", "8080", "Set the port to expose prometheus metrics, defaults to 8080")
	flagAudience   = flag.String("audience", "", "Set the Identity Aware Proxy audience value. Example: RANDOMSTRING.apps.googleusercontent.com")
	flagFilepath   = flag.String("filepath", "gcptoken", "Set the path for where the token should be written to. Default is current/directory/gcptoken.")
)

// newLogger creates a new logger for the application
func newLogger(logLevel string) log.Logger {
	var (
		logger log.Logger
		lvl    level.Option
	)

	switch logLevel {
	case "error":
		lvl = level.AllowError()
	case "warn":
		lvl = level.AllowWarn()
	case "info":
		lvl = level.AllowInfo()
	case "debug":
		lvl = level.AllowDebug()
	default:
		// This enum is already checked and enforced by flag validations, so
		// this should never happen.
		panic("unexpected log level")
	}

	logger = log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	logger = level.NewFilter(logger, lvl)
	return log.With(logger, "ts", log.DefaultTimestampUTC, "caller", log.DefaultCaller)
}

func fatalError(logger log.Logger, msg string, err error) {
	_ = level.Error(logger).Log("msg", msg, "err", err.Error())
	os.Exit(1)
}

// tokenWriter is
type tokenWriter struct {
	ctx          context.Context
	ts           oauth2.TokenSource
	currentToken *oauth2.Token
	filepath     string
	logger       log.Logger
}

// newTokenWriter returns a new tokenWriter for placing tokens on disk
func newTokenWriter(ctx context.Context, logger log.Logger, conf Settings) (*tokenWriter, error) {

	ts, err := idtoken.NewTokenSource(ctx, conf.Audience)
	if err != nil {
		_ = level.Error(logger).Log("msg", "failed to create token source", "err", err.Error())
		return nil, err
	}

	if conf.Filepath == "" {
		return nil, errors.New("filepath must be non-empty")
	}

	tw := &tokenWriter{
		ctx:          ctx,
		ts:           ts,
		currentToken: nil,
		filepath:     conf.Filepath,
		logger:       logger,
	}
	return tw, nil
}

// refreshUntilCancel starts a refresh loop that always
func (tw *tokenWriter) refreshUntilCancel() {

	// Interval should be less than 10 seconds, since the default delta
	// for refreshing a token is 10 seconds before
	// 2 seconds gives us multiple opportunities within the 10s renewal period
	// to write to the file
	ticker := time.NewTicker(time.Second * 2)
	defer ticker.Stop()

	_ = level.Info(tw.logger).Log("msg", "starting token refresh loop")
	for {
		tw.refresh()
		select {
		case <-tw.ctx.Done():
			_ = level.Info(tw.logger).Log("msg", "stopping token refresh loop")
			return
		case <-ticker.C:
		}
	}
}

// refresh updates the token in the destination for which it is managed.
// The underlying TokenSource performs the logic of how the token is refreshed,
// therefore we must ensure that the current value is what is passed to the
// written destination.
func (tw *tokenWriter) refresh() {
	var err error

	// Check if the current token is still valid. If so, nothing to be done
	if tw.currentToken.Valid() {
		_ = level.Debug(tw.logger).Log("msg", "token still valid, skipping write to disk")
		return
	}

	// Otherwise, update the token on disk.
	_ = level.Info(tw.logger).Log("msg", "renewing token on disk")
	tw.currentToken, err = tw.ts.Token()

	if err != nil {
		_ = level.Error(tw.logger).Log("msg", "failed to get access token", "err", err.Error())
	}

	err = ioutil.WriteFile(tw.filepath, []byte(tw.currentToken.AccessToken), 0644)
	if err != nil {
		_ = level.Error(tw.logger).Log("msg", fmt.Sprintf("failed to write access token to %s", tw.filepath), "err", err.Error())
	}
}

// handleSignal captures SIGTERM (like from k8s) and Interrupt
// (like ctrl+c) signals to keep them from immediately ending
// the program and instead cancels the context
func handleSignal(logger log.Logger) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer cancel()
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM, os.Interrupt)
		// wait for signal
		<-c
		_ = level.Info(logger).Log("msg", "received shutdown signal")
	}()
	return ctx
}

// gracefulShutdown allows us to shutdown the metrics server when the
// shutdown signal has been sent
func gracefulShutdown(ctx context.Context, logger log.Logger, server *http.Server) {
	// wait for signal
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer cancel()
	_ = level.Info(logger).Log("msg", "gracefully shutting down the metrics server")
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = level.Error(logger).Log("msg", "unable to shutdown metrics server", err, err.Error())
	}
}

func main() {

	flag.Parse()

	// Setup configuration
	conf := Settings{
		Audience:   *flagAudience,
		Filepath:   *flagFilepath,
		LogLevel:   *flagLogLevel,
		MetricPort: *flagMetricPort,
	}
	if err := conf.Validate(); err != nil {
		panic(err)
	}

	logger := newLogger(conf.LogLevel)
	ctx := handleSignal(logger)

	// Create token writer and start routine
	tw, err := newTokenWriter(ctx, logger, conf)
	if err != nil {
		fatalError(logger, "failed to initialize token writer, shutting down", err)
	}
	go tw.refreshUntilCancel()

	// Create metrics server
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	http.Handle("/metrics", promhttp.Handler())
	server := &http.Server{
		Addr:         fmt.Sprintf(":%s", conf.MetricPort),
		ReadTimeout:  httpTimeout,
		WriteTimeout: httpTimeout,
		IdleTimeout:  httpIdleTimeout,
	}

	// Handle graceful shutdown
	go gracefulShutdown(ctx, logger, server)
	_ = level.Info(logger).Log("msg", fmt.Sprintf("listening for metrics requests on :%s", conf.MetricPort))
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		stdlog.Fatal(err)
	}
}
