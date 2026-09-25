package aws

import (
	"context"
	"fmt"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/apsdsm/joka/internal/providers"
)

// ErrNoSuchInstance means no running instance carries the tags a tunnel
// selects by.
var ErrNoSuchInstance = fmt.Errorf("no running instance matches the tunnel target")

// findInstance returns the instance id a spec names, resolving tags when no id
// was given.
//
// Only running instances are considered: a stopped or terminated one cannot
// forward a port, and matching it would produce a session that fails for a
// reason the tag did not suggest.
func findInstance(ctx context.Context, lookup instanceLookup, spec providers.TunnelSpec) (string, error) {
	if spec.Target != "" {
		return spec.Target, nil
	}
	if len(spec.TargetTags) == 0 {
		return "", fmt.Errorf("the tunnel names no target: set an instance id or a tag selector")
	}

	ids, err := lookup(ctx, spec.Params, spec.TargetTags)
	if err != nil {
		return "", err
	}

	if len(ids) == 0 {
		return "", fmt.Errorf("%w: %s (in %s)", ErrNoSuchInstance,
			describeTags(spec.TargetTags), describeAccount(spec.Params))
	}

	// Sorted and first, rather than whichever the API listed first. The
	// instances are interchangeable relays, so any would do — but a run that
	// picks the same one twice is one whose logs can be read against each
	// other, and the cost of determinism here is a sort of a handful of ids.
	sort.Strings(ids)

	return ids[0], nil
}

// instanceLookup is the EC2 call, as a function so the resolution logic can be
// tested without an AWS account.
type instanceLookup func(ctx context.Context, params, tags map[string]string) ([]string, error)

// describeInstances is the real lookup.
func describeInstances(ctx context.Context, params, tags map[string]string) ([]string, error) {
	cfg, err := loadConfig(ctx, params)
	if err != nil {
		return nil, err
	}

	filters := []ec2types.Filter{{
		Name:   strPtr("instance-state-name"),
		Values: []string{"running"},
	}}

	for _, key := range sortedKeys(tags) {
		value := tags[key]
		filters = append(filters, ec2types.Filter{
			Name:   strPtr("tag:" + key),
			Values: []string{value},
		})
	}

	var ids []string
	paginator := ec2.NewDescribeInstancesPaginator(
		ec2.NewFromConfig(cfg), &ec2.DescribeInstancesInput{Filters: filters})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("looking up the tunnel target: %w", err)
		}

		for _, reservation := range page.Reservations {
			for _, instance := range reservation.Instances {
				if instance.InstanceId != nil {
					ids = append(ids, *instance.InstanceId)
				}
			}
		}
	}

	return ids, nil
}

// loadConfig builds an AWS config from the provider-neutral params, so the
// region and profile a tunnel declares reach every call it makes.
func loadConfig(ctx context.Context, params map[string]string) (aws.Config, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if region := params[ParamRegion]; region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	if profile := params[ParamProfile]; profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return cfg, fmt.Errorf("loading aws config: %w", err)
	}

	return cfg, nil
}

// describeTags renders a selector for an error message, sorted so two runs
// describe one selector the same way.
func describeTags(tags map[string]string) string {
	out := ""
	for _, key := range sortedKeys(tags) {
		if out != "" {
			out += ","
		}
		out += key + "=" + tags[key]
	}

	return out
}

// describeAccount names where joka looked, which is most of the answer when a
// tag that exists finds nothing: the wrong profile, or the wrong region.
func describeAccount(params map[string]string) string {
	region, profile := params[ParamRegion], params[ParamProfile]

	switch {
	case region != "" && profile != "":
		return "region " + region + ", profile " + profile
	case region != "":
		return "region " + region
	case profile != "":
		return "profile " + profile
	}

	return "the default region and profile"
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	return keys
}

func strPtr(s string) *string { return &s }
