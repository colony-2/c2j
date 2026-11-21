package extensions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"golang.org/x/sys/unix"
)

// stageArtifactFiles changes only the detached subprocess payload. Neither the
// prepared invocation nor its compact transport ever receives these descriptors.
func (r *ResolvedOp) stageArtifactFiles(ctx context.Context, deps ops.OpDependencies, payload map[string]any) (_ map[string]any, cleanup func() error, err error) {
	cleanup = func() error { return nil }
	if _, err := r.ArtifactFileKeys(payload); err != nil {
		return nil, cleanup, err
	}
	var names []string
	for _, name := range r.artifactFiles {
		if _, present := payload[name]; present {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return payload, cleanup, nil
	}
	root := objectPaths(deps).Workdir
	if !filepath.IsAbs(root) {
		return nil, cleanup, fmt.Errorf("artifact file staging requires an absolute invocation workdir")
	}
	dir, err := os.MkdirTemp(root, "artifact-files-*")
	if err != nil {
		return nil, cleanup, fmt.Errorf("create artifact file staging: %w", fileIOError(err))
	}
	cleanup = func() error {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove artifact file staging: %w", fileIOError(err))
		}
		return nil
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, cleanup())
		}
	}()
	out := make(map[string]any, len(payload))
	for k, v := range payload {
		out[k] = v
	}
	for i, name := range names {
		ref, _ := artifacts.ParseFileRef(payload[name]) // all values validated above
		path := filepath.Join(dir, fmt.Sprintf("%06d", i))
		if err := copyArtifactFile(ctx, deps, ref, path); err != nil {
			return nil, cleanup, fmt.Errorf("inputs.%s: %w", name, err)
		}
		out[name] = map[string]any{"ref": payload[name], "path": path}
	}
	return out, cleanup, nil
}

func fileIOError(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err // never put generated staging paths in recorded errors
	}
	return err
}

type artifactFileWriter struct {
	ctx context.Context
	w   io.Writer
}

func (w artifactFileWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.w.Write(p)
}

func copyArtifactFile(ctx context.Context, deps ops.OpDependencies, ref artifacts.Ref, path string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create staged file: %w", fileIOError(err))
	}
	defer func() { err = errors.Join(err, fileIOError(f.Close())) }()
	w := artifactFileWriter{ctx: ctx, w: f}
	if key, ok := ref.StoredKey(); ok {
		artifact, err := resolveArtifactFile(ctx, deps, key)
		if err != nil {
			return fmt.Errorf("unavailable stored artifact: %w", err)
		}
		if err := artifact.WriteTo(ctx, w); err != nil {
			return fmt.Errorf("read stored artifact: %w", fileIOError(err))
		}
		return ctx.Err()
	}
	u, err := artifacts.FileRefURL(ref.External.URL)
	if err != nil {
		return err
	}
	var source io.ReadCloser
	if u.Scheme == "file" {
		file, err := openArtifactFileSource(u.Path, unix.Open)
		if err != nil {
			return err
		}
		source = file
	} else {
		transport := &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true}
		if base, ok := http.DefaultTransport.(*http.Transport); ok {
			transport = base.Clone()
		}
		if transport.TLSClientConfig != nil {
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
			transport.TLSClientConfig.InsecureSkipVerify = false
		}
		transport.DisableCompression = true // preserve response bytes exactly
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("unsupported redirect scheme")
			}
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return fmt.Errorf("create external artifact request: %w", err)
		}
		response, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("fetch external artifact: %w", err)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			_ = response.Body.Close()
			return fmt.Errorf("unavailable external artifact: HTTP status %d", response.StatusCode)
		}
		source = response.Body
	}
	defer func() { err = errors.Join(err, fileIOError(source.Close())) }()
	if _, err := io.Copy(w, source); err != nil {
		return fmt.Errorf("read external artifact: %w", fileIOError(err))
	}
	return ctx.Err()
}

func resolveArtifactFile(ctx context.Context, deps ops.OpDependencies, key jobdb.ArtifactKey) (jobdb.Artifact, error) {
	for _, artifact := range deps.GetInputArtifacts() {
		if artifact == nil {
			continue
		}
		got, err := artifact.ArtifactKey()
		if err == nil && got.JobId == key.JobId && got.TaskOrdinal == key.TaskOrdinal && got.Name == key.Name {
			return artifact, nil
		}
	}
	if deps.WorkflowControl() == nil || deps.JobTool() == nil || deps.JobTool().GetJobKey().TenantId == "" {
		return nil, fmt.Errorf("tenant-scoped artifact resolution context is unavailable")
	}
	artifact := deps.WorkflowControl().GetArtifactLazy(ctx, deps.JobTool().GetJobKey().TenantId, key)
	if artifact == nil {
		return nil, fmt.Errorf("artifact not found")
	}
	return artifact, nil
}

// openArtifactFileSource checks both the pathname and the opened handle. The
// opener argument permits a deterministic source-swap test between those checks.
func openArtifactFileSource(path string, open func(string, int, uint32) (int, error)) (*os.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("open external file: %w", fileIOError(err))
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("unsupported external file source: requires a regular file")
	}
	// Nonblocking open prevents a substituted FIFO from hanging the worker.
	fd, err := open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open external file: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		closeErr := fileIOError(file.Close())
		if err != nil {
			return nil, errors.Join(fmt.Errorf("stat external file: %w", fileIOError(err)), closeErr)
		}
		return nil, errors.Join(fmt.Errorf("unsupported external file source: requires a regular file"), closeErr)
	}
	return file, nil
}
