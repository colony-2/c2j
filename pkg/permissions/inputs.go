package permissions

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// InputPolicy authorizes driver materialization, never delegated lease authority.
// RootRepository is fixed for the entire recipe, including nested includes.
type InputPolicy struct {
	RootRepository string   `json:"root_repository,omitempty"`
	Foreign        bool     `json:"foreign,omitempty"`
	Allowed        []string `json:"allowed,omitempty"`
}

func (s State) CheckInput(name string) error {
	if name == "" {
		return nil
	}
	if s.Limit != nil && !slices.Contains(s.Limit.Secrets.Resolve, name) {
		return fmt.Errorf("input secret %q exceeds the enclosing limit: %w", name, jobdb.ErrLeasePermissionDenied)
	}
	if s.Inputs != nil && (!s.Inputs.Foreign || slices.Contains(s.Inputs.Allowed, name)) {
		return nil
	}
	if s.Grant != nil && slices.Contains(s.Grant.Secrets.Resolve, name) {
		return nil
	}
	return fmt.Errorf("input secret %q: %w", name, jobdb.ErrLeasePermissionDenied)
}

// EnterInclude interprets the callsite grant as authority entrusted to the
// included recipe. It does not put those secrets on descendant lease tokens.
func (s State) EnterInclude(spec *Spec, boundary, trusted bool) (State, error) {
	if err := ValidateInclude(spec, boundary); err != nil {
		return State{}, err
	}
	input := InputPolicy{Foreign: !trusted}
	if s.Inputs != nil {
		input.RootRepository = s.Inputs.RootRepository
		if !boundary {
			input.Allowed = slices.Clone(s.Inputs.Allowed)
		}
	}
	if spec != nil && spec.JobDB != nil {
		for _, name := range spec.JobDB.Secrets.Resolve {
			if err := s.CheckInput(name); err != nil {
				return State{}, err
			}
		}
		input.Allowed = append(input.Allowed, spec.JobDB.Secrets.Resolve...)
	}
	if boundary || (spec != nil && spec.JobDB != nil) {
		s.Grant = nil
	}
	// Callsite authority is also a ceiling: foreign descendants cannot enlarge it.
	if spec != nil {
		ceilings := []*JobDB{spec.JobDB}
		if spec.Limit != nil {
			ceilings = append(ceilings, spec.Limit.JobDB)
		}
		for _, ceiling := range ceilings {
			if ceiling == nil {
				continue
			}
			v := clone(*ceiling)
			if s.Limit != nil {
				v = intersect(v, *s.Limit)
			}
			s.Limit = &v
		}
	}
	s.Inputs = &input
	return s.Enter(nil, false)
}

func ValidateInclude(spec *Spec, boundary bool) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	if boundary && (spec == nil || (spec.JobDB == nil && (spec.Limit == nil || spec.Limit.JobDB == nil))) {
		return fmt.Errorf("cross-repository include requires perms.limit.jobdb or perms.jobdb")
	}
	return nil
}

// RepositoryIdentity is computed only while loading sources, never by replay.
// Unknown origins cannot establish trust. Local paths follow symlinks to the
// actual checkout; URL comparison deliberately does not alias SSH with HTTPS.
func RepositoryIdentity(source string) string {
	if strings.HasPrefix(source, "git+") {
		source = strings.TrimPrefix(source, "git+")
		if scheme := strings.Index(source, "://"); scheme >= 0 {
			if split := strings.Index(source[scheme+3:], "//"); split >= 0 {
				source = source[:scheme+3+split]
			}
		}
	}
	u, err := url.Parse(source)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	if u.Scheme != "" && u.Scheme != "file" {
		if u.Host == "" {
			return ""
		}
		return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git")
	}
	path := source
	if u.Scheme == "file" {
		path = u.Path
	}
	if path == "" {
		return ""
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	info, err := os.Stat(real)
	if err != nil {
		return ""
	}
	if !info.IsDir() {
		real = filepath.Dir(real)
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return ""
	}
	for dir := real; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return "file://" + dir
		}
		if filepath.Dir(dir) == dir {
			return ""
		}
	}
}
