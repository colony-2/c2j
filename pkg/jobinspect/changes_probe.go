package jobinspect

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// probePrerequisite tests an eligible earlier cumulative bundle without making
// its objects available to the current read until ancestry proves applicability.
// All probes share the operation deadline, download/scan counters and storage
// budget. Only already validated private pack files may be linked or adopted.
func (o *changesObjects) probePrerequisite(h *history, v changesCandidate, candidates []changesCandidate, depth int, hash, format string) (bool, error) {
	root, err := os.MkdirTemp(o.root, "probe-")
	if err != nil {
		return false, changesErr("temporary_storage", "cannot isolate earlier bundle validation")
	}
	ctx, cancel := context.WithCancel(o.ctx)
	config := *o.c
	config.config.ResolveRepository = nil // Exhaust retained evidence before network access.
	p := &changesObjects{c: &config, ctx: ctx, cancel: cancel, root: root, repo: filepath.Join(root, "objects.git"), budgetRoot: o.storageRoot(), repositoryIdentity: o.repositoryIdentity, downloaded: o.downloaded, lineageExamined: o.lineageExamined, loaded: map[string]bool{}, loading: map[string]bool{}, sources: append([]ChangesSource(nil), o.sources...)}
	for k, val := range o.loaded {
		p.loaded[k] = val
	}
	for k, val := range o.loading {
		p.loading[k] = val
	}
	defer func() { o.downloaded = p.downloaded; o.lineageExamined = p.lineageExamined; p.close() }()
	if err = p.init(format); err != nil {
		return false, err
	}
	parentPacks := filepath.Join(o.repo, "objects", "pack")
	probePacks := filepath.Join(p.repo, "objects", "pack")
	files, err := os.ReadDir(parentPacks)
	if err != nil {
		return false, changesErr("temporary_storage", "cannot inspect private object storage")
	}
	for _, file := range files {
		if err = o.ctx.Err(); err != nil {
			return false, ChangesDiagnostic(err)
		}
		if !changesPackFile(file) {
			continue
		}
		if err = os.Link(filepath.Join(parentPacks, file.Name()), filepath.Join(probePacks, file.Name())); err != nil {
			return false, changesErr("temporary_storage", "cannot share validated private objects")
		}
	}
	// An earlier deliberate exact fetch can leave loose objects. Share only
	// Git's hash-named regular object files, never info/alternates or config.
	objectDirs, err := os.ReadDir(filepath.Join(o.repo, "objects"))
	if err != nil {
		return false, changesErr("temporary_storage", "cannot inspect private loose objects")
	}
	for _, dir := range objectDirs {
		if err = o.ctx.Err(); err != nil {
			return false, ChangesDiagnostic(err)
		}
		if !dir.IsDir() || len(dir.Name()) != 2 || strings.Trim(dir.Name(), "0123456789abcdef") != "" {
			continue
		}
		destination := filepath.Join(p.repo, "objects", dir.Name())
		if err = os.MkdirAll(destination, 0700); err != nil {
			return false, changesErr("temporary_storage", "cannot isolate private loose objects")
		}
		objects, err := os.ReadDir(filepath.Join(o.repo, "objects", dir.Name()))
		if err != nil {
			return false, changesErr("temporary_storage", "cannot inspect private loose objects")
		}
		for _, object := range objects {
			if err = o.ctx.Err(); err != nil {
				return false, ChangesDiagnostic(err)
			}
			if !object.Type().IsRegular() || strings.Trim(object.Name(), "0123456789abcdef") != "" {
				continue
			}
			if err = os.Link(filepath.Join(o.repo, "objects", dir.Name(), object.Name()), filepath.Join(destination, object.Name())); err != nil {
				return false, changesErr("temporary_storage", "cannot share validated private loose objects")
			}
		}
	}
	b, err := p.bundle(h, v)
	if err == nil && b.format != format {
		return false, changesErr("invalid_objects", "prerequisite object formats disagree")
	}
	if err == nil {
		err = p.loadBundle(h, b, candidates, depth)
	}
	if err != nil {
		if ChangesDiagnostic(err).Code == "missing_closure" || ChangesDiagnostic(err).Code == "missing_source" {
			return false, nil
		}
		return false, err
	}
	present, err := p.has(hash)
	if err != nil || !present {
		return false, err
	}
	if _, err = p.git("merge-base", "--is-ancestor", hash, v.w.HeadCommit); err != nil {
		if ChangesDiagnostic(err).Code == "invalid_objects" {
			return false, nil
		}
		return false, err
	}
	files, err = os.ReadDir(probePacks)
	if err != nil {
		return false, changesErr("temporary_storage", "cannot inspect validated prerequisite storage")
	}
	for _, file := range files {
		if err = o.ctx.Err(); err != nil {
			return false, ChangesDiagnostic(err)
		}
		if !changesPackFile(file) {
			continue
		}
		target := filepath.Join(parentPacks, file.Name())
		if _, err = os.Stat(target); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return false, changesErr("temporary_storage", "cannot inspect prerequisite destination")
		}
		if err = os.Rename(filepath.Join(probePacks, file.Name()), target); err != nil {
			return false, changesErr("temporary_storage", "cannot adopt validated prerequisite objects")
		}
	}
	o.loaded = p.loaded
	o.sources = p.sources
	if err = o.checkObjects(); err != nil {
		return false, err
	}
	return true, nil
}

func changesPackFile(file os.DirEntry) bool {
	if !file.Type().IsRegular() {
		return false
	}
	switch filepath.Ext(file.Name()) {
	case ".pack", ".idx", ".rev":
		return true
	}
	return false
}
