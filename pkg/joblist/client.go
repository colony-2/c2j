// Package joblist provides supported, read-only c2j job listing without loading
// project configuration or initializing an executor. See README.md for the
// compatibility, pagination, and lifecycle contracts.
package joblist

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/colony-2/c2j/internal/jobdbtarget"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
)

// ErrInvalidInput identifies invalid connection or query configuration.
var ErrInvalidInput = errors.New("invalid listing input")

// Config is explicit and never supplemented with environment or project config.
type Config struct {
	// JobDBURI uses the CLI format: https://host[:port]/<tenant-id>.
	JobDBURI string
	// HTTPClient configures authentication, TLS, proxies, and timeouts. Nil uses
	// a client with a 30-second request timeout and the default HTTP transport.
	// A supplied client remains caller-owned and must be safe for concurrent use.
	HTTPClient *http.Client
}

// Client is immutable and safe for concurrent listing calls. It starts no
// workers or background tasks and requires no Close. Callers own any supplied
// HTTP client's transport and its idle-connection lifecycle.
type Client struct {
	lister   Lister
	tenantID string
}

// New validates explicit connection inputs without making a network request.
// Only remote HTTP(S) targets are supported; embedded runtimes are not opened.
func New(config Config) (*Client, error) {
	target, err := jobdbtarget.Parse(config.JobDBURI)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if target.Embedded || target.RuntimeURL == "" {
		return nil, fmt.Errorf("%w: an HTTP(S) JobDB URI with a tenant is required", ErrInvalidInput)
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	runtime, err := remote.New(target.RuntimeURL, httpClient)
	if err != nil {
		return nil, fmt.Errorf("create listing connection: %w", err)
	}
	return &Client{lister: runtime, tenantID: target.TenantID}, nil
}

// List returns one logical page. An empty page is not necessarily the end:
// continue while NextPageToken is nonempty, keeping the query unchanged.
// The query and its referenced values must not be mutated during a call.
func (c *Client) List(ctx context.Context, query Query) (Page, error) {
	if c == nil || c.lister == nil {
		return Page{}, fmt.Errorf("%w: uninitialized listing client", ErrInvalidInput)
	}
	if ctx == nil {
		return Page{}, fmt.Errorf("%w: context is required", ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	req, err := BuildRequest(c.tenantID, query)
	if err != nil {
		return Page{}, err
	}
	response, err := ListExecutionJobs(ctx, c.lister, req, query.ExecutionFilter)
	if err != nil {
		return Page{}, fmt.Errorf("list jobs: %w", err)
	}
	page := Page{Jobs: make([]Job, 0, len(response.Jobs)), NextPageToken: response.NextPageToken}
	for _, summary := range response.Jobs {
		page.Jobs = append(page.Jobs, JobFromSummary(summary))
	}
	return page, nil
}
