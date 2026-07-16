// Package secrets resolves named secret sources from the `secrets:` map in
// .jokarc.yaml. Each source is fetched at most once per run and cached, since
// a seed run typically references the same few secrets many times.
package secrets

import (
	"context"
	"fmt"

	"github.com/apsdsm/joka/config"
	"github.com/apsdsm/joka/internal/connection"
)

// Resolver resolves (source, key) pairs against configured secret sources.
// Error messages name sources and keys but never secret values.
type Resolver struct {
	fetcher connection.SecretFetcher
	sources map[string]config.Secret
	cache   map[string]map[string]string
}

// New builds a Resolver over the configured sources using the default AWS
// Secrets Manager fetcher (created lazily on first fetch, so constructing a
// Resolver never touches AWS).
func New(sources map[string]config.Secret) *Resolver {
	return NewWithFetcher(sources, nil)
}

// NewWithFetcher is New with an explicit fetcher, for tests.
func NewWithFetcher(sources map[string]config.Secret, fetcher connection.SecretFetcher) *Resolver {
	return &Resolver{
		fetcher: fetcher,
		sources: sources,
		cache:   make(map[string]map[string]string),
	}
}

// Resolve returns the value of JSON key `key` in the secret configured under
// source name `source`.
func (r *Resolver) Resolve(ctx context.Context, source, key string) (string, error) {
	values, ok := r.cache[source]
	if !ok {
		sec, ok := r.sources[source]
		if !ok {
			return "", fmt.Errorf("secret source %q not configured", source)
		}
		if sec.SecretID == "" {
			return "", fmt.Errorf("secret source %q has no secret_id", source)
		}

		if r.fetcher == nil {
			r.fetcher = connection.NewAWSSecretsManager()
		}

		fetched, err := r.fetcher.Fetch(ctx, sec.SecretID, sec.Region)
		if err != nil {
			return "", fmt.Errorf("fetching secret source %q: %w", source, err)
		}

		values = fetched
		r.cache[source] = values
	}

	v, ok := values[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret source %q", key, source)
	}

	return v, nil
}
