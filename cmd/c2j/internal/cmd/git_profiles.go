package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobconfiguration "github.com/colony-2/jobdb/pkg/jobdb/configuration"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "Read configuration in the current execution"}
	c.AddCommand(newExecutionGet("config"))
	return c
}
func newSecretCmd() *cobra.Command {
	c := &cobra.Command{Use: "secret", Short: "Read authorized execution secrets"}
	c.AddCommand(newExecutionGet("secret"))
	c.AddCommand(&cobra.Command{Use: "git-credential get|store|erase", Short: "Git credential protocol adapter", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error { return runCredentialHelper(cmd, args[0]) }})
	return c
}
func newExecutionGet(kind string) *cobra.Command {
	var format string
	c := &cobra.Command{Use: "get NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if format != "json" && format != "raw" {
			return errors.New("format must be json or raw")
		}
		res, err := gitprofile.Call(cmd.Context(), os.Getenv(gitprofile.SocketEnv), gitprofile.Request{Operation: kind, Name: args[0]})
		if err != nil {
			return err
		}
		if kind == "secret" {
			if res.Secret == nil {
				return errors.New("secret resolution returned no value")
			}
			if format == "raw" {
				_, err = io.WriteString(cmd.OutOrStdout(), res.Secret.Value)
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(res.Secret)
		}
		if format == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(res.Value)
		}
		var value string
		if json.Unmarshal(res.Value, &value) != nil || string(res.Value) == "null" {
			return errors.New("raw configuration output requires a string")
		}
		_, err = io.WriteString(cmd.OutOrStdout(), value)
		return err
	}}
	c.Flags().StringVar(&format, "format", "json", "Output format: json or raw")
	return c
}
func runCredentialHelper(cmd *cobra.Command, operation string) error {
	if operation != "get" {
		return nil
	} // Stateless helpers ignore store/erase and future operations.
	fail := func(err error) error { _, _ = io.WriteString(cmd.OutOrStdout(), "quit=true\n\n"); return err }
	attrs := map[string]string{}
	scanner := bufio.NewScanner(io.LimitReader(cmd.InOrStdin(), 65537))
	scanner.Buffer(make([]byte, 4096), 65536)
	total := 0
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line) + 1
		if total > 65536 {
			return fail(errors.New("credential request too large"))
		}
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !gitprofile.ProtocolValue(key) || !gitprofile.ProtocolValue(value) {
			return fail(errors.New("invalid credential request"))
		}
		switch key {
		case "protocol", "host", "path", "username":
			if _, exists := attrs[key]; exists {
				return fail(errors.New("duplicate credential attribute"))
			}
			attrs[key] = value
		}
	}
	if scanner.Err() != nil {
		return fail(errors.New("invalid credential request"))
	}
	res, err := gitprofile.Call(cmd.Context(), os.Getenv(gitprofile.SocketEnv), gitprofile.Request{Operation: "credential", Protocol: attrs["protocol"], Host: attrs["host"], Path: attrs["path"], Username: attrs["username"]})
	if err != nil {
		return fail(err)
	}
	if res.Secret == nil || res.Secret.Value == "" || !utf8.ValidString(res.Secret.Value) || !gitprofile.ProtocolValue(res.Secret.Value) || !gitprofile.ProtocolValue(res.Username) {
		return fail(errors.New("invalid credential response"))
	}
	if res.Secret.ExpiresAt != nil && !res.Secret.ExpiresAt.After(time.Now()) {
		return fail(errors.New("credential expired"))
	}
	var out strings.Builder
	fmt.Fprintf(&out, "username=%s\npassword=%s\n", res.Username, res.Secret.Value)
	if res.Secret.ExpiresAt != nil {
		fmt.Fprintf(&out, "password_expiry_utc=%d\n", res.Secret.ExpiresAt.Unix())
	}
	out.WriteByte('\n')
	_, err = io.WriteString(cmd.OutOrStdout(), out.String())
	return err
}

type profileAdmin struct {
	config       jobdb.ConfigurationAdmin
	client       *http.Client
	base, tenant string
}
type profileAuthTransport struct{ token string }

func (t profileAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c := r.Clone(r.Context())
	if t.token != "" {
		c.Header.Set("Authorization", "Bearer "+t.token)
	}
	return http.DefaultTransport.RoundTrip(c)
}
func openProfileAdmin(base, tenant string) (*profileAdmin, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("--url must be an HTTP(S) JobDB server URL without credentials, query, or fragment")
	}
	if tenant == "" || !utf8.ValidString(tenant) {
		return nil, errors.New("--tenant is required")
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: profileAuthTransport{os.Getenv("JOBDB_BEARER_TOKEN")}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := remote.New(strings.TrimRight(base, "/"), client)
	if err != nil {
		return nil, errors.New("cannot initialize configuration client")
	}
	return &profileAdmin{r, client, strings.TrimRight(base, "/"), tenant}, nil
}
func (a *profileAdmin) secret(ctx context.Context, op, name, value string) error {
	body := map[string]string{"name": name}
	if op == "set" {
		body["value"] = value
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/dev/v1/tenants/"+url.PathEscape(a.tenant)+"/secrets/"+op, bytes.NewReader(raw))
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
func newGitCmd() *cobra.Command {
	var base, tenant string
	c := &cobra.Command{Use: "git", Short: "Manage and activate Git profiles"}
	c.PersistentFlags().StringVar(&base, "url", os.Getenv("C2J_JOBDB"), "JobDB HTTP(S) server URL for management commands")
	c.PersistentFlags().StringVar(&tenant, "tenant", os.Getenv("C2J_TENANT_ID"), "Tenant for management commands")
	for _, op := range []string{"activate", "clear"} {
		args := cobra.ExactArgs(1)
		use := op + " NAME"
		if op == "clear" {
			args = cobra.NoArgs
			use = op
		}
		c.AddCommand(&cobra.Command{Use: use, Args: args, RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			_, err := gitprofile.Call(cmd.Context(), os.Getenv(gitprofile.SocketEnv), gitprofile.Request{Operation: op, Name: name})
			return err
		}})
	}
	for _, op := range []string{"set", "get", "list", "remove"} {
		var file string
		args := cobra.ExactArgs(1)
		use := op + " NAME"
		if op == "list" {
			args = cobra.NoArgs
			use = op
		}
		sub := &cobra.Command{Use: use, Args: args, RunE: func(cmd *cobra.Command, args []string) error {
			// Execution subprocesses must use the scoped read/control interface,
			// never discover management credentials from their environment.
			if os.Getenv(gitprofile.SocketEnv) != "" {
				return errors.New("Git profile management requires a provisioning context")
			}
			var key string
			var err error
			if len(args) > 0 {
				key, err = gitprofile.Key(args[0])
				if err != nil {
					return err
				}
			}
			var p gitprofile.Profile
			values := map[string]string{}
			if op == "set" {
				if file == "" {
					return errors.New("--file is required")
				}
				raw, err := readProfileFile(file, cmd.InOrStdin(), 64<<10)
				if err != nil {
					return err
				}
				p, err = gitprofile.Decode(raw, true)
				if err != nil {
					return err
				}
				stdinUsed := file == "-"
				for i := range p.Entries {
					entry := &p.Entries[i]
					if entry.SecretFile == "" {
						continue
					}
					path := entry.SecretFile
					if path == "-" {
						if stdinUsed {
							return errors.New("stdin may supply only one profile or credential")
						}
						stdinUsed = true
					} else if !filepath.IsAbs(path) && file != "-" {
						path = filepath.Join(filepath.Dir(file), path)
					}
					value, err := readProfileFile(path, cmd.InOrStdin(), 64<<10)
					if err != nil {
						return err
					}
					if !utf8.Valid(value) || !gitprofile.ProtocolValue(string(value)) || len(value) == 0 {
						return errors.New("Git credential must be nonempty UTF-8 without newline or NUL")
					}
					entry.SecretRef = gitprofile.SecretPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
					entry.SecretFile = ""
					values[entry.SecretRef] = string(value)
				}
				if err = p.Validate(false); err != nil {
					return err
				}
			}
			a, err := openProfileAdmin(base, tenant)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			snapshot, err := a.config.GetTenantConfiguration(ctx, tenant)
			if err != nil {
				return errors.New("configuration administration read failed")
			}
			switch op {
			case "get":
				p, err = gitprofile.Load(snapshot, args[0])
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(p)
			case "list":
				names := []string{}
				for key := range snapshot.Values {
					if strings.HasPrefix(key, gitprofile.ProfilePrefix) {
						names = append(names, strings.TrimPrefix(key, gitprofile.ProfilePrefix))
					}
				}
				sort.Strings(names)
				return json.NewEncoder(cmd.OutOrStdout()).Encode(names)
			case "remove":
				_, err = a.config.RemoveTenantConfigurationValue(ctx, jobdb.RemoveTenantConfigurationValueRequest{TenantID: tenant, Name: key, ExpectedRevision: &snapshot.Revision})
			case "set":
				raw, _ := json.Marshal(p)
				if err = jobconfiguration.ValidateValue(raw, jobdb.ConfigurationLimits{}); err != nil {
					return errors.New("encoded Git profile exceeds configuration limits")
				}
				created := []string{}
				cleanup := func() {
					cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					for _, name := range created {
						if e := a.secret(cleanupCtx, "remove", name, ""); e != nil {
							fmt.Fprintln(cmd.ErrOrStderr(), "managed secret cleanup failed:", name)
						}
					}
				}
				for name, value := range values {
					created = append(created, name)
					if err = a.secret(ctx, "set", name, value); err != nil {
						cleanup()
						return err
					}
				}
				_, err = a.config.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: tenant, Name: key, Value: raw, ExpectedRevision: &snapshot.Revision})
				// On uncertain publication keep credentials: the server may have
				// committed the profile even if its response was lost.
				if errors.Is(err, jobdb.ErrConfigurationRevisionMismatch) {
					cleanup()
				}
			}
			if err != nil {
				return errors.New("profile update failed; reread the profile before retrying")
			}
			return nil
		}}
		if op == "set" {
			sub.Flags().StringVar(&file, "file", "", "Profile YAML/JSON file, or - for stdin")
		}
		c.AddCommand(sub)
	}
	return c
}
func readProfileFile(path string, stdin io.Reader, limit int64) ([]byte, error) {
	r := stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, errors.New("cannot open profile or credential file")
		}
		defer f.Close()
		r = f
	}
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("profile or credential file unreadable or too large")
	}
	return raw, nil
}
