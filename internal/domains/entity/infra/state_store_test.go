package infra_test

import (
	"context"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
	"github.com/apsdsm/joka/internal/domains/entity/infra"
	"github.com/apsdsm/joka/internal/providers"
)

type memStore struct {
	body  []byte
	found bool
}

func (m *memStore) Read(context.Context, providers.StateRef) ([]byte, bool, error) {
	return m.body, m.found, nil
}

func (m *memStore) Write(_ context.Context, _ providers.StateRef, body []byte) error {
	m.body, m.found = body, true
	return nil
}

func TestNewStateStoreWithoutRemoteIsTheFile(t *testing.T) {
	store, err := infra.NewStateStore("", "dev1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Describe(); got != "joka.dev1.state.json" {
		t.Fatalf("Describe = %q", got)
	}
}

func TestNewStateStoreRefusesAPathAndARemote(t *testing.T) {
	providers.RegisterState("mem", &memStore{})

	_, err := infra.NewStateStore("local.json", "", &infra.RemoteState{Backend: "mem", Container: "b"})
	if err == nil || !strings.Contains(err.Error(), "use one") {
		t.Fatalf("err = %v, want a refusal naming both", err)
	}
}

func TestNewStateStoreNeedsABucket(t *testing.T) {
	providers.RegisterState("mem", &memStore{})

	if _, err := infra.NewStateStore("", "", &infra.RemoteState{Backend: "mem"}); err == nil {
		t.Fatal("a remote state with no bucket was accepted")
	}
}

func TestNewStateStoreUnknownBackend(t *testing.T) {
	_, err := infra.NewStateStore("", "", &infra.RemoteState{Backend: "nope", Container: "b"})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v, want one naming the backend", err)
	}
}

func TestRemoteStoreDefaultsTheKeyFromTheProfile(t *testing.T) {
	providers.RegisterState("mem", &memStore{})

	store, err := infra.NewStateStore("", "prod", &infra.RemoteState{Backend: "mem", Container: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Describe(); got != "mem://b/joka.prod.state.json" {
		t.Fatalf("Describe = %q", got)
	}
}

func TestRemoteStoreRoundTrip(t *testing.T) {
	mem := &memStore{}
	providers.RegisterState("mem", mem)
	ctx := context.Background()

	store, err := infra.NewStateStore("", "", &infra.RemoteState{Backend: "mem", Container: "b", Key: "k.json"})
	if err != nil {
		t.Fatal(err)
	}

	if _, found, err := store.Read(ctx); err != nil || found {
		t.Fatalf("before any write: found=%v err=%v", found, err)
	}

	want := infra.StateDocument{Identity: "abc", Version: 4, State: domain.NewState()}
	if err := store.Write(ctx, want); err != nil {
		t.Fatal(err)
	}

	got, found, err := store.Read(ctx)
	if err != nil || !found {
		t.Fatalf("after write: found=%v err=%v", found, err)
	}
	if got.Identity != "abc" || got.Version != 4 {
		t.Fatalf("got %+v", got)
	}
}
