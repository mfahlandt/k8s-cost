package collector

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// AWSQueryConfig configures an ad-hoc Cost Explorer query. Unlike AWSConfig
// (which feeds the store), this is meant for one-off analyses such as "what did
// a single prow job cost, broken down per AWS service / usage type?".
type AWSQueryConfig struct {
	Start time.Time // inclusive day
	End   time.Time // exclusive day

	Profile string // optional shared-config profile
	Region  string // optional; Cost Explorer is global, defaults to us-east-1

	// Metric is the Cost Explorer metric ("UnblendedCost", "AmortizedCost",
	// "UsageQuantity", ...). Default "UnblendedCost".
	Metric string
	// Granularity is DAILY (default), MONTHLY or HOURLY.
	Granularity cetypes.Granularity

	// GroupBy holds up to two group definitions in the compact form
	// "SERVICE", "USAGE_TYPE", "RECORD_TYPE", "REGION", "INSTANCE_TYPE" or
	// "TAG:<key>" / "COST_CATEGORY:<name>".
	GroupBy []string

	// TagFilters restrict the query to resources carrying the given tag
	// key/values, e.g. {"prow.k8s.io/job": {"ci-kubernetes-e2e-kops-..."}}.
	// Multiple keys are ANDed, values within a key are ORed.
	TagFilters map[string][]string

	// DimensionFilters restrict on dimensions, e.g. {"SERVICE": {"Amazon Elastic Compute Cloud - Compute"}}.
	DimensionFilters map[string][]string

	// IncludeRefundsCredits, when false (default), excludes the Refund and
	// Credit record types (matching the k8s Cost Explorer view).
	IncludeRefundsCredits bool
}

// CostRow is one Cost Explorer result cell: a time period, the group keys and
// the metric amount.
//
// Amount is the effective (net) cost; Gross is the same usage before
// provider-side discounts. On AWS the two are identical for these accounts
// (no reserved instances or savings plans), on GCP Gross excludes the
// committed-use/sustained-use credits.
type CostRow struct {
	Start  string   `json:"start"`
	End    string   `json:"end"`
	Keys   []string `json:"keys"`
	Amount float64  `json:"amount"`
	Gross  float64  `json:"gross"`
	Unit   string   `json:"unit"`
}

// QueryAWS runs an ad-hoc GetCostAndUsage query and returns the raw rows.
func QueryAWS(ctx context.Context, cfg AWSQueryConfig) ([]CostRow, error) {
	client, err := newCEClient(ctx, cfg.Profile, cfg.Region)
	if err != nil {
		return nil, err
	}

	metric := cfg.Metric
	if metric == "" {
		metric = "UnblendedCost"
	}
	gran := cfg.Granularity
	if gran == "" {
		gran = cetypes.GranularityDaily
	}
	groups, err := parseGroupBy(cfg.GroupBy)
	if err != nil {
		return nil, err
	}
	filter := buildAWSFilter(cfg)

	var out []CostRow
	var nextToken *string
	for {
		resp, err := client.GetCostAndUsage(ctx, &costexplorer.GetCostAndUsageInput{
			Granularity: gran,
			Metrics:     []string{metric},
			TimePeriod: &cetypes.DateInterval{
				Start: aws.String(cfg.Start.Format("2006-01-02")),
				End:   aws.String(cfg.End.Format("2006-01-02")),
			},
			GroupBy:       groups,
			Filter:        filter,
			NextPageToken: nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("GetCostAndUsage: %w", err)
		}
		for _, r := range resp.ResultsByTime {
			start, end := "", ""
			if r.TimePeriod != nil {
				start = aws.ToString(r.TimePeriod.Start)
				end = aws.ToString(r.TimePeriod.End)
			}
			if len(r.Groups) == 0 {
				mv, ok := r.Total[metric]
				if !ok {
					continue
				}
				amount, err := parseAWSAmount(aws.ToString(mv.Amount))
				if err != nil {
					return nil, err
				}
				out = append(out, CostRow{Start: start, End: end, Amount: amount, Gross: amount, Unit: aws.ToString(mv.Unit)})
				continue
			}
			for _, g := range r.Groups {
				mv, ok := g.Metrics[metric]
				if !ok {
					continue
				}
				amount, err := parseAWSAmount(aws.ToString(mv.Amount))
				if err != nil {
					return nil, err
				}
				out = append(out, CostRow{
					Start:  start,
					End:    end,
					Keys:   append([]string(nil), g.Keys...),
					Amount: amount,
					// These accounts carry no reserved instances or savings
					// plans, so the discounted and list figures coincide.
					Gross: amount,
					Unit:  aws.ToString(mv.Unit),
				})
			}
		}
		if resp.NextPageToken == nil || aws.ToString(resp.NextPageToken) == "" {
			break
		}
		nextToken = resp.NextPageToken
	}
	return out, nil
}

// ListAWSTagValues returns the values Cost Explorer knows for a cost-allocation
// tag key in the given window. Useful to check whether a tag (e.g.
// "prow.k8s.io/job") is activated for cost allocation at all: an inactive tag
// simply returns no values.
func ListAWSTagValues(ctx context.Context, cfg AWSQueryConfig, tagKey, search string) ([]string, error) {
	client, err := newCEClient(ctx, cfg.Profile, cfg.Region)
	if err != nil {
		return nil, err
	}
	var out []string
	var next *string
	for {
		in := &costexplorer.GetTagsInput{
			TimePeriod: &cetypes.DateInterval{
				Start: aws.String(cfg.Start.Format("2006-01-02")),
				End:   aws.String(cfg.End.Format("2006-01-02")),
			},
			NextPageToken: next,
		}
		if tagKey != "" {
			in.TagKey = aws.String(tagKey)
		}
		if search != "" {
			in.SearchString = aws.String(search)
		}
		resp, err := client.GetTags(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("GetTags: %w", err)
		}
		out = append(out, resp.Tags...)
		if resp.NextPageToken == nil || aws.ToString(resp.NextPageToken) == "" {
			break
		}
		next = resp.NextPageToken
	}
	sort.Strings(out)
	return out, nil
}

func newCEClient(ctx context.Context, profile, region string) (*costexplorer.Client, error) {
	if region == "" {
		region = "us-east-1" // Cost Explorer global endpoint
	}
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return costexplorer.NewFromConfig(awsCfg), nil
}

// parseGroupBy converts the compact CLI form into Cost Explorer group defs.
func parseGroupBy(specs []string) ([]cetypes.GroupDefinition, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	if len(specs) > 2 {
		return nil, fmt.Errorf("cost explorer supports at most 2 group-by keys, got %d", len(specs))
	}
	var out []cetypes.GroupDefinition
	for _, s := range specs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		switch {
		case strings.HasPrefix(strings.ToUpper(s), "TAG:"):
			out = append(out, cetypes.GroupDefinition{
				Type: cetypes.GroupDefinitionTypeTag,
				Key:  aws.String(s[len("TAG:"):]),
			})
		case strings.HasPrefix(strings.ToUpper(s), "COST_CATEGORY:"):
			out = append(out, cetypes.GroupDefinition{
				Type: cetypes.GroupDefinitionTypeCostCategory,
				Key:  aws.String(s[len("COST_CATEGORY:"):]),
			})
		default:
			out = append(out, cetypes.GroupDefinition{
				Type: cetypes.GroupDefinitionTypeDimension,
				Key:  aws.String(strings.ToUpper(s)),
			})
		}
	}
	return out, nil
}

// buildAWSFilter ANDs the record-type exclusion, tag filters and dimension
// filters into a single Cost Explorer expression.
func buildAWSFilter(cfg AWSQueryConfig) *cetypes.Expression {
	var exprs []cetypes.Expression

	if !cfg.IncludeRefundsCredits {
		exprs = append(exprs, cetypes.Expression{
			Not: &cetypes.Expression{
				Dimensions: &cetypes.DimensionValues{
					Key:    cetypes.DimensionRecordType,
					Values: []string{"Refund", "Credit"},
				},
			},
		})
	}
	for _, key := range sortedKeys(cfg.TagFilters) {
		exprs = append(exprs, cetypes.Expression{
			Tags: &cetypes.TagValues{
				Key:    aws.String(key),
				Values: cfg.TagFilters[key],
			},
		})
	}
	for _, key := range sortedKeys(cfg.DimensionFilters) {
		exprs = append(exprs, cetypes.Expression{
			Dimensions: &cetypes.DimensionValues{
				Key:    cetypes.Dimension(strings.ToUpper(key)),
				Values: cfg.DimensionFilters[key],
			},
		})
	}

	switch len(exprs) {
	case 0:
		return nil
	case 1:
		e := exprs[0]
		return &e
	default:
		return &cetypes.Expression{And: exprs}
	}
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}



