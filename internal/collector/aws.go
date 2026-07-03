package collector

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"

	"github.com/kubernetes/k8s-cost/internal/model"
)

// AWSConfig configures a Cost Explorer collection run. Credentials come from the
// standard AWS chain (env vars, shared config/credentials, SSO, or IAM role);
// Profile/Region are optional overrides.
type AWSConfig struct {
	Start   time.Time // inclusive day
	End     time.Time // exclusive day
	Profile string    // optional shared-config profile (AWS_PROFILE)
	Region  string    // optional; Cost Explorer is global, defaults to us-east-1
	// Metric is the Cost Explorer metric to pull. Default "UnblendedCost".
	// The 2025 sheet's "AWS Cost Management" table uses unblended cost.
	Metric string
	// IncludeRefundsCredits, when false (default), excludes RECORD_TYPE
	// "Refund" and "Credit" to match the k8s Cost Explorer view
	// (RecordType EXCLUDES Refund, Credit).
	IncludeRefundsCredits bool
}

// CollectAWS pulls daily spend from AWS Cost Explorer (GetCostAndUsage).
func CollectAWS(ctx context.Context, cfg AWSConfig) ([]model.DailySpend, error) {
	region := cfg.Region
	if region == "" {
		region = "us-east-1" // Cost Explorer global endpoint
	}
	metric := cfg.Metric
	if metric == "" {
		metric = "UnblendedCost"
	}

	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if cfg.Profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(cfg.Profile))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	client := costexplorer.NewFromConfig(awsCfg)

	start := cfg.Start.Format("2006-01-02")
	end := cfg.End.Format("2006-01-02")

	// Match the k8s Cost Explorer view: exclude Refund + Credit charge types.
	var filter *cetypes.Expression
	if !cfg.IncludeRefundsCredits {
		filter = &cetypes.Expression{
			Not: &cetypes.Expression{
				Dimensions: &cetypes.DimensionValues{
					Key:    cetypes.DimensionRecordType,
					Values: []string{"Refund", "Credit"},
				},
			},
		}
	}

	var out []model.DailySpend
	var nextToken *string
	for {
		resp, err := client.GetCostAndUsage(ctx, &costexplorer.GetCostAndUsageInput{
			Granularity: cetypes.GranularityDaily,
			Metrics:     []string{metric},
			TimePeriod: &cetypes.DateInterval{
				Start: aws.String(start),
				End:   aws.String(end),
			},
			Filter:        filter,
			NextPageToken: nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("GetCostAndUsage: %w", err)
		}
		for _, r := range resp.ResultsByTime {
			if r.TimePeriod == nil || r.Total == nil {
				continue
			}
			day, err := model.ParseDate(aws.ToString(r.TimePeriod.Start))
			if err != nil {
				return nil, fmt.Errorf("parse period %q: %w", aws.ToString(r.TimePeriod.Start), err)
			}
			mv, ok := r.Total[metric]
			if !ok {
				continue
			}
			amount, err := parseAWSAmount(aws.ToString(mv.Amount))
			if err != nil {
				return nil, err
			}
			currency := aws.ToString(mv.Unit)
			if currency == "" {
				currency = "USD"
			}
			out = append(out, model.DailySpend{
				Provider: model.ProviderAWS,
				Date:     day,
				Amount:   amount,
				Currency: currency,
			})
		}
		if resp.NextPageToken == nil || aws.ToString(resp.NextPageToken) == "" {
			break
		}
		nextToken = resp.NextPageToken
	}
	return out, nil
}

func parseAWSAmount(s string) (float64, error) {
	var v float64
	if s == "" {
		return 0, nil
	}
	if _, err := fmt.Sscanf(s, "%g", &v); err != nil {
		return 0, fmt.Errorf("parse amount %q: %w", s, err)
	}
	return v, nil
}



