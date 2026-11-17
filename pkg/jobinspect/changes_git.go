package jobinspect

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type changesObjects struct {
	c                  *ChangesService
	ctx                context.Context
	cancel             context.CancelFunc
	root, repo         string
	budgetRoot         string
	repositoryIdentity string
	downloaded         int64
	lineageExamined    int
	sources            []ChangesSource
	repositoryObjects  []string
	loaded             map[string]bool
	loading            map[string]bool
	checkedTrees       map[string]bool
	checkedCommits     map[string]bool
}

type changesContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r changesContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, ChangesDiagnostic(err)
	}
	return r.r.Read(p)
}

func (c *ChangesService) objects(parent context.Context) (*changesObjects, error) {
	ctx, cancel := context.WithTimeout(parent, c.config.Limits.OperationTime)
	if err := ctx.Err(); err != nil {
		cancel()
		return nil, ChangesDiagnostic(err)
	}
	root, err := os.MkdirTemp(c.config.TempDir, "c2j-changes-")
	if err != nil {
		cancel()
		e := changesErr("temporary_storage", "cannot create private temporary storage")
		e.Cause = err
		return nil, e
	}
	o := &changesObjects{c: c, ctx: ctx, cancel: cancel, root: root, repo: filepath.Join(root, "objects.git"), loaded: map[string]bool{}, loading: map[string]bool{}}
	return o, nil
}
func (o *changesObjects) close() { o.cancel(); _ = os.RemoveAll(o.root) }
func (o *changesObjects) storageRoot() string {
	if o.budgetRoot != "" {
		return o.budgetRoot
	}
	return o.root
}

func (o *changesObjects) environment() []string {
	// An allowlist intentionally drops GIT_CONFIG*, alternates, object dirs,
	// replacements, credentials, proxy and SSH variables from the worker.
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + o.root, "XDG_CONFIG_HOME=" + o.root, "TMPDIR=" + o.root, "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0", "GIT_ALLOW_PROTOCOL="}
}

type changesBoundedWriter struct {
	w        io.Writer
	n, limit int64
	name     string
	err      error
}

func (w *changesBoundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.limit-w.n {
		w.err = changesLimit(w.name)
		return 0, w.err
	}
	n, e := w.w.Write(p)
	w.n += int64(n)
	return n, e
}

func directoryBytes(root string, limit int64) error {
	_, err := changesDirectoryUsage(root, limit)
	return err
}

func changesDirectoryUsage(root string, limit int64) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			info, e := d.Info()
			if e != nil {
				if os.IsNotExist(e) {
					return nil
				}
				return e
			}
			size += info.Size()
			if size > limit {
				return changesLimit("temporary_bytes")
			}
		}
		return nil
	})
	return size, err
}

// run supervises disk use during execution, bounds output and kills/waits for
// the complete process group on error/cancellation. Git stderr stays private.
func (o *changesObjects) run(input io.Reader, out io.Writer, outputLimit int64, outputName string, access *RepositoryAccess, args ...string) error {
	if !changesProcessGroups {
		return changesErr("protocol", "Changes object reads require Unix process-group supervision")
	}
	if err := o.ctx.Err(); err != nil {
		return ChangesDiagnostic(err)
	}
	ctx, cancel := context.WithTimeout(o.ctx, o.c.config.Limits.GitTime)
	defer cancel()
	argv := []string{"--no-replace-objects", "-c", "core.hooksPath=/dev/null", "-c", "init.templateDir=", "-c", "protocol.allow=never", "-c", "protocol.ext.allow=never", "-c", "core.attributesFile=/dev/null", "-c", "diff.external=", "-c", "core.commitGraph=false", "-c", "pack.threads=1", "-c", "pack.windowMemory=16m", "-c", "core.bigFileThreshold=1m", "-c", "gc.auto=0", "-c", "gc.autoDetach=false", "-c", "maintenance.auto=false", "-c", "fetch.writeCommitGraph=false", "-c", "fetch.fsckObjects=true", "-c", "transfer.fsckObjects=true", "-c", "submodule.recurse=false", "-c", "fetch.recurseSubmodules=false"}
	if access != nil {
		argv = append(argv, "-c", "protocol.file.allow=always", "-c", "protocol.http.allow=always", "-c", "protocol.https.allow=always", "-c", "protocol.ssh.allow=always")
	}
	argv = append(argv, "--git-dir="+o.repo)
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, o.c.config.GitPath, argv...)
	cmd.Env = o.environment()
	cmd.Stdin = input
	if access != nil { // Only the application resolver can authorize this overlay.
		cmd.Env = append(cmd.Env, "GIT_ALLOW_PROTOCOL=file:http:https:ssh")
		cmd.Env = append(cmd.Env, access.Environment...)
	}
	// Git sends the received pack trace to an inherited pipe. Counting this
	// stream covers both unpacked and retained packs without estimating object
	// directory growth or saving another copy of the payload. The bounded pipe
	// also applies backpressure while the budget supervisor stops the group.
	var downloadReader, downloadWriter *os.File
	var downloadDone chan changesDownloadResult
	remaining := o.c.config.Limits.DownloadBytes - o.downloaded
	if access != nil {
		if remaining <= 0 {
			return changesLimit("download_bytes")
		}
		var err error
		downloadReader, downloadWriter, err = os.Pipe()
		if err != nil {
			return changesErr("temporary_storage", "cannot supervise repository download")
		}
		defer downloadReader.Close()
		defer downloadWriter.Close()
		cmd.ExtraFiles = []*os.File{downloadWriter}
		cmd.Env = append(cmd.Env, "GIT_TRACE_PACKFILE=3")
		downloadDone = make(chan changesDownloadResult, 1)
	}
	stdout := &changesBoundedWriter{w: out, limit: outputLimit, name: outputName}
	cmd.Stdout = stdout
	// A discard with a bound also prevents unbounded diagnostic memory.
	var diagnostic bytes.Buffer
	stderr := &changesBoundedWriter{w: &diagnostic, limit: 64 << 10, name: "git_diagnostics"}
	cmd.Stderr = stderr
	configureChangesProcess(cmd)
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return ChangesDiagnostic(ctx.Err())
		}
		e := changesErr("git_unavailable", "cannot start Git; configure GitPath and executable access")
		e.Cause = err
		return e
	}
	if downloadDone != nil {
		_ = downloadWriter.Close() // EOF follows the last Git process, not this parent.
		go func() {
			n, err := io.Copy(io.Discard, io.LimitReader(downloadReader, remaining+1))
			if n > remaining {
				err = changesLimit("download_bytes")
			}
			if err != nil {
				cancel()
			}
			downloadDone <- changesDownloadResult{n, err}
		}()
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	var budgetErr error
	var mu sync.Mutex
	go func() {
		defer close(stopped)
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				if e := directoryBytes(o.storageRoot(), o.c.config.Limits.TemporaryBytes); e != nil {
					mu.Lock()
					budgetErr = e
					mu.Unlock()
					cancel()
					return
				}
			}
		}
	}()
	err := cmd.Wait()
	cleanupChangesProcess(cmd)
	close(done)
	<-stopped
	if downloadDone != nil {
		download := <-downloadDone
		o.downloaded += download.bytes
		if download.err != nil {
			return download.err
		}
		// Never claim a bounded successful fetch when Git did not expose its
		// incoming pack. Exact missing-object fetches must receive object bytes.
		if err == nil && download.bytes == 0 && ctx.Err() == nil {
			return changesErr("protocol", "Git did not expose repository download bytes")
		}
	}
	mu.Lock()
	limitErr := budgetErr
	mu.Unlock()
	if limitErr != nil {
		return limitErr
	}
	if stdout.err != nil {
		return stdout.err
	}
	if stderr.err != nil {
		return stderr.err
	}
	var ce *ChangesError
	if errors.As(err, &ce) {
		return ce
	}
	if ctx.Err() != nil {
		return ChangesDiagnostic(ctx.Err())
	}
	if err != nil {
		if access != nil {
			message := strings.ToLower(diagnostic.String())
			for _, s := range []string{"authentication failed", "could not read username", "http 401", "error: 401", "publickey"} {
				if strings.Contains(message, s) {
					return changesErr("repository_authentication", "configured repository authentication failed")
				}
			}
			for _, s := range []string{"permission denied", "access denied", "not authorized", "error: 403", "http 403", "repository not found"} {
				if strings.Contains(message, s) {
					return changesErr("repository_denied", "configured repository access was denied")
				}
			}
			for _, s := range []string{"not our ref", "couldn't find remote ref", "unadvertised object"} {
				if strings.Contains(message, s) {
					hash := args[len(args)-1]
					if fullHash(hash) {
						return missingChangesObjects(hash)
					}
				}
			}
			for _, s := range []string{"unpack", "bad object", "index-pack", "corrupt"} {
				if strings.Contains(message, s) {
					return changesErr("invalid_objects", "configured repository returned invalid Git objects")
				}
			}
			return changesErr("repository_unavailable", "configured repository could not be reached or read")
		}
		return changesErr("invalid_objects", "Git could not read or validate selected objects")
	}
	return directoryBytes(o.storageRoot(), o.c.config.Limits.TemporaryBytes)
}

type changesDownloadResult struct {
	bytes int64
	err   error
}

func (o *changesObjects) git(args ...string) ([]byte, error) {
	var b bytes.Buffer
	err := o.run(nil, &b, o.c.config.Limits.ObjectBytes, "object_bytes", nil, args...)
	return b.Bytes(), err
}
func (o *changesObjects) init(format string) error {
	_, err := o.git("init", "--bare", "--quiet", "--object-format="+format)
	return err
}

func (o *changesObjects) has(hash string) (bool, error) {
	_, e := o.git("cat-file", "-e", hash+"^{commit}")
	if e == nil {
		return true, nil
	}
	var diagnostic *ChangesError
	if errors.As(e, &diagnostic) && diagnostic.Code == "invalid_objects" {
		return false, nil
	}
	return false, e
}

// checkObjects checks all inflated sizes before asking Git to parse tree/blob
// closure. Object storage is monitored while index-pack/fsck run as well.
func (o *changesObjects) checkObjects() error {
	f, err := os.CreateTemp(o.root, "sizes-")
	if err != nil {
		return changesErr("temporary_storage", "cannot create object inventory")
	}
	defer func() { f.Close(); os.Remove(f.Name()) }()
	l := o.c.config.Limits
	if err = o.run(nil, f, int64(l.Objects)*128, "objects", nil, "cat-file", "--batch-all-objects", "--batch-check=%(objectname) %(objecttype) %(objectsize)"); err != nil {
		return err
	}
	if _, err = f.Seek(0, 0); err != nil {
		return err
	}
	return checkChangesObjectSizes(f, l)
}

func parseObjectSize(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

func (o *changesObjects) tree(hash string) (string, error) {
	b, e := o.git("cat-file", "-t", hash)
	if e != nil {
		return "", changesObjectReadError(e, hash)
	}
	if strings.TrimSpace(string(b)) != "commit" {
		return "", changesErr("invalid_objects", "endpoint is not a commit")
	}
	if e = o.validateCommit(hash); e != nil {
		return "", e
	}
	b, e = o.git("rev-parse", hash+"^{tree}")
	if e != nil {
		return "", e
	}
	tree := strings.TrimSpace(string(b))
	if !fullHash(tree) {
		return "", changesErr("invalid_objects", "invalid tree hash")
	}
	return tree, nil
}

func changesObjectReadError(err error, hash string) error {
	var diagnostic *ChangesError
	if errors.As(err, &diagnostic) && diagnostic.Code == "invalid_objects" {
		return missingChangesObjects(hash)
	}
	return err
}

func missingChangesObjects(hashes ...string) *ChangesError {
	e := changesErr("missing_closure", "required full-hash objects are unavailable")
	e.Missing = hashes
	return e
}
