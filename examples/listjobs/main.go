// listjobs demonstrates the supported public listing API. It can be copied into
// a separate Go module; no c2j CLI or executor is involved.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

func main() {
	if err := run(); err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			fmt.Fprintln(os.Stderr, "listing canceled")
		case errors.Is(err, context.DeadlineExceeded):
			fmt.Fprintln(os.Stderr, "listing deadline exceeded")
		case errors.Is(err, joblist.ErrInvalidInput):
			fmt.Fprintln(os.Stderr, "check listing configuration:", err)
		default:
			fmt.Fprintln(os.Stderr, "listing failed:", err)
		}
		os.Exit(1)
	}
}

func run() error {
	uri := flag.String("jobdb", "", "JobDB URI: https://host/tenant")
	repo := flag.String("repo", "", "repository URL or host/owner/repository")
	pageSize := flag.Int("page-size", 50, "maximum jobs returned per page")
	pageToken := flag.String("page-token", "", "opaque token from a previous page")
	all := flag.Bool("all", false, "explicitly iterate every page (one JSON object per page)")
	timeout := flag.Duration("timeout", 30*time.Second, "overall listing deadline")
	flag.Parse()

	client, err := joblist.New(joblist.Config{JobDBURI: *uri})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	query := joblist.Query{
		Repository: *repo, JobTypes: []string{"recipe"},
		Statuses: []jobdb.JobStatus{jobdb.JobStatusReady, jobdb.JobStatusCrashConcern},
		PageSize: *pageSize, PageToken: *pageToken,
	}
	for {
		page, err := client.List(ctx, query)
		if err != nil {
			return err
		}
		for _, job := range page.Jobs {
			view := job.Execution
			if view.Diagnostic != "" {
				fmt.Fprintf(os.Stderr, "job %s: %s\n", job.JobID, view.Diagnostic)
				continue // Never treat an invalid or unsupported demand as empty.
			}
			if view.Status == "specified" && view.Demand != nil {
				// A scheduler can inspect view.Demand.Effective here. This example
				// only reports the snapshot; it never provisions or claims jobs.
				fmt.Fprintf(os.Stderr, "job %s: execution revision %d\n", job.JobID, view.Demand.Revision)
			}
		}
		if err := json.NewEncoder(os.Stdout).Encode(page); err != nil {
			return err
		}
		if !*all || page.NextPageToken == "" {
			return nil
		}
		if page.NextPageToken == query.PageToken {
			return fmt.Errorf("listing cursor did not advance")
		}
		query.PageToken = page.NextPageToken
	}
}
