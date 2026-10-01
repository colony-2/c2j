package testjob

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type suiteResult struct {
	File   string `json:"file"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Output string `json:"output,omitempty"`
}

// discoverSuites does not follow directory symlinks or inspect hidden directories.
func discoverSuites(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("suite directory is not a directory: %s", root)
	}
	var files []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules" || entry.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		for _, suffix := range []string{".test.yaml", ".test.yml", ".test.json", ".scenario.md"} {
			if strings.HasSuffix(entry.Name(), suffix) {
				files = append(files, path)
				break
			}
		}
		return nil
	})
	sort.Strings(files)
	if err == nil && len(files) == 0 {
		err = fmt.Errorf("no test suites found in %s", root)
	}
	return files, err
}

func runDirectory(ctx context.Context, opts Options, execute bool) error {
	opts, err := completeOptions(ctx, opts)
	if err != nil {
		return err
	}
	root, err := absPathFromWorkingDir(opts.WorkingDir, opts.Directory)
	if err != nil {
		return err
	}
	files, err := discoverSuites(root)
	if err != nil {
		return exitError{code: exitCodeCompile, err: err}
	}
	if opts.OutDir == "" {
		opts.OutDir = defaultOutDir()
	}
	outDir, err := absPathFromWorkingDir(opts.WorkingDir, opts.OutDir)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(outDir, 0755); err != nil {
		return err
	}
	results := []suiteResult{}
	failed, selected := 0, 0
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, file)
		result := suiteResult{File: filepath.ToSlash(rel), Status: "passed"}
		child := opts
		child.Directory, child.FilePath = "", file
		child.OutDir = filepath.Join(outDir, "suites", rel)
		child.JSONLEvents = ""
		if opts.JSONLEvents != "" {
			child.JSONLEvents = filepath.Join(child.OutDir, "events.jsonl")
		}
		child.JSONOutput = false
		var output bytes.Buffer
		child.Stdout, child.Stderr = &output, &output
		raw, format, loadErr := loadSuiteBytes(child)
		var suite suiteEnvelope
		var caseCount int
		if loadErr == nil {
			suite, loadErr = parseSuiteEnvelope(raw, format)
		}
		if loadErr == nil {
			cases, parseErr := parseSuiteCases(raw, format)
			loadErr = parseErr
			caseCount = len(filterCasesByIDs(cases, opts.CaseIDs))
			if loadErr == nil && len(cases) == 0 {
				loadErr = fmt.Errorf("suite contains no cases")
			}
		}
		if loadErr == nil && suite.Recipe == "" {
			loadErr = fmt.Errorf("discovered suite must declare recipe")
		}
		switch {
		case loadErr != nil:
			result.Status, result.Error = "failed", loadErr.Error()
		case suite.Live && !opts.IncludeLive:
			result.Status = "excluded_live"
		case caseCount == 0:
			result.Status = "not_selected"
		default:
			selected++
			var runErr error
			if execute {
				runErr = Run(ctx, child)
			} else {
				runErr = Validate(ctx, child)
			}
			if runErr != nil {
				result.Status, result.Error = "failed", runErr.Error()
			}
		}
		result.Output = output.String()
		results = append(results, result)
		if !opts.JSONOutput {
			fmt.Fprintf(opts.Stdout, "suite %s: %s\n%s", result.File, result.Status, result.Output)
			if result.Error != "" {
				fmt.Fprintln(opts.Stdout, result.Error)
			}
		}
		if result.Status == "failed" {
			failed++
			if opts.FailFast || opts.StopOnFailure {
				break
			}
		}
	}
	report := struct {
		Selected int           `json:"selected"`
		Failed   int           `json:"failed"`
		Suites   []suiteResult `json:"suites"`
	}{selected, failed, results}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "summary.json"), data, 0644); err != nil {
		return err
	}
	if opts.JSONOutput {
		if err := json.NewEncoder(opts.Stdout).Encode(report); err != nil {
			return err
		}
	}
	if failed > 0 {
		return exitError{code: exitCodeCases, err: fmt.Errorf("%d suite(s) failed", failed)}
	}
	if selected == 0 {
		return exitError{code: exitCodeCases, err: fmt.Errorf("no test cases selected (live suites require --include-live)")}
	}
	return nil
}
