package repository

import (
	"context"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

type repositoryTransportKey struct{}

// WithRepositoryTransport installs a deployment-owned clone transport mapping.
// It must be configured by the embedding, never decoded from recipe/job inputs.
// Durable ownership continues to use the canonical JobDB repository locator.
func WithRepositoryTransport(ctx context.Context, resolve func(string) string) context.Context {
	return context.WithValue(ctx, repositoryTransportKey{}, resolve)
}

func RepositoryTransport(ctx context.Context, repo string) string {
	id, err := jobdb.NormalizeCellRepo(repo)
	if err != nil {
		return repo
	}
	if resolve, ok := ctx.Value(repositoryTransportKey{}).(func(string) string); ok {
		if transport := resolve(id); transport != "" {
			return transport
		}
	}
	return "https://" + id + ".git"
}

func MappedTransport(ctx context.Context, repo string) string {
	id, err := jobdb.NormalizeCellRepo(repo)
	if err != nil {
		return ""
	}
	if resolve, ok := ctx.Value(repositoryTransportKey{}).(func(string) string); ok {
		return resolve(id)
	}
	return ""
}
