// A disposable real PostgreSQL fixture for the c2j consumer acceptance tests.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	pg "github.com/colony-2/pgjobdb/pkg/pgjobdb/runtime"
	ep "github.com/fergusstrange/embedded-postgres"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	receipts := flag.String("recipejob-test", "", "path to compiled pkg/recipejob tests")
	waits := flag.String("ops-test", "", "path to compiled pkg/ops/recipe tests")
	cache := flag.String("cache", filepath.Join(os.TempDir(), "c2j-pg-acceptance-cache"), "PostgreSQL download cache")
	rawRounds := flag.Int("raw-receipts", 0, "also run this many direct JobDB ordinary receipt races without c2j")
	flag.Parse()
	if (*receipts == "") != (*waits == "") || (*receipts == "" && *rawRounds <= 0) {
		return fmt.Errorf("both compiled test binaries, or positive -raw-receipts, are required")
	}
	dir, err := os.MkdirTemp("", "exactjob-pg-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := uint32(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		return err
	}
	db := ep.NewDatabase(ep.DefaultConfig().Port(port).RuntimePath(filepath.Join(dir, "runtime")).DataPath(filepath.Join(dir, "data")).CachePath(*cache))
	if err := db.Start(); err != nil {
		return err
	}
	defer db.Stop()
	runtime, err := pg.NewFromConfig(pg.Config{
		PostgresDSN:  fmt.Sprintf("postgres://postgres:postgres@localhost:%d/postgres?sslmode=disable", port),
		BlobStoreURI: "blobfs://" + filepath.Join(dir, "blobs"),
	})
	if err != nil {
		return err
	}
	defer runtime.Close(context.Background())
	fmt.Println("PostgreSQL fixture: pgjobdb v0.0.27 (409a5ef1f19323f86dfb8ab98f20904fc7faf5d4); JobDB f885723cc03c")
	handler := remote.NewConsumerServer(runtime)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fixture authority only; never used by application code or a real server.
		ctx := jobdb.WithPrimaryGoalSelectionAuthorizer(celltest.Context(r.Context()), func(context.Context, jobdb.GoalKey, jobdb.ResolvedCell) error { return nil })
		handler.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer server.Close()
	failed := false
	if *rawRounds > 0 {
		client, err := remote.New(server.URL, server.Client())
		if err != nil {
			return err
		}
		if err := rawReceipts(client, *rawRounds); err != nil {
			fmt.Println(err)
			failed = true
		}
	}
	for _, fixture := range []struct{ binary, pattern string }{
		{*receipts, "^TestExactJob(ConsumerReceiptsAndRestart|LegacyFrozenRecovery)/postgres$"},
		{*waits, "^TestExactJobBackendIsolation/postgres/"},
	} {
		if fixture.binary == "" {
			continue
		}
		cmd := exec.Command(fixture.binary, "-test.v", "-test.count=1", "-test.timeout=5m", "-test.run="+fixture.pattern)
		cmd.Env = append(os.Environ(), "C2J_EXACT_JOB_TEST_URL="+server.URL)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Println("Fixture failed:", err)
			failed = true
		}
	}
	if failed {
		return fmt.Errorf("PostgreSQL acceptance failed; see individual fixtures")
	}
	return nil
}

// This minimal reproduction has no c2j schema/descriptor adapter or imports.
// Transient uncertainty is allowed; the unchanged original must reconcile.
func rawReceipts(runtime *remote.Runtime, rounds int) error {
	ctx := context.Background()
	failed := false
	for round := 0; round < rounds; round++ {
		tenant := fmt.Sprintf("raw-%d-%d", time.Now().UnixNano(), round)
		if _, err := runtime.CreateCell(ctx, jobdb.CreateCellRequest{TenantID: tenant, Name: "app", Repo: "example.test/app"}); err != nil {
			return err
		}
		request := jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: tenant, JobID: "destination", Cell: "app", CellRepo: "example.test/app", JobType: "work", Data: jobdb.NewTaskDataOrPanic(map[string]any{"n": "unchanged"})}}
		var handles [4]jobdb.JobHandle
		var results [4]error
		var group sync.WaitGroup
		for i := range handles {
			group.Add(1)
			go func(i int) { defer group.Done(); handles[i], results[i] = runtime.SubmitJob(ctx, request) }(i)
		}
		group.Wait()
		var accepted *jobdb.JobHandle
		for i := range handles {
			if results[i] != nil {
				fmt.Printf("raw round %d caller %d uncertain: %v\n", round, i, results[i])
				handles[i], results[i] = runtime.SubmitJob(ctx, request)
			}
			if results[i] != nil {
				fmt.Printf("raw round %d caller %d unchanged replay failed: %v\n", round, i, results[i])
				failed = true
			} else if accepted == nil {
				accepted = &handles[i]
			} else if handles[i] != *accepted {
				fmt.Printf("raw round %d caller %d returned different identity\n", round, i)
				failed = true
			}
		}
	}
	if failed {
		return fmt.Errorf("direct JobDB ordinary receipt reconciliation failed")
	}
	fmt.Printf("direct JobDB ordinary receipt reconciliation passed %d rounds\n", rounds)
	return nil
}
