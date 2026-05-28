package connection

import (
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// awsSecretsManager fetches secrets from AWS Secrets Manager using the default
// credential chain (env vars, shared config, SSO, instance role, ...).
type awsSecretsManager struct{}

func newAWSSecretsManager() SecretFetcher { return &awsSecretsManager{} }

func (a *awsSecretsManager) Fetch(ctx context.Context, secretID, region string) (map[string]string, error) {
	opts := []func(*awsconfig.LoadOptions) error{}
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("loading aws config: %w", err)
	}

	client := secretsmanager.NewFromConfig(cfg)
	out, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &secretID})
	if err != nil {
		return nil, err
	}
	if out.SecretString == nil {
		return nil, fmt.Errorf("secret %q has no string value", secretID)
	}

	return parseSecretString(*out.SecretString), nil
}
