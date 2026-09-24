package providers_test

import (
	"context"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/providers"
	// Registers the AWS providers, which is the whole mechanism: adding a
	// vendor is adding a package and importing it.
	_ "github.com/apsdsm/joka/internal/providers/aws"
)

func TestRegistry(t *testing.T) {
	t.Run("importing a provider package registers it", func(t *testing.T) {
		if _, err := providers.LookupSecrets("aws"); err != nil {
			t.Errorf("expected aws secrets to be registered, got: %v", err)
		}
		if _, err := providers.LookupTunnel("aws"); err != nil {
			t.Errorf("expected the aws tunnel to be registered, got: %v", err)
		}
	})

	t.Run("an unknown provider names the ones there are", func(t *testing.T) {
		// The likely cause is a typo or a vendor joka does not ship, and
		// either way the useful reply is the list.
		_, err := providers.LookupSecrets("gcp")
		if err == nil {
			t.Fatal("expected an unknown provider to be refused")
		}
		if !strings.Contains(err.Error(), "aws") {
			t.Errorf("expected the available providers to be listed, got: %v", err)
		}
	})

	t.Run("a registered provider can be replaced, which is how a test stubs one", func(t *testing.T) {
		providers.RegisterSecrets("fake", stubSecrets{})

		s, err := providers.LookupSecrets("fake")
		if err != nil {
			t.Fatalf("LookupSecrets: %v", err)
		}
		got, err := s.Fetch(context.Background(), providers.SecretRef{ID: "x"})
		if err != nil || got["k"] != "v" {
			t.Errorf("expected the stub to answer, got %v %v", got, err)
		}
	})
}

type stubSecrets struct{}

func (stubSecrets) Fetch(context.Context, providers.SecretRef) (map[string]string, error) {
	return map[string]string{"k": "v"}, nil
}
