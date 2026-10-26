package swfruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
)

// SecretAdmin is JobDB's development secret-store administration capability.
// Remote clients currently expose this through the HTTP API only.
type SecretAdmin interface {
	SetDevSecret(context.Context, string, string, string) error
	RemoveDevSecret(context.Context, string, string) error
}

type bearerTransport struct{ token string }

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c := r.Clone(r.Context())
	if t.token != "" {
		c.Header.Set("Authorization", "Bearer "+t.token)
	}
	return http.DefaultTransport.RoundTrip(c)
}
func newHTTPClient() *http.Client {
	return &http.Client{Timeout: defaultHTTPTimeout, Transport: bearerTransport{os.Getenv("JOBDB_BEARER_TOKEN")}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type remoteSecretAdmin struct {
	client *http.Client
	base   string
}

func (a *remoteSecretAdmin) SetDevSecret(ctx context.Context, tenant, name, value string) error {
	return a.secret(ctx, tenant, "set", name, value)
}
func (a *remoteSecretAdmin) RemoveDevSecret(ctx context.Context, tenant, name string) error {
	return a.secret(ctx, tenant, "remove", name, "")
}
func (a *remoteSecretAdmin) secret(ctx context.Context, tenant, op, name, value string) error {
	body := map[string]string{"name": name}
	if op == "set" {
		body["value"] = value
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/dev/v1/tenants/"+url.PathEscape(tenant)+"/secrets/"+op, bytes.NewReader(raw))
	if err != nil {
		return errors.New("invalid secret administration request")
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(req)
	if err != nil {
		return errors.New("secret administration unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("supplied-secret storage failed (HTTP %d); this server must support JobDB's secret administration API, otherwise use secret_ref", response.StatusCode)
	}
	return nil
}
