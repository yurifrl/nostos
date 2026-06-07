// Package main is the nostos-bootstrap controller entrypoint.
//
// This is the in-cluster, self-healing bootstrap controller (design doc
// .agents/drafts/nostos-cluster-bootstrap-controller.md). It runs as a
// single-replica Deployment on a control-plane node with hostNetwork, comes up
// before the CNI, and drives the cluster from bare apiserver to "ArgoCD
// reconciling the user's repo" via an ordered, idempotent reconcile loop.
//
// Invocation in-cluster: the container runs this binary with --interval and
// --log-format flags (see internal/bootstrap/manifests/controller-deployment.yaml).
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/rest"

	"github.com/yurifrl/nostos/internal/bootstrap"
)

// Version is the controller version; CI stamps the real value at build time.
const Version = "0.0.0-dev"

func main() {
	var (
		interval  = flag.Duration("interval", 60*time.Second, "reconcile / self-heal interval")
		logFormat = flag.String("log-format", "json", "log format: json|text")
		logLevel  = flag.String("log-level", "info", "log level: debug|info|warn|error")
		namespace = flag.String("namespace", envOr("NOSTOS_BOOTSTRAP_NAMESPACE", bootstrap.SystemNamespace), "namespace holding the bootstrap config/status/root-secret")
	)
	flag.Parse()

	logger := newLogger(*logFormat, *logLevel)
	logger.Info("nostos-bootstrap", "version", Version)

	cfg, err := rest.InClusterConfig()
	if err != nil {
		logger.Error("not running in-cluster: cannot build rest config", "err", err)
		os.Exit(1)
	}

	ctrl, err := bootstrap.New(cfg, bootstrap.Options{
		Namespace: *namespace,
		Interval:  *interval,
		Logger:    logger,
	})
	if err != nil {
		logger.Error("failed to build controller", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := ctrl.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Error("controller exited with error", "err", err)
		os.Exit(1)
	}
}

// newLogger builds a slog logger in JSON or text form (design §7: structured,
// JSON- and human-friendly).
func newLogger(format, level string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}
	var h slog.Handler
	switch format {
	case "text":
		h = slog.NewTextHandler(os.Stdout, opts)
	default:
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
