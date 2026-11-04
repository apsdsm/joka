package infra

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apsdsm/joka/internal/providers"
)

// StateStore is where the state document is kept: a file beside the working
// directory, or an object in a remote store.
//
// Callers hold one of these instead of a path, so the wrong-database check, the
// status report and the post-commit write all reach the same place.
type StateStore interface {
	// Describe names the location for messages: a path or a URL.
	Describe() string
	// Read returns the document. found is false when there is none, which is
	// not an error.
	Read(ctx context.Context) (doc StateDocument, found bool, err error)
	// Write replaces the document.
	Write(ctx context.Context, doc StateDocument) error
}

// RemoteState is a `state:` block, resolved far enough to open a store.
type RemoteState struct {
	// Backend names a registered store, such as "s3".
	Backend string
	// Container and Key locate the document. An empty Key takes the default
	// file name, so one bucket can serve several profiles without each
	// repeating the name.
	Container string
	Key       string
	// Params is provider-specific: region and profile for s3.
	Params map[string]string
}

// NewStateStore returns the store for the given settings.
//
// remote nil means a local file, which is what every config without a `state:`
// block gets. A remote store and an explicit local path are two answers to one
// question, so naming both is refused rather than one silently winning.
func NewStateStore(explicit, profile string, remote *RemoteState) (StateStore, error) {
	if remote == nil {
		return FileStore{Path: StateFilePath(explicit, profile)}, nil
	}

	if explicit != "" {
		return nil, fmt.Errorf("both a state: block and a statefile path (%s) say where the state lives; use one", explicit)
	}
	if remote.Container == "" {
		return nil, fmt.Errorf("state: backend %q needs a bucket", remote.Backend)
	}

	store, err := providers.LookupState(remote.Backend)
	if err != nil {
		return nil, err
	}

	key := remote.Key
	if key == "" {
		key = StateFilePath("", profile)
	}

	return RemoteStore{
		Store: store,
		Ref:   providers.StateRef{Container: remote.Container, Key: key, Params: remote.Params},
		Label: fmt.Sprintf("%s://%s/%s", remote.Backend, remote.Container, key),
	}, nil
}

// FileStore keeps the document in a local file.
type FileStore struct {
	Path string
}

func (f FileStore) Describe() string { return f.Path }

func (f FileStore) Read(_ context.Context) (StateDocument, bool, error) {
	return ReadStateFile(f.Path)
}

func (f FileStore) Write(_ context.Context, doc StateDocument) error {
	return WriteStateFile(f.Path, doc)
}

// RemoteStore keeps the document in a provider's store.
type RemoteStore struct {
	Store providers.StateStore
	Ref   providers.StateRef
	Label string
}

func (r RemoteStore) Describe() string { return r.Label }

func (r RemoteStore) Read(ctx context.Context) (StateDocument, bool, error) {
	body, found, err := r.Store.Read(ctx, r.Ref)
	if err != nil || !found {
		return StateDocument{}, false, err
	}

	var doc StateDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return StateDocument{}, false, fmt.Errorf("decoding %s: %w", r.Label, err)
	}

	return doc, true, nil
}

func (r RemoteStore) Write(ctx context.Context, doc StateDocument) error {
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the state document: %w", err)
	}

	return r.Store.Write(ctx, r.Ref, append(body, '\n'))
}
