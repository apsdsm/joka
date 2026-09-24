// Package aws implements joka's providers against AWS: Secrets Manager for
// secrets, and Session Manager port forwarding for tunnels.
//
// Importing this package registers both. Nothing else in joka names AWS.
package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/apsdsm/joka/internal/providers"
)

// Name is what a config says to reach AWS.
const Name = "aws"

// ParamRegion and ParamProfile are the AWS-specific parameters these providers
// read out of a Params map. They are AWS's concerns, so they travel there
// rather than in the provider-neutral SecretRef and TunnelSpec.
const (
	ParamRegion  = "region"
	ParamProfile = "profile"
)

func init() {
	providers.RegisterSecrets(Name, SecretsManager{})
	providers.RegisterTunnel(Name, SessionManager{})
}

// SecretsManager fetches secrets from AWS Secrets Manager using the default
// credential chain (env vars, shared config, SSO, instance role, ...).
type SecretsManager struct{}

// Fetch returns the secret's values. A JSON object yields its fields; a plain
// string yields one entry under the "" key.
func (SecretsManager) Fetch(ctx context.Context, ref providers.SecretRef) (map[string]string, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if region := ref.Params[ParamRegion]; region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	if profile := ref.Params[ParamProfile]; profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("loading aws config: %w", err)
	}

	out, err := secretsmanager.NewFromConfig(cfg).GetSecretValue(ctx,
		&secretsmanager.GetSecretValueInput{SecretId: &ref.ID})
	if err != nil {
		return nil, err
	}
	if out.SecretString == nil {
		return nil, fmt.Errorf("secret %q has no string value", ref.ID)
	}

	return ParseSecretString(*out.SecretString), nil
}

// ParseSecretString reads a secret payload as a flat JSON object, falling back
// to a single unnamed entry for a plain string.
//
// Exported because the whole-URL mode reads the fallback entry by key, and a
// provider that stores a bare DSN is not an AWS peculiarity.
func ParseSecretString(s string) map[string]string {
	var obj map[string]any
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return map[string]string{"": s}
	}

	out := make(map[string]string, len(obj))
	for k, v := range obj {
		switch t := v.(type) {
		case string:
			out[k] = t
		case float64:
			out[k] = fmt.Sprintf("%g", t)
		case bool:
			out[k] = fmt.Sprintf("%t", t)
		default:
			b, err := json.Marshal(t)
			if err != nil {
				continue
			}
			out[k] = string(b)
		}
	}

	return out
}
