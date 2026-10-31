package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/colony-2/c2j/pkg/hookpolicy"
)

const hookPolicyNote = "Cell entries replace defaults for this hook. Updates affect new jobs; tenant edits do not change cell overrides or running jobs."

type hookPolicyView struct {
	Effective hookpolicy.Entry `json:"effective"`
	Source    string           `json:"source"`
}

func writeHookPolicies(w io.Writer, operation, tenant, scope, repository, revision string, entries map[string]hookPolicyView, jsonOutput bool) error {
	if jsonOutput {
		return json.NewEncoder(w).Encode(map[string]any{"tenant": tenant, "scope": scope, "repository": repository, "revision": revision, "hooks": entries, "note": hookPolicyNote})
	}
	var out strings.Builder
	switch operation {
	case "add", "remove", "set":
		out.WriteString("Updated hook permissions.\n")
	case "clear":
		out.WriteString("Cleared hook permissions (explicit deny).\n")
	case "inherit":
		out.WriteString("Removed cell override; tenant inheritance restored.\n")
	}
	fmt.Fprintf(&out, "Tenant: %s\nRevision: %s\n", tenant, revision)
	if scope == "cell" {
		fmt.Fprintf(&out, "Cell: %s\n", repository)
	} else {
		out.WriteString("Scope: tenant defaults\n")
	}
	appendHookPolicies(&out, entries)
	fmt.Fprintf(&out, "\n%s\n", hookPolicyNote)
	_, err := io.WriteString(w, out.String())
	return err
}

func writeAllHookPolicies(w io.Writer, tenant, revision string, policies map[string]hookpolicy.Document, jsonOutput bool) error {
	if jsonOutput {
		return json.NewEncoder(w).Encode(map[string]any{"tenant": tenant, "revision": revision, "policies": policies})
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Tenant: %s\nRevision: %s\n", tenant, revision)
	if len(policies) == 0 {
		out.WriteString("No hook policies configured.\n")
	}
	// Sort by repository for people, not by the hashed configuration key.
	documents := make([]hookpolicy.Document, 0, len(policies))
	for _, d := range policies {
		documents = append(documents, d)
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].Repository < documents[j].Repository })
	for _, d := range documents {
		source := "tenant"
		if d.Repository == "" {
			out.WriteString("\nTenant defaults\n")
		} else {
			source = "cell"
			fmt.Fprintf(&out, "\nCell: %s\n", d.Repository)
		}
		entries := make(map[string]hookPolicyView, len(d.Hooks))
		for path, entry := range d.Hooks {
			entries[path] = hookPolicyView{Effective: entry, Source: source}
		}
		appendHookPolicies(&out, entries)
	}
	fmt.Fprintf(&out, "\n%s\n", hookPolicyNote)
	_, err := io.WriteString(w, out.String())
	return err
}

func appendHookPolicies(out *strings.Builder, entries map[string]hookPolicyView) {
	if len(entries) == 0 {
		out.WriteString("No hook entries configured.\n")
		return
	}
	paths := make([]string, 0, len(entries))
	for path := range entries {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		view := entries[path]
		source := map[string]string{"tenant": "tenant default", "cell": "cell override", "none": "none (no grants)"}[view.Source]
		fmt.Fprintf(out, "\nHook: %s\n  Source: %s\n", path, source)
		entry := view.Effective
		var secrets []string
		submit := false
		if entry.Perms.JobDB != nil {
			secrets = append(secrets, entry.Perms.JobDB.Secrets.Resolve...)
			submit = entry.Perms.JobDB.Jobs.Submit
		}
		sort.Strings(secrets)
		if len(secrets) == 0 {
			out.WriteString("  Secret resolution: none\n")
		} else {
			out.WriteString("  Secret resolution:\n")
			for _, name := range secrets {
				fmt.Fprintf(out, "    %s (trust: %s)\n", name, hookTrustLabel(entry.Trust.Secrets[name]))
			}
		}
		if submit && entry.Trust.Submit != nil {
			fmt.Fprintf(out, "  Submit jobs: allowed (trust: %s)\n", hookTrustLabel(*entry.Trust.Submit))
		} else {
			out.WriteString("  Submit jobs: denied\n")
		}
		if entry.Perms.Limit == nil || entry.Perms.Limit.JobDB == nil {
			out.WriteString("  Limits: no additional limit\n")
		} else {
			limit := entry.Perms.Limit.JobDB
			names := append([]string(nil), limit.Secrets.Resolve...)
			sort.Strings(names)
			label := "none"
			if len(names) > 0 {
				label = strings.Join(names, ", ")
			}
			jobs := "denied"
			if limit.Jobs.Submit {
				jobs = "allowed"
			}
			fmt.Fprintf(out, "  Limits:\n    Secret resolution: %s\n    Submit jobs: %s\n", label, jobs)
		}
	}
}

func hookTrustLabel(trust hookpolicy.Trust) string {
	if trust.Mode == "commit" {
		return "candidate commit " + trust.Commit
	}
	return "candidate code"
}
