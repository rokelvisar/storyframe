// Command server is the single binary: REST API + tus upload endpoint + FFmpeg
// orchestration + embedded Angular SPA.
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

	"github.com/rokelvisar/storyframe/backend/internal/analysis"
	"github.com/rokelvisar/storyframe/backend/internal/api"
	"github.com/rokelvisar/storyframe/backend/internal/engine"
	"github.com/rokelvisar/storyframe/backend/internal/immich"
	"github.com/rokelvisar/storyframe/backend/internal/store"
	tuswire "github.com/rokelvisar/storyframe/backend/internal/tusd"
	"github.com/rokelvisar/storyframe/backend/internal/web"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the local /healthz endpoint and exit")
	flag.Parse()
	if *healthcheck {
		os.Exit(runHealthcheck())
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := loadConfig()
	if err := os.MkdirAll(cfg.dataDir, 0o755); err != nil {
		log.Error("cannot create data dir", "dir", cfg.dataDir, "err", err)
		os.Exit(1)
	}

	var st *store.Store
	var err error
	if dsn := os.Getenv("MYSQL_DSN"); dsn != "" {
		st, err = store.OpenMySQL(dsn)
		if err != nil {
			log.Error("open mysql store", "err", err)
			os.Exit(1)
		}
		log.Info("job store: mysql")
	} else {
		st, err = store.Open(cfg.dbPath)
		if err != nil {
			log.Error("open sqlite store", "err", err)
			os.Exit(1)
		}
		log.Info("job store: sqlite", "path", cfg.dbPath)
	}
	defer st.Close()

	analysisCfg := analysis.FromEnv()
	provider := analysis.New(analysisCfg, log)
	immichCfg := immich.Config{BaseURL: os.Getenv("IMMICH_BASE_URL"), APIKey: os.Getenv("IMMICH_API_KEY")}
	var immichClient *immich.Client
	// Default on: if you've configured an Immich import, writing the analysis
	// back onto the source asset is the point of the round-trip. Opt out with
	// IMMICH_WRITEBACK=false.
	immichWriteback := envOr("IMMICH_WRITEBACK", "true") != "false"
	if immichCfg.Enabled() {
		immichClient = immich.New(immichCfg)
		log.Info("immich import enabled", "baseURL", immichCfg.BaseURL, "writeback", immichWriteback)
	} else {
		log.Info("immich import disabled (IMMICH_BASE_URL not set)")
	}

	eng := engine.New(st, provider, cfg.dataDir, cfg.ffmpegBin, cfg.workers, immichClient, immichWriteback, log)
	defer eng.Shutdown()

	tus, err := tuswire.New(cfg.dataDir, eng, log)
	if err != nil {
		log.Error("init tus", "err", err)
		os.Exit(1)
	}

	handler := api.NewRouter(api.Deps{
		Store:                 st,
		Engine:                eng,
		Tus:                   tus,
		DataDir:               cfg.dataDir,
		Immich:                immichClient,
		WebFS:                 web.FS(),
		Log:                   log,
		DefaultLanguageHint:   analysisCfg.DefaultLanguageHint,
		DefaultOutputLanguage: analysisCfg.DefaultOutputLanguage,
	})

	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		// No write/read timeout: 2 GB tus PATCH bodies stream for a long time.
	}

	go func() {
		log.Info("listening", "addr", srv.Addr, "dataDir", cfg.dataDir, "provider", provider.Name(), "workers", cfg.workers)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("serve", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

type config struct {
	port      string
	dataDir   string
	dbPath    string
	ffmpegBin string
	workers   int
}

func loadConfig() config {
	c := config{
		port:      envOr("PORT", "8080"),
		dataDir:   envOr("DATA_DIR", "/data"),
		ffmpegBin: envOr("FFMPEG_BIN", "ffmpeg"),
	}
	c.dbPath = envOr("DB_PATH", c.dataDir+"/video-extractor.sqlite")
	c.workers, _ = strconv.Atoi(envOr("FFMPEG_WORKERS", "2"))
	if c.workers <= 0 {
		c.workers = 2
	}
	return c
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func runHealthcheck() int {
	port := envOr("PORT", "8080")
	c := &http.Client{Timeout: 4 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%s/healthz", port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.StatusCode)
		return 1
	}
	return 0
}
