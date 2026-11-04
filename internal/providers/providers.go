// Package providers is joka's boundary against the outside world: the vendor
// services it can fetch a secret from and tunnel a connection through.
//
// Every provider implements the same two interfaces and registers itself under
// a name, so adding one is adding a directory rather than editing a switch in
// the connection code. joka ships AWS; the shape is the same for anything else.
//
// The interfaces are deliberately vendor-neutral. The first version of this
// had `Fetch(ctx, secretID, region)` — a signature that only means anything to
// AWS, and that a second provider would have had to widen or ignore. Anything
// a particular provider needs goes in Params, which it reads and nothing else
// has to know about.
package providers

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// SecretRef identifies one secret within a provider.
type SecretRef struct {
	// ID is the secret's name, path or ARN — whatever the provider calls it.
	ID string
	// Params carries what a particular provider needs to find it: a region for
	// AWS, a project for GCP, a vault for Azure. A provider ignores what it
	// does not recognise, because the alternative is a struct that grows a
	// field per vendor and means nothing in most of them.
	Params map[string]string
}

// Secrets fetches a secret's values.
//
// A secret stored as a JSON object yields its fields; one stored as a plain
// string yields a single entry under the "" key. That convention is joka's,
// not a vendor's, so every provider owes the same shape.
type Secrets interface {
	Fetch(ctx context.Context, ref SecretRef) (map[string]string, error)
}

// StateRef locates one state document within a state store.
type StateRef struct {
	// Container is the bucket, share or directory the document lives in.
	Container string
	// Key is the document's name within the container.
	Key string
	// Params is provider-specific, as in SecretRef.
	Params map[string]string
}

// String renders the reference for messages, in the form a person would write.
func (r StateRef) String() string {
	return r.Container + "/" + r.Key
}

// StateStore keeps the state document somewhere other than the working
// directory.
//
// It deals in bytes because the document's shape belongs to joka, not to a
// vendor. Read reports a missing document as found=false rather than an error:
// a database nobody has synced from yet has none, and that is the ordinary
// first run.
type StateStore interface {
	Read(ctx context.Context, ref StateRef) (body []byte, found bool, err error)
	Write(ctx context.Context, ref StateRef, body []byte) error
}

// TunnelSpec describes a port forward to open.
type TunnelSpec struct {
	// Target is the thing being tunnelled through: an SSM instance id, a
	// bastion, an IAP resource. Empty when TargetTags selects one instead.
	Target string
	// TargetTags selects the target by tag when no id was given. Every tag must
	// match, and the provider picks deterministically among the matches.
	//
	// Tags rather than something AWS-shaped because every cloud has them under
	// one name or another - labels on GCP, tags on Azure - and the reason for
	// selecting at all is the same everywhere: an instance in an autoscaling
	// group has no stable id to write down.
	TargetTags map[string]string
	// RemoteHost and RemotePort are the database, as addressed from the
	// target's side of the network.
	RemoteHost string
	RemotePort int
	// LocalPort is the port to listen on, or zero to let the provider pick a
	// free one. Zero is the ordinary case: a fixed port collides with whatever
	// else is running, and nothing outside this process needs to predict it.
	LocalPort int
	// Params is provider-specific, as in SecretRef.
	Params map[string]string
}

// Session is an open tunnel. Close is always safe to call and safe to call
// twice, because it runs from a defer on a path that may already have failed.
type Session struct {
	LocalHost string
	LocalPort int
	Close     func() error
}

// Tunnel opens a local port forwarded to a remote one.
type Tunnel interface {
	Open(ctx context.Context, spec TunnelSpec) (*Session, error)
}

var (
	mu      sync.RWMutex
	secrets = map[string]Secrets{}
	tunnels = map[string]Tunnel{}
	states  = map[string]StateStore{}
)

// RegisterSecrets makes a secrets provider available under a name. Called from
// a provider package's init, so importing the package is what enables it.
func RegisterSecrets(name string, s Secrets) {
	mu.Lock()
	defer mu.Unlock()
	secrets[name] = s
}

// RegisterTunnel makes a tunnel provider available under a name.
func RegisterTunnel(name string, t Tunnel) {
	mu.Lock()
	defer mu.Unlock()
	tunnels[name] = t
}

// RegisterState makes a state store available under a backend name, the value
// of `backend:` in the `state:` block.
func RegisterState(name string, s StateStore) {
	mu.Lock()
	defer mu.Unlock()
	states[name] = s
}

// LookupState returns the state store registered under a backend name.
func LookupState(name string) (StateStore, error) {
	mu.RLock()
	defer mu.RUnlock()

	s, ok := states[name]
	if !ok {
		have := make([]string, 0, len(states))
		for k := range states {
			have = append(have, k)
		}
		sort.Strings(have)
		return nil, unknown("state", name, have)
	}

	return s, nil
}

// LookupSecrets returns the named secrets provider.
func LookupSecrets(name string) (Secrets, error) {
	mu.RLock()
	defer mu.RUnlock()

	s, ok := secrets[name]
	if !ok {
		return nil, unknown("secrets", name, names(secrets))
	}

	return s, nil
}

// LookupTunnel returns the named tunnel provider.
func LookupTunnel(name string) (Tunnel, error) {
	mu.RLock()
	defer mu.RUnlock()

	t, ok := tunnels[name]
	if !ok {
		return nil, unknown("tunnel", name, namesT(tunnels))
	}

	return t, nil
}

func unknown(kind, name string, have []string) error {
	if len(have) == 0 {
		return fmt.Errorf("unknown %s provider %q, and none are registered", kind, name)
	}

	return fmt.Errorf("unknown %s provider %q (joka has %v)", kind, name, have)
}

func names(m map[string]Secrets) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)

	return out
}

func namesT(m map[string]Tunnel) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)

	return out
}
