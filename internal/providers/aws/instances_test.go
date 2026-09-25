package aws

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/providers"
)

func fixedLookup(ids []string, err error) (instanceLookup, *map[string]string) {
	seen := map[string]string{}

	return func(_ context.Context, _, tags map[string]string) ([]string, error) {
		for k, v := range tags {
			seen[k] = v
		}
		return ids, err
	}, &seen
}

func TestFindInstance(t *testing.T) {
	ctx := context.Background()

	t.Run("a literal id is used as given, and nothing is looked up", func(t *testing.T) {
		lookup := func(context.Context, map[string]string, map[string]string) ([]string, error) {
			t.Fatal("expected no lookup when an id was given")
			return nil, nil
		}

		got, err := findInstance(ctx, lookup, providers.TunnelSpec{Target: "i-explicit"})
		if err != nil || got != "i-explicit" {
			t.Errorf("expected i-explicit, got %q (%v)", got, err)
		}
	})

	t.Run("a tag selector resolves to an instance", func(t *testing.T) {
		// The relays are in autoscaling groups, so the id changes on every
		// instance refresh and a committed one goes stale.
		lookup, seen := fixedLookup([]string{"i-abc"}, nil)

		got, err := findInstance(ctx, lookup, providers.TunnelSpec{
			TargetTags: map[string]string{"Role": "relay"},
		})
		if err != nil {
			t.Fatalf("findInstance: %v", err)
		}
		if got != "i-abc" {
			t.Errorf("expected i-abc, got %q", got)
		}
		if (*seen)["Role"] != "relay" {
			t.Errorf("expected the tag to reach the lookup, got %v", *seen)
		}
	})

	t.Run("several matches resolve the same way every run", func(t *testing.T) {
		// The relays are interchangeable, so any would do — but a run that
		// picks the same one twice is one whose logs can be read together.
		lookup, _ := fixedLookup([]string{"i-ccc", "i-aaa", "i-bbb"}, nil)

		for i := 0; i < 3; i++ {
			got, err := findInstance(ctx, lookup, providers.TunnelSpec{
				TargetTags: map[string]string{"Role": "relay"},
			})
			if err != nil {
				t.Fatalf("findInstance: %v", err)
			}
			if got != "i-aaa" {
				t.Errorf("expected the same instance each time, got %q", got)
			}
		}
	})

	t.Run("no match names the tag and where joka looked", func(t *testing.T) {
		// A tag that exists but finds nothing is usually the wrong profile or
		// the wrong region, so saying which were used is most of the answer.
		lookup, _ := fixedLookup(nil, nil)

		_, err := findInstance(ctx, lookup, providers.TunnelSpec{
			TargetTags: map[string]string{"Role": "relay"},
			Params:     map[string]string{ParamRegion: "ap-northeast-1", ParamProfile: "ONC_TEST"},
		})
		if !errors.Is(err, ErrNoSuchInstance) {
			t.Fatalf("expected ErrNoSuchInstance, got: %v", err)
		}
		for _, want := range []string{"Role=relay", "ap-northeast-1", "ONC_TEST"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("expected the error to mention %q, got: %v", want, err)
			}
		}
	})

	t.Run("naming no target at all is refused", func(t *testing.T) {
		lookup, _ := fixedLookup(nil, nil)

		if _, err := findInstance(ctx, lookup, providers.TunnelSpec{}); err == nil {
			t.Fatal("expected a tunnel with no target to be refused")
		}
	})

	t.Run("a lookup failure is reported, not treated as no match", func(t *testing.T) {
		lookup, _ := fixedLookup(nil, errors.New("AccessDenied"))

		_, err := findInstance(ctx, lookup, providers.TunnelSpec{
			TargetTags: map[string]string{"Role": "relay"},
		})
		if err == nil || !strings.Contains(err.Error(), "AccessDenied") {
			t.Fatalf("expected the API failure to surface, got: %v", err)
		}
		if errors.Is(err, ErrNoSuchInstance) {
			t.Error("a permissions failure is not an empty result")
		}
	})
}

func TestSessionManagerResolvesTheTarget(t *testing.T) {
	// End to end through Open: the resolved id must reach the CLI invocation.
	l := listenOn(t, 0)
	port := l.Addr().(*net.TCPAddr).Port

	var args []string
	lookup, _ := fixedLookup([]string{"i-resolved"}, nil)

	s := SessionManager{
		lookPath: present,
		lookup:   lookup,
		run: func(ctx context.Context, name string, a ...string) *exec.Cmd {
			args = a
			return exec.CommandContext(ctx, "sleep", "30")
		},
	}

	sess, err := s.Open(context.Background(), providers.TunnelSpec{
		TargetTags: map[string]string{"Role": "relay"},
		RemoteHost: "db.internal", RemotePort: 5432, LocalPort: port,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close()

	if !strings.Contains(strings.Join(args, " "), "--target i-resolved") {
		t.Errorf("expected the resolved id in the invocation, got: %v", args)
	}
}
