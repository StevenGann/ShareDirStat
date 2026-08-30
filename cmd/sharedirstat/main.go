// Command sharedirstat is the ShareDirStat server.
//
// Usage:
//
//	sharedirstat [serve] [--config FILE] [--listen ADDR] [--data-dir DIR] [--log-level L] [--log-format F] [--base-path P]
//	sharedirstat check-config [--config FILE]     validate and print the effective configuration
//	sharedirstat healthcheck [--listen ADDR]      exit 0 if the local server answers /healthz (used by HEALTHCHECK)
//	sharedirstat version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/StevenGann/ShareDirStat/internal/api"
	"github.com/StevenGann/ShareDirStat/internal/config"
	"github.com/StevenGann/ShareDirStat/internal/events"
	"github.com/StevenGann/ShareDirStat/internal/metrics"
	"github.com/StevenGann/ShareDirStat/internal/model"
	"github.com/StevenGann/ShareDirStat/internal/ops"
	"github.com/StevenGann/ShareDirStat/internal/scan"
	"github.com/StevenGann/ShareDirStat/internal/share"
	"github.com/StevenGann/ShareDirStat/internal/snapshot"
	"github.com/StevenGann/ShareDirStat/internal/version"
	"github.com/StevenGann/ShareDirStat/web"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 && (!strings.HasPrefix(args[0], "-") || args[0] == "-h" || args[0] == "--help") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return serve(args, stderr)
	case "check-config":
		return checkConfig(args, stdout, stderr)
	case "healthcheck":
		return healthcheck(args, stderr)
	case "version":
		fmt.Fprintln(stdout, version.String())
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}

const usage = `ShareDirStat - disk usage analyser for mounted shares

Commands:
  serve          run the server (default)
  check-config   validate configuration and print the effective values
  healthcheck    probe the local server's /healthz endpoint
  version        print version information

Flags (serve, check-config):
  --config FILE      configuration file (default ` + config.DefaultPath + `, may be absent; env SDS_CONFIG)
  --listen ADDR      listen address (overrides server.listen)
  --base-path PATH   URL prefix (overrides server.base_path)
  --data-dir DIR     data directory (overrides data_dir)
  --log-level LEVEL  debug|info|warn|error
  --log-format FMT   json|text
`

func configFlags(fs *flag.FlagSet) (*string, *config.Overrides) {
	ov := &config.Overrides{}
	cfgPath := fs.String("config", os.Getenv("SDS_CONFIG"), "configuration file")
	fs.StringVar(&ov.Listen, "listen", "", "listen address")
	fs.StringVar(&ov.BasePath, "base-path", "", "URL prefix")
	fs.StringVar(&ov.DataDir, "data-dir", "", "data directory")
	fs.StringVar(&ov.LogLevel, "log-level", "", "log level")
	fs.StringVar(&ov.LogFormat, "log-format", "", "log format")
	return cfgPath, ov
}

func newLogger(cfg config.Log, w io.Writer) *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.Level))
	opts := &slog.HandlerOptions{Level: level}
	if cfg.Format == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

func serve(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath, ov := configFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, warnings, err := config.Load(*cfgPath, *ov)
	if err != nil {
		fmt.Fprintf(stderr, "configuration error:\n%v\n", err)
		return 1
	}
	log := newLogger(cfg.Log, stderr)
	log.Info("starting", "version", version.Version, "commit", version.Commit, "listen", cfg.Server.Listen, "base_path", cfg.Server.BasePath, "data_dir", cfg.DataDir)
	for _, w := range warnings {
		log.Warn(string(w))
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		log.Error("data directory is not writable", "path", cfg.DataDir, "error", err)
		return 1
	}

	m := metrics.New()
	reg := share.New(cfg, log)
	reg.CheckAll()
	m.SharesConfigured.Set(float64(reg.Len()))
	for _, s := range reg.All() {
		m.SetShareState(s.Config.ID, string(s.State()))
	}

	// The signal context is set up before the managers, so hooks that launch
	// background work are bound to the server's lifetime.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := snapshot.NewStore(cfg.DataDir)
	if err != nil {
		log.Error("cannot open the snapshot store", "error", err)
		return 1
	}
	bus := events.NewBroker()
	scans := scan.NewManager(cfg, reg, store, bus, log, scan.Hooks{
		OnScanStart: func(shareID string) {
			m.ScanRunning.WithLabelValues(shareID).Set(1)
		},
		OnScanFinish: func(shareID string, outcome scan.Outcome, d time.Duration, errs uint64) {
			m.ScanRunning.WithLabelValues(shareID).Set(0)
			m.ScansTotal.WithLabelValues(shareID, string(outcome)).Inc()
			m.ScanDuration.Observe(d.Seconds())
			m.ScanErrors.WithLabelValues(shareID).Add(float64(errs))
		},
		OnShareState: func(shareID string, state share.State) {
			m.SetShareState(shareID, string(state))
		},
		OnSnapshotSave: func(shareID string, bytes int64, d time.Duration) {
			m.SnapshotBytes.WithLabelValues(shareID).Set(float64(bytes))
			m.SnapshotWriteDuration.Observe(d.Seconds())
		},
		OnResults: func(shareID string, gen *model.Generation) {
			st := gen.Stats()
			m.ShareNodes.WithLabelValues(shareID, "file").Set(float64(st.Files))
			m.ShareNodes.WithLabelValues(shareID, "dir").Set(float64(st.Dirs))
			m.ShareBytes.WithLabelValues(shareID, "apparent").Set(float64(st.Size))
			m.ShareBytes.WithLabelValues(shareID, "allocated").Set(float64(st.Alloc))
			m.ShareGenerationTime.WithLabelValues(shareID).Set(float64(gen.ScannedAt().Unix()))
		},
	})

	fileOps, err := ops.NewManager(cfg, reg, bus, log, ops.Hooks{
		OnDelete: func(shareID, outcome string, freed uint64) {
			m.DeleteOps.WithLabelValues(shareID, outcome).Inc()
			m.DeleteBytesFreed.WithLabelValues(shareID).Add(float64(freed))
		},
		OnDownload: func(shareID, kind string, bytes int64) {
			m.DownloadBytes.WithLabelValues(shareID, kind).Add(float64(bytes))
		},
		// A delete that only partly succeeded leaves the model and the disk
		// disagreeing; a rescan of the parent is the only honest fix.
		StartRescan: func(shareID, path string) error {
			_, err := scans.Start(ctx, shareID, path, scan.TriggerReconcile)
			return err
		},
		RunningScanPath: func(shareID string) (string, bool) {
			st, running := scans.RunningFor(shareID)
			return st.Path, running
		},
		MarkDirty: scans.MarkDirty,
	})
	if err != nil {
		log.Error("cannot open the audit log", "error", err)
		return 1
	}
	defer func() { _ = fileOps.Close() }()

	srv := api.New(api.Deps{
		Config:  cfg,
		Shares:  reg,
		Scans:   scans,
		Ops:     fileOps,
		Events:  bus,
		Metrics: m,
		Log:     log,
		UI:      web.Dist(),
		Owners:  api.NewOwnerResolver(cfg.UI.OwnerNames, cfg.UI.GroupNames),
	})
	httpSrv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       cfg.Server.ReadTimeout.D(),
		// No WriteTimeout: downloads and SSE streams are long-lived by design.
		IdleTimeout: 120 * time.Second,
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Server.Listen)
	if err != nil {
		log.Error("cannot listen", "addr", cfg.Server.Listen, "error", err)
		return 1
	}

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()

	// Restore the last results before declaring readiness, so the first
	// request after a restart is served from real data (NFR-7).
	scans.LoadSnapshots()
	for _, s := range reg.All() {
		m.SetShareState(s.Config.ID, string(s.State()))
	}
	srv.SetReady(true)
	log.Info("ready", "addr", ln.Addr().String(), "shares", reg.Len())

	scans.StartScheduler(ctx)
	scans.StartupScans(ctx)
	if cfg.Operations.Delete.Trash.Enabled {
		go purgeTrashPeriodically(ctx, fileOps, log)
	}

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "error", err)
			return 1
		}
	case <-ctx.Done():
		log.Info("shutdown requested")
	}
	// Restore the default disposition so a second Ctrl-C or SIGTERM kills the
	// process. Without this the handler stays installed for the whole shutdown
	// and an operator watching it hang has no way to interrupt short of -9.
	stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout.D())
	defer cancel()
	srv.SetReady(false)
	// Release the event streams before Shutdown. They are never idle, so
	// Shutdown would otherwise wait out the full timeout with a single browser
	// tab open, then report failure for what was a clean, requested stop.
	srv.Close()
	// Scans get part of the budget, so one blocked on a hung NFS mount cannot
	// consume all of it and leave in-flight downloads to be cut off hard.
	scanCtx, cancelScans := context.WithTimeout(shutdownCtx, cfg.Server.ShutdownTimeout.D()*2/3)
	// Stop scanning first: a cancelled scan throws away its partial
	// generation, leaving the last complete results on disk (NFR-9).
	scans.Close(scanCtx)
	cancelScans()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("forced shutdown", "error", err)
	}
	log.Info("stopped")
	return 0
}

// purgeTrashPeriodically removes expired trash batches (FR-DEL-07). Hourly is
// ample for a retention measured in days.
func purgeTrashPeriodically(ctx context.Context, fileOps *ops.Manager, log *slog.Logger) {
	fileOps.PurgeTrash()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			log.Debug("purging expired trash")
			fileOps.PurgeTrash()
		}
	}
}

func checkConfig(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check-config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath, ov := configFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, warnings, err := config.Load(*cfgPath, *ov)
	if err != nil {
		fmt.Fprintf(stderr, "configuration error:\n%v\n", err)
		return 1
	}
	for _, w := range warnings {
		fmt.Fprintf(stderr, "warning: %s\n", w)
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "# effective configuration\n%s", out)
	return 0
}

// healthcheck is used by the container HEALTHCHECK; there is no shell or curl
// in the image. It probes 127.0.0.1 on the configured port.
func healthcheck(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	// The container's HEALTHCHECK runs this with no arguments, so the defaults
	// have to find the same listen address and base path the server is using.
	// Reading only the environment was not enough: an operator who sets either
	// in /config/config.yaml -- the documented way -- got a probe pointed at
	// the wrong port or path, and a permanently unhealthy container.
	cfgPath := fs.String("config", os.Getenv("SDS_CONFIG"), "configuration file to read listen/base_path from")
	listen := fs.String("listen", "", "listen address of the server (overrides the config file)")
	basePath := fs.String("base-path", "", "base path of the server (overrides the config file)")
	timeout := fs.Duration("timeout", 3*time.Second, "probe timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *listen == "" || *basePath == "" {
		// Best effort: a config that does not load is not this command's
		// problem to report, and the env/built-in defaults still apply.
		l, b := ":8080", "/"
		if cfg, _, err := config.Load(*cfgPath, config.Overrides{}); err == nil {
			l, b = cfg.Server.Listen, cfg.Server.BasePath
		}
		if *listen == "" {
			*listen = envOr("SDS_SERVER__LISTEN", l)
		}
		if *basePath == "" {
			*basePath = envOr("SDS_SERVER__BASE_PATH", b)
		}
	}
	host, port, err := net.SplitHostPort(*listen)
	if err != nil {
		fmt.Fprintf(stderr, "invalid listen address %q: %v\n", *listen, err)
		return 2
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	url := fmt.Sprintf("http://%s/%s", net.JoinHostPort(host, port), strings.TrimPrefix(strings.TrimSuffix(*basePath, "/")+"/healthz", "/"))
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintf(stderr, "unhealthy: %v\n", err)
		return 2
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "unhealthy: %v\n", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "unhealthy: %s returned %s\n", url, resp.Status)
		return 1
	}
	return 0
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
