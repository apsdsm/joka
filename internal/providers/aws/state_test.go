package aws

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/apsdsm/joka/internal/providers"
)

// fakeS3 is just enough of S3's object API for one bucket: path-style GET and
// PUT. Anything else is a test failure.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string]string
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.objects[r.URL.Path] = string(body)
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		body, ok := f.objects[r.URL.Path]
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `<?xml version="1.0"?><Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`) //nolint:errcheck
			return
		}
		io.WriteString(w, body) //nolint:errcheck
	default:
		http.Error(w, "unexpected "+r.Method, http.StatusMethodNotAllowed)
	}
}

func newRef(t *testing.T) (providers.StateRef, *fakeS3) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")

	fake := &fakeS3{objects: map[string]string{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	return providers.StateRef{
		Container: "bkt",
		Key:       "joka/prod.state.json",
		Params:    map[string]string{ParamRegion: "us-east-1", ParamEndpoint: srv.URL},
	}, fake
}

func TestS3StateRoundTrip(t *testing.T) {
	ref, fake := newRef(t)
	ctx := context.Background()

	if err := (S3State{}).Write(ctx, ref, []byte(`{"version":3}`)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := fake.objects["/bkt/joka/prod.state.json"]; got != `{"version":3}` {
		t.Fatalf("object body = %q", got)
	}

	body, found, err := (S3State{}).Read(ctx, ref)
	if err != nil || !found {
		t.Fatalf("Read: found=%v err=%v", found, err)
	}
	if !strings.Contains(string(body), `"version":3`) {
		t.Fatalf("Read body = %q", body)
	}
}

func TestS3StateMissingObjectIsNotAnError(t *testing.T) {
	ref, _ := newRef(t)

	_, found, err := (S3State{}).Read(context.Background(), ref)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if found {
		t.Fatal("found = true for an object that does not exist")
	}
}

func TestS3StateRegisters(t *testing.T) {
	if _, err := providers.LookupState(S3Backend); err != nil {
		t.Fatalf("s3 state store not registered: %v", err)
	}
}
