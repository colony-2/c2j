package ops

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/colony-2/c2j/internal/extensioneagain"
	"github.com/colony-2/c2j/internal/jsonexact"
	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/objects"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/secrets"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

const (
	recordName = "_c2j/eagain-v1.json"
	inboxName  = "_c2j/eagain-inbox.tar"
	outboxName = "_c2j/eagain-outbox.tar"
)

type ContinuationResult struct {
	extensioneagain.Resume
	Attachment string `json:"attachment"`
}

type ContinuationInput struct {
	extensioneagain.Resume
	Checkout coretask.WorkspaceState `json:"checkout"`
	Inbox    jobdb.ArtifactKey       `json:"inbox"`
	Outbox   *jobdb.ArtifactKey      `json:"outbox,omitempty"`
	Pack     *jobdb.ArtifactKey      `json:"pack,omitempty"`
	Selector string                  `json:"resolved_selector"`
}

type continuationRecord struct {
	Result     ContinuationResult        `json:"result"`
	Request    extensioneagain.Request   `json:"request"`
	Invocation ActivityInvocationRequest `json:"invocation"`
	Checkout   coretask.WorkspaceState   `json:"checkout"`
	Selector   string                    `json:"resolved_selector"`
	OutputPack bool                      `json:"output_pack,omitempty"`
}

type continuationCapture struct {
	inbox    jobdb.Artifact
	checkout coretask.WorkspaceState
	pack     jobdb.Artifact
	outbox   jobdb.Artifact
	request  extensioneagain.Request
}

func (saved *continuationCapture) cleanup() {
	for _, artifact := range []jobdb.Artifact{saved.inbox, saved.outbox, saved.pack} {
		if artifact != nil {
			_ = artifact.Cleanup()
		}
	}
}

func continuationSelector(resolved *extops.ResolvedOp) string {
	if resolved == nil {
		return ""
	}
	if resolved.Nix != nil {
		return resolved.Selector
	}
	return resolved.ResolvedSelector
}

func (saved *continuationCapture) result(ctx context.Context, request ActivityInvocationRequest, resolved *extops.ResolvedOp) (*ContinuationResult, []jobdb.Artifact, error) {
	resume := extensioneagain.Resume{Version: extensioneagain.Version, Mode: saved.request.Mode, Count: 1}
	if request.Continuation != nil {
		resume.Count = request.Continuation.Count + 1
	}
	selector := continuationSelector(resolved)
	if !resume.Valid() || !saved.request.Valid() || saved.inbox == nil || selector == "" {
		return nil, nil, errors.New("invalid extension EAGAIN continuation state")
	}
	extension := *request.Preparation.Extension
	extension.RawInputs, extension.TemplateContext = nil, nil
	extension.Selector = selector
	extension.Options.BaseDir = ""
	extension.PinnedNix = resolved.Nix
	request.Preparation.Extension = &extension
	request.RestoreArtifact, request.Continuation = nil, nil
	marker := ContinuationResult{Resume: resume, Attachment: recordName}
	record := continuationRecord{Result: marker, Request: saved.request, Invocation: request, Checkout: saved.checkout, Selector: selector, OutputPack: saved.pack != nil}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, nil, err
	}
	if err := secrets.FromContext(ctx).CheckOutput(string(data)); err != nil {
		return nil, nil, err
	}
	artifacts := []jobdb.Artifact{jobdb.NewArtifactFromBytes(recordName, data), saved.inbox}
	if saved.outbox != nil {
		artifacts = append(artifacts, saved.outbox)
	}
	if saved.pack != nil {
		artifacts = append(artifacts, saved.pack)
	}
	return &marker, artifacts, nil
}

func ReadContinuation(ctx context.Context, data jobdb.TaskData, marker ContinuationResult, job jobdb.JobKey, expectedCount uint64, git gitstate.GlobalGitTaskContext, isConst bool) (ActivityInvocationRequest, error) {
	var empty ActivityInvocationRequest
	bad := errors.New("invalid extension EAGAIN continuation attachment")
	if !marker.Resume.Valid() || marker.Count != expectedCount || marker.Attachment != recordName {
		return empty, bad
	}
	artifacts, err := data.GetArtifacts()
	if err != nil {
		return empty, err
	}
	byName := map[string]jobdb.Artifact{}
	for _, artifact := range artifacts {
		if artifact.Name() == recordName || artifact.Name() == inboxName || artifact.Name() == outboxName || artifact.Name() == thinPackSentinel {
			if byName[artifact.Name()] != nil {
				return empty, bad
			}
			byName[artifact.Name()] = artifact
		}
	}
	recordArtifact := byName[recordName]
	if recordArtifact == nil {
		return empty, bad
	}
	key, err := recordArtifact.ArtifactKey()
	if err != nil || key.Validate() != nil || key.JobId != job.JobId || key.Name != recordName {
		return empty, bad
	}
	getKey := func(name string) (*jobdb.ArtifactKey, error) {
		artifact := byName[name]
		if artifact == nil {
			return nil, bad
		}
		other, err := artifact.ArtifactKey()
		if err != nil || other.Validate() != nil || other.JobId != key.JobId || other.TaskOrdinal != key.TaskOrdinal || other.Name != name {
			return nil, bad
		}
		return &other, nil
	}
	raw, err := recordArtifact.Bytes(ctx)
	if err != nil {
		return empty, err
	}
	var record continuationRecord
	if jsonexact.Unmarshal(raw, &record) != nil || record.Result != marker || !record.Request.Valid() || record.Request.Mode != marker.Mode {
		return empty, bad
	}
	request := record.Invocation
	if request.Preparation.Extension == nil || request.Preparation.Extension.Selector != record.Selector || record.Selector == "" || request.Const != isConst || request.InputArtifact != "" || request.Continuation != nil || request.RestoreArtifact != nil ||
		request.GitTaskContext.NodePath != git.NodePath || request.GitTaskContext.InvokeHash != git.InvokeHash || request.GitTaskContext.BaseRepo != git.BaseRepo || request.GitTaskContext.BaseRef != git.BaseRef || scopeID(request.GitTaskContext) != scopeID(git) || !validCheckout(record.Checkout, git) {
		return empty, bad
	}
	if extops.IsNixSelector(record.Selector) != (request.Preparation.Extension.PinnedNix != nil) {
		return empty, bad
	}
	for name, binding := range request.Artifacts {
		if validateBindingName(name) != nil || binding.Validate() != nil {
			return empty, bad
		}
	}
	inbox, err := getKey(inboxName)
	if err != nil {
		return empty, err
	}
	resume := ContinuationInput{Resume: marker.Resume, Checkout: record.Checkout, Inbox: *inbox, Selector: record.Selector}
	if marker.Mode == extensioneagain.Persist {
		resume.Outbox, err = getKey(outboxName)
		if err != nil {
			return empty, err
		}
		inputs, ok := request.Input["inputs"].(map[string]any)
		if !ok {
			return empty, bad
		}
		for name, value := range *record.Request.NextInputs {
			inputs[name] = value
		}
	} else if byName[outboxName] != nil {
		return empty, bad
	}
	if record.OutputPack {
		resume.Pack, err = getKey(thinPackSentinel)
		if err != nil {
			return empty, err
		}
	} else if byName[thinPackSentinel] != nil {
		return empty, bad
	}
	if !resume.valid(git, job.JobId) {
		return empty, bad
	}
	request.Continuation = &resume
	return request, nil
}

func captureContinuation(ctx context.Context, controller workspaceController, git *gitstate.GitTaskContext, before coretask.WorkspaceState, pack jobdb.Artifact, outbox string, invocation ActivityInvocationRequest, request extensioneagain.Request) error {
	saved := invocation.capture
	checkout := before
	newPack := false
	var artifacts []jobdb.Artifact
	if request.Mode == extensioneagain.Persist {
		var err error
		saved.outbox, err = snapshotContinuationFiles(ctx, outbox, outboxName, true)
		if err != nil {
			return err
		}
		if !invocation.Const {
			persisted, generated, err := controller.PersistWithDiffs(ctx, git)
			artifacts = generated
			defer func() {
				for _, artifact := range artifacts {
					_ = artifact.Cleanup()
				}
			}()
			if err != nil {
				return err
			}
			checkout.BaseCommit = git.ResolvedBaseHash
			checkout.EmptyBase = git.Workspace.HasEmptyBase(git.ResolvedBaseHash, git.ParentHash)
			checkout.HeadCommit, err = common.HeadCommit(ctx, git.WorktreePath, checkout.EmptyBase)
			if err != nil {
				return err
			}
			newPack = persisted != nil && persisted.HasChanges
		}
	}
	needsPack := checkout.HeadCommit != "" && checkout.HeadCommit != checkout.BaseCommit
	if needsPack {
		var snapshot jobdb.Artifact
		for _, artifact := range artifacts {
			if artifact.Name() != thinPackSentinel {
				continue
			}
			if snapshot != nil {
				return errors.New("multiple extension EAGAIN snapshots")
			}
			snapshot = artifact
		}
		if snapshot == nil && !newPack {
			snapshot = pack
		}
		if snapshot == nil {
			return errors.New("extension EAGAIN checkout cannot be restored exactly")
		}
		var err error
		saved.pack, err = copyContinuationPack(ctx, snapshot)
		if err != nil {
			return err
		}
	}
	if !validCheckout(checkout, invocation.GitTaskContext) {
		return errors.New("extension EAGAIN checkout cannot be restored exactly")
	}
	saved.checkout, saved.request = checkout, request
	return nil
}

func copyContinuationPack(ctx context.Context, pack jobdb.Artifact) (jobdb.Artifact, error) {
	file, err := os.CreateTemp("", "c2j-eagain-*.pack")
	if err != nil {
		return nil, err
	}
	if err := errors.Join(pack.WriteTo(ctx, file), file.Close()); err != nil {
		_ = os.Remove(file.Name())
		return nil, err
	}
	artifact, err := jobdb.NewArtifactFromFile(thinPackSentinel, file.Name())
	if err != nil {
		_ = os.Remove(file.Name())
	}
	return artifact, err
}

func scopeID(git gitstate.GlobalGitTaskContext) string {
	if git.Workspace != nil {
		return git.Workspace.ScopeID
	}
	return ""
}

func validCheckout(checkout coretask.WorkspaceState, git gitstate.GlobalGitTaskContext) bool {
	if checkout.ScopeID != scopeID(git) || checkout.Repository != git.BaseRepo || checkout.BaseRef != git.BaseRef {
		return false
	}
	validHash := func(hash string) bool {
		return (len(hash) == 40 || len(hash) == 64) && strings.Trim(hash, "0123456789abcdef") == ""
	}
	if checkout.EmptyBase {
		return git.Workspace.HasEmptyBase("", "") && checkout.BaseCommit == "" && (checkout.HeadCommit == "" || validHash(checkout.HeadCommit))
	}
	return validHash(checkout.HeadCommit) && validHash(checkout.BaseCommit)
}

func (resume ContinuationInput) valid(git gitstate.GlobalGitTaskContext, jobID string) bool {
	validKey := func(key jobdb.ArtifactKey, name string) bool {
		return key.Validate() == nil && key.JobId == jobID && key.Name == name
	}
	needsPack := resume.Checkout.HeadCommit != "" && resume.Checkout.HeadCommit != resume.Checkout.BaseCommit
	return resume.Resume.Valid() && validCheckout(resume.Checkout, git) && resume.Selector != "" && validKey(resume.Inbox, inboxName) &&
		((resume.Mode == extensioneagain.Abandon && resume.Outbox == nil) || (resume.Mode == extensioneagain.Persist && resume.Outbox != nil && validKey(*resume.Outbox, outboxName) && resume.Outbox.TaskOrdinal == resume.Inbox.TaskOrdinal)) &&
		(needsPack == (resume.Pack != nil)) && (resume.Pack == nil || validKey(*resume.Pack, thinPackSentinel))
}

func validateContinuationInputs(request extensioneagain.Request, workDir string, session *secrets.Session) error {
	if request.NextInputs == nil {
		return nil
	}
	patch := *request.NextInputs
	if configuration.Contains(patch) {
		return errors.New("extension EAGAIN patch cannot contain configuration references")
	}
	if err := session.CheckOutput(patch); err != nil {
		return err
	}
	raw, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	if strings.Contains(string(raw), workDir) {
		return errors.New("extension EAGAIN patch cannot contain worker paths")
	}
	return nil
}

func validContinuationPath(name string, outbox bool) bool {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\\x00") {
		return false
	}
	return !outbox || (name != "_c2j" && !strings.HasPrefix(name, "_c2j/") && !objects.IsInternalArtifact(name) && name != thinPackSentinel && name != "diff_from_parent.diff" && name != "diff_from_base.diff")
}

func snapshotContinuationFiles(ctx context.Context, root, name string, outbox bool) (jobdb.Artifact, error) {
	file, err := os.CreateTemp("", "c2j-eagain-*.tar")
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(file.Name())
		}
	}()
	writer := tar.NewWriter(file)
	err = filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if filename == root {
			if !entry.IsDir() {
				return errors.New("invalid extension EAGAIN file root")
			}
			return nil
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil || !validContinuationPath(relative, outbox) {
			return errors.New("invalid extension EAGAIN file path")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("extension EAGAIN can only save regular files and directories")
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = relative
		if err := writer.WriteHeader(header); err != nil || info.IsDir() {
			return err
		}
		source, err := os.Open(filename)
		if err != nil {
			return err
		}
		_, err = io.Copy(writer, source)
		return errors.Join(err, source.Close())
	})
	if err := errors.Join(err, writer.Close(), file.Close()); err != nil {
		return nil, err
	}
	artifact, err := jobdb.NewArtifactFromFile(name, file.Name())
	keep = err == nil
	return artifact, err
}

func restoreContinuationFiles(ctx context.Context, artifact jobdb.Artifact, root string) error {
	archive, err := artifact.Open()
	if err != nil {
		return err
	}
	defer archive.Close()
	reader := tar.NewReader(archive)
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if !validContinuationPath(header.Name, artifact.Name() == outboxName) || seen[header.Name] {
			return errors.New("invalid or duplicate extension EAGAIN file path")
		}
		seen[header.Name] = true
		filename := filepath.Join(root, header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(filename, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Join(root, path.Dir(header.Name)), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fs.FileMode(header.Mode)&0o777)
			if err != nil {
				return err
			}
			_, err = io.Copy(file, reader)
			if err := errors.Join(err, file.Close()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("invalid extension EAGAIN archive entry type")
		}
	}
}
