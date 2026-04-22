package swfruntime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/pgwf-go/installer"
	"github.com/colony-2/strata-go/pkg/daemon"
	"github.com/colony-2/swf-go/pkg/swf"
	directruntime "github.com/colony-2/swf-go/pkg/swf/runtime/direct"
	remoteruntime "github.com/colony-2/swf-go/pkg/swf/runtime/remote"
	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	_ "github.com/lib/pq"
	"golang.org/x/sys/unix"
)

const (
	embedScheme            = "embed"
	embedAPIKey            = "c2j-embed-token"
	defaultHTTPTimeout     = 30 * time.Second
	defaultSetupTimeout    = 45 * time.Second
	defaultShutdownTimeout = 10 * time.Second
)

type Handle struct {
	Runtime swf.WorkflowRuntime
	Engine  swf.SWFEngine
	cleanup func() error
}

func (h *Handle) Cleanup() error {
	if h == nil || h.cleanup == nil {
		return nil
	}
	return h.cleanup()
}

func Open(ctx context.Context, swfURL string) (*Handle, error) {
	swfURL = strings.TrimSpace(swfURL)
	if swfURL == "" {
		return nil, fmt.Errorf("SWF runtime URL is required")
	}

	parsed, err := url.Parse(swfURL)
	if err != nil {
		return nil, fmt.Errorf("parse SWF runtime URL: %w", err)
	}

	switch parsed.Scheme {
	case embedScheme:
		return openEmbed(ctx, parsed, swfURL)
	case "http", "https":
		return openRemote(swfURL)
	default:
		return nil, fmt.Errorf("unsupported SWF runtime URL %q", swfURL)
	}
}

func openRemote(swfURL string) (*Handle, error) {
	runtime, err := remoteruntime.New(swfURL, &http.Client{Timeout: defaultHTTPTimeout})
	if err != nil {
		return nil, fmt.Errorf("create remote runtime: %w", err)
	}

	engine, err := swf.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
	if err != nil {
		return nil, fmt.Errorf("build engine: %w", err)
	}

	return &Handle{
		Runtime: runtime,
		Engine:  engine,
		cleanup: func() error { return nil },
	}, nil
}

func openEmbed(ctx context.Context, parsed *url.URL, rawURL string) (*Handle, error) {
	if parsed == nil {
		return nil, fmt.Errorf("parse SWF runtime URL: missing parsed URL")
	}
	if parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, fmt.Errorf("unsupported SWF runtime URL %q: only embed:/// is supported", rawURL)
	}

	root, err := resolveEmbedRoot()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir embed root %s: %w", root, err)
	}

	lock, err := acquireEmbedLock(root)
	if err != nil {
		return nil, err
	}

	cleanupOnErr := func(err error) (*Handle, error) {
		return nil, errors.Join(err, lock.close())
	}

	setupCtx := ctx
	if setupCtx == nil {
		setupCtx = context.Background()
	}
	setupCtx, cancel := context.WithTimeout(setupCtx, defaultSetupTimeout)
	defer cancel()

	postgresDSN, postgresStop, err := startEmbeddedPostgres(root)
	if err != nil {
		return cleanupOnErr(err)
	}
	closePostgres := func() error {
		return postgresStop()
	}

	if err := installPGWF(setupCtx, postgresDSN); err != nil {
		return nil, errors.Join(err, closePostgres(), lock.close())
	}

	strata, err := startEmbeddedStrata(root)
	if err != nil {
		return nil, errors.Join(err, closePostgres(), lock.close())
	}

	runtime, err := directruntime.NewFromConfig(postgresDSN, strata.BaseURL, strata.APIKey)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("build direct runtime: %w", err),
			strata.shutdown(),
			closePostgres(),
			lock.close(),
		)
	}

	engine, err := swf.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("build engine: %w", err),
			strata.shutdown(),
			closePostgres(),
			lock.close(),
		)
	}

	return &Handle{
		Runtime: runtime,
		Engine:  engine,
		cleanup: func() error {
			return errors.Join(
				strata.shutdown(),
				closePostgres(),
				lock.close(),
			)
		},
	}, nil
}

func resolveEmbedRoot() (string, error) {
	if root := strings.TrimSpace(os.Getenv(defaults.EmbedRootEnv)); root != "" {
		if !filepath.IsAbs(root) {
			return "", fmt.Errorf("%s must be an absolute path", defaults.EmbedRootEnv)
		}
		return filepath.Clean(root), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for embedded SWF runtime: %w", err)
	}
	return filepath.Join(home, ".c2j", "embed", "default"), nil
}

type embedLock struct {
	file *os.File
}

func acquireEmbedLock(root string) (*embedLock, error) {
	lockPath := filepath.Join(root, "lock")
	file, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open runtime lock %s: %w", lockPath, err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("embedded SWF runtime at %s is already in use", root)
		}
		return nil, fmt.Errorf("lock runtime file %s: %w", lockPath, err)
	}
	if err := file.Truncate(0); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("truncate runtime lock %s: %w", lockPath, err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("rewind runtime lock %s: %w", lockPath, err)
	}
	if _, err := fmt.Fprintf(file, "pid=%d\n", os.Getpid()); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write runtime lock %s: %w", lockPath, err)
	}
	return &embedLock{file: file}, nil
}

func (l *embedLock) close() error {
	if l == nil {
		return nil
	}
	var err error
	if l.file != nil {
		err = errors.Join(
			unix.Flock(int(l.file.Fd()), unix.LOCK_UN),
			l.file.Close(),
		)
	}
	return err
}

func startEmbeddedPostgres(root string) (string, func() error, error) {
	pgPort, err := freeTCPPort()
	if err != nil {
		return "", nil, err
	}

	runtimePath := filepath.Join(root, "postgres", "runtime")
	dataPath := filepath.Join(root, "postgres", "data")
	if err := os.MkdirAll(runtimePath, 0o755); err != nil {
		return "", nil, fmt.Errorf("mkdir postgres runtime dir: %w", err)
	}
	if err := os.MkdirAll(dataPath, 0o755); err != nil {
		return "", nil, fmt.Errorf("mkdir postgres data dir: %w", err)
	}

	postgres := embeddedpostgres.NewDatabase(
		embeddedpostgres.DefaultConfig().
			Port(pgPort).
			RuntimePath(runtimePath).
			DataPath(dataPath),
	)
	if err := postgres.Start(); err != nil {
		return "", nil, fmt.Errorf("start embedded postgres: %w", err)
	}

	stop := func() error {
		return postgres.Stop()
	}
	dsn := fmt.Sprintf("postgres://postgres:postgres@localhost:%d/postgres?sslmode=disable", pgPort)
	return dsn, stop, nil
}

func installPGWF(ctx context.Context, dsn string) error {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}

	inst := installer.Installer{DB: db}
	if err := inst.Apply(ctx); err != nil {
		return fmt.Errorf("install pgwf schema: %w", err)
	}
	if err := inst.Verify(ctx); err != nil {
		return fmt.Errorf("verify pgwf schema: %w", err)
	}
	return nil
}

type strataHandle struct {
	BaseURL string
	APIKey  string
	daemon  *daemon.Daemon
}

func startEmbeddedStrata(root string) (*strataHandle, error) {
	rowDir := filepath.Join(root, "strata", "rows")
	blobDir := filepath.Join(root, "strata", "blobs")
	if err := os.MkdirAll(rowDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir strata row dir: %w", err)
	}
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir strata blob dir: %w", err)
	}

	cfg := daemon.Config{
		ListenAddr:             "127.0.0.1:0",
		RowStoreURI:            fmt.Sprintf("pebble://%s", filepath.ToSlash(rowDir)),
		BlobStoreURI:           fmt.Sprintf("blobfs://%s", filepath.ToSlash(blobDir)),
		MaxInlineArtifactBytes: daemon.DefaultMaxInlineArtifactBytes,
	}
	d, err := daemon.StartEmbedded(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("start embedded strata: %w", err)
	}

	addr, err := d.Addr()
	if err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
		defer cancel()
		return nil, errors.Join(
			fmt.Errorf("resolve embedded strata address: %w", err),
			d.Shutdown(shutdownCtx),
		)
	}

	return &strataHandle{
		BaseURL: "http://" + addr,
		APIKey:  embedAPIKey,
		daemon:  d,
	}, nil
}

func (h *strataHandle) shutdown() error {
	if h == nil || h.daemon == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
	defer cancel()
	return h.daemon.Shutdown(ctx)
}

func freeTCPPort() (uint32, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve postgres port: %w", err)
	}
	defer listener.Close()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("reserve postgres port: unexpected addr type %T", listener.Addr())
	}
	return uint32(addr.Port), nil
}
