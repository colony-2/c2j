package defaults

import (
	"context"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

func ExecutionBasePolicy(runtime, tenant string, admin jobdb.ConfigurationAdmin) (executioncatalog.Policy, error) {
	required, err := executioncatalog.RequiredFromEnv()
	if err != nil {
		return executioncatalog.Policy{}, err
	}
	p := executioncatalog.Policy{Required: required, Scope: execution.CatalogScope{Runtime: runtime, Tenant: tenant}}
	if admin != nil {
		p.Read = func(ctx context.Context) (jobdb.ConfigurationSnapshot, error) {
			return admin.GetTenantConfiguration(ctx, tenant)
		}
	}
	return p, nil
}
