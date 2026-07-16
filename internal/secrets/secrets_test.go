package secrets

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apsdsm/joka/config"
)

type stubFetcher struct {
	values map[string]string
	err    error
	calls  int
}

func (s *stubFetcher) Fetch(_ context.Context, _, _ string) (map[string]string, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.values, nil
}

func TestResolver(t *testing.T) {
	sources := map[string]config.Secret{
		"seed": {SecretID: "lgc/seed/dev1", Region: "ap-northeast-1"},
	}

	t.Run("it resolves a key and caches the fetch per source", func(t *testing.T) {
		f := &stubFetcher{values: map[string]string{"api_key": "s3cret", "other": "x"}}
		r := NewWithFetcher(sources, f)

		for i := 0; i < 3; i++ {
			v, err := r.Resolve(context.Background(), "seed", "api_key")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v != "s3cret" {
				t.Errorf("expected resolved value, got %q", v)
			}
		}
		if _, err := r.Resolve(context.Background(), "seed", "other"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.calls != 1 {
			t.Errorf("expected 1 fetch, got %d", f.calls)
		}
	})

	t.Run("it errors on an unknown source", func(t *testing.T) {
		f := &stubFetcher{values: map[string]string{}}
		r := NewWithFetcher(sources, f)

		_, err := r.Resolve(context.Background(), "nope", "api_key")
		if err == nil || !strings.Contains(err.Error(), `secret source "nope" not configured`) {
			t.Fatalf("expected unknown-source error, got %v", err)
		}
		if f.calls != 0 {
			t.Errorf("expected no fetch, got %d", f.calls)
		}
	})

	t.Run("it errors on a missing key without leaking values", func(t *testing.T) {
		f := &stubFetcher{values: map[string]string{"api_key": "s3cret"}}
		r := NewWithFetcher(sources, f)

		_, err := r.Resolve(context.Background(), "seed", "missing")
		if err == nil || !strings.Contains(err.Error(), `key "missing" not found in secret source "seed"`) {
			t.Fatalf("expected missing-key error, got %v", err)
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("error leaked a secret value: %v", err)
		}
	})

	t.Run("it errors on a source without secret_id", func(t *testing.T) {
		r := NewWithFetcher(map[string]config.Secret{"seed": {Region: "ap-northeast-1"}}, &stubFetcher{})

		_, err := r.Resolve(context.Background(), "seed", "api_key")
		if err == nil || !strings.Contains(err.Error(), `secret source "seed" has no secret_id`) {
			t.Fatalf("expected missing secret_id error, got %v", err)
		}
	})

	t.Run("it wraps fetch failures", func(t *testing.T) {
		f := &stubFetcher{err: errors.New("boom")}
		r := NewWithFetcher(sources, f)

		_, err := r.Resolve(context.Background(), "seed", "api_key")
		if err == nil || !strings.Contains(err.Error(), `fetching secret source "seed"`) {
			t.Fatalf("expected wrapped fetch error, got %v", err)
		}
	})
}
