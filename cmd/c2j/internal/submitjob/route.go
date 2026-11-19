package submitjob

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/jobcontext"
)

// submissionRoute is resolved once, before any path can prepare or publish a job.
// A discovered broker fixes the route, including when direct configuration exists.
type submissionRoute struct {
	parent    *jobcontext.Parent
	broker    jobcontext.ChildJobBroker
	hasBroker bool
}

func resolveSubmissionRoute(ctx context.Context, o *Options) (*submissionRoute, error) {
	version := strings.TrimSpace(os.Getenv(jobcontext.CurrentContextVersionEnv))
	if version != "" && version != jobcontext.CurrentContextVersion {
		return nil, fmt.Errorf("unsupported current job context version")
	}
	parent, hasParent, err := jobcontext.ParentFromEnv(os.Getenv)
	if err != nil {
		return nil, err
	}
	if version != "" && !hasParent {
		return nil, fmt.Errorf("current job context version requires current job identity")
	}
	broker, hasBroker, err := jobcontext.ChildJobBrokerFromEnv(os.Getenv)
	if err != nil {
		return nil, err
	}
	route := &submissionRoute{broker: broker, hasBroker: hasBroker}
	if hasParent {
		route.parent = &parent
	}
	if hasBroker {
		if !hasParent {
			return nil, fmt.Errorf("child job broker context requires current job context env")
		}
		if err := validateBrokerEndpoint(broker.Endpoint); err != nil {
			return nil, err
		}
	}
	// Optional direct configuration still has the same precedence and validation.
	target, err := defaults.ResolveJobDBTarget(ctx, o.WorkingDir, o.JobDBURI)
	if err != nil {
		return nil, err
	}
	if hasParent && target.TenantID != "" && target.TenantID != parent.TenantID {
		return nil, fmt.Errorf("child job submission must use the current tenant %q; got %q", parent.TenantID, target.TenantID)
	}
	o.JobDBURI, o.SWFURL, o.TenantID = target.URI, target.RuntimeURL, target.TenantID
	if hasBroker {
		o.TenantID = parent.TenantID
	}
	if hasBroker && o.RunAfterSubmit && o.SWFURL == "" {
		return nil, fmt.Errorf("--run requires direct JobDB configuration; broker-only submission cannot execute immediately")
	}
	return route, nil
}

func validateBrokerEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid child job broker endpoint")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") || u.Opaque != "" {
		return fmt.Errorf("child job broker endpoint requires an absolute HTTP(S) URL without credentials, query or fragment")
	}
	if strings.HasSuffix(u.Host, ":") {
		return fmt.Errorf("invalid child job broker endpoint port")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("invalid child job broker endpoint port")
		}
	}
	return nil
}

func (r *submissionRoute) openRuntime(ctx context.Context, runtimeURL string, open func(context.Context, string) (*swfruntime.Handle, error)) (*swfruntime.Handle, error) {
	if r.hasBroker {
		return nil, nil
	}
	return open(ctx, runtimeURL)
}
