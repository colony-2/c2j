package template

import (
	"fmt"
	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/recipe"
)

func (rc *ResolutionContext) SelectGitProfile(spec *recipe.GitProfileSpec, op string) error {
	if spec != nil {
		value := spec.Value
		if text, ok := value.(string); ok {
			if ref, yes := configuration.Template(text); yes {
				value = ref
			} else {
				var err error
				value, err = rc.ResolveValueWithLocals(text, nil)
				if err != nil {
					return fmt.Errorf("git_profile: %w", err)
				}
			}
		}
		if _, ref, err := configuration.Parse(value); err != nil {
			return err
		} else if !ref && value != nil {
			name, ok := value.(string)
			if !ok {
				return fmt.Errorf("git_profile must resolve to a name or null")
			}
			if _, err := gitprofile.Key(name); err != nil {
				return err
			}
		}
		rc.GitProfile = gitprofile.Selection{Explicit: true, Value: value}
	} else if !rc.GitProfile.Explicit && (op == "merge" || op == "squashrebasemerge") {
		rc.GitProfile.Publish = true
	}
	return nil
}
