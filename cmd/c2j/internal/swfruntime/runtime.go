package swfruntime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/swf-go/pkg/swf"
	remoteruntime "github.com/colony-2/swf-go/pkg/swf/runtime/remote"
	sqliteruntime "github.com/colony-2/swf-go/pkg/swf/runtime/sqlite"
	"golang.org/x/sys/unix"
)

const (
	embedScheme            = "embed"
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
	baseRuntime, err := remoteruntime.New(swfURL, &http.Client{Timeout: defaultHTTPTimeout})
	if err != nil {
		return nil, fmt.Errorf("create remote runtime: %w", err)
	}
	runtime := withChapterVisibility(baseRuntime)

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

	baseRuntime, err := sqliteruntime.NewFromConfig(setupCtx, sqliteruntime.Config{
		DBPath: filepath.Join(root, "swf.db"),
	})
	if err != nil {
		return cleanupOnErr(fmt.Errorf("build sqlite runtime: %w", err))
	}
	runtime := withChapterVisibility(baseRuntime)

	engine, err := swf.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
	if err != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
		defer shutdownCancel()
		return nil, errors.Join(
			fmt.Errorf("build engine: %w", err),
			baseRuntime.Close(shutdownCtx),
			lock.close(),
		)
	}

	return &Handle{
		Runtime: runtime,
		Engine:  engine,
		cleanup: func() error {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
			defer shutdownCancel()
			return errors.Join(
				baseRuntime.Close(shutdownCtx),
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
