package aws

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/apsdsm/joka/internal/providers"
)

// S3Backend is the `backend:` value that selects S3 for the state document.
const S3Backend = "s3"

// ParamEndpoint overrides the S3 endpoint, for S3-compatible stores such as
// MinIO and for tests. When it is set the bucket is addressed by path rather
// than by host name, which is what those stores expect.
const ParamEndpoint = "endpoint"

func init() {
	providers.RegisterState(S3Backend, S3State{})
}

// S3State keeps the state document in an S3 object, using the default
// credential chain.
type S3State struct{}

// Read returns the object's body. A missing object is found=false; any other
// failure, including a denied request, is an error, because the wrong-database
// check must not treat "could not look" as "nothing there".
func (S3State) Read(ctx context.Context, ref providers.StateRef) ([]byte, bool, error) {
	client, err := s3Client(ctx, ref.Params)
	if err != nil {
		return nil, false, err
	}

	out, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: &ref.Container, Key: &ref.Key})
	if err != nil {
		var missing *types.NoSuchKey
		if errors.As(err, &missing) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("reading s3://%s: %w", ref, err)
	}
	defer out.Body.Close()

	body, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, false, fmt.Errorf("reading s3://%s: %w", ref, err)
	}

	return body, true, nil
}

// Write replaces the object. A single PutObject is atomic: a reader sees the
// previous document or the new one, never part of either.
func (S3State) Write(ctx context.Context, ref providers.StateRef, body []byte) error {
	client, err := s3Client(ctx, ref.Params)
	if err != nil {
		return err
	}

	contentType := "application/json"
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &ref.Container,
		Key:         &ref.Key,
		Body:        bytes.NewReader(body),
		ContentType: &contentType,
	})
	if err != nil {
		return fmt.Errorf("writing s3://%s: %w", ref, err)
	}

	return nil
}

func s3Client(ctx context.Context, params map[string]string) (*s3.Client, error) {
	cfg, err := loadConfig(ctx, params)
	if err != nil {
		return nil, err
	}

	endpoint := params[ParamEndpoint]

	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = &endpoint
			o.UsePathStyle = true
		}
	}), nil
}
