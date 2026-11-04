// Package celltest provides fresh cell fixtures and explicit local Git mappings.
package celltest

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"github.com/colony-2/c2j/internal/localruntime"
	"github.com/colony-2/c2j/internal/repository"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

var transports sync.Map

func Context(ctx context.Context) context.Context {
	return repository.WithRepositoryTransport(localruntime.Context(ctx), func(id string) string {
		value, _ := transports.Load(id)
		if value == nil {
			return ""
		}
		return value.(string)
	})
}
func Handler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r.WithContext(Context(r.Context()))) })
}

// Repo explicitly maps a local fixture checkout to a portable registry locator.
func Repo(source string) string {
	if source == "" {
		return "example.test/test"
	}
	if id, err := jobdb.NormalizeCellRepo(source); err == nil {
		return id
	}
	source = strings.TrimPrefix(source, "file://")
	absolute, err := filepath.Abs(source)
	if err != nil {
		panic(err)
	}
	id := fmt.Sprintf("example.test/fixture-%x", sha256.Sum256([]byte(absolute)))
	transports.Store(id, absolute)
	return id
}
