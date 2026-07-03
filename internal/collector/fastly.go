package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/kubernetes/k8s-cost/internal/model"
)

// FastlyConfig configures a Fastly bandwidth collection run.
//
// Fastly invoices for the Kubernetes project are $0 (covered by a committed
// bandwidth plan), so we track *bandwidth usage* (in GB) rather than dollars.
type FastlyConfig struct {
	// Token is a Fastly API token with stats read access. Falls back to the
	// FASTLY_API_TOKEN env var (handled by the caller).
	Token string
	// Start and End bound the collected days (End exclusive).
	Start time.Time
	End   time.Time
	// HTTPClient is optional.
	HTTPClient *http.Client
}

// UnitGB is the currency/unit string used for bandwidth records.
const UnitGB = "GB"

const fastlyStatsAggregate = "https://api.fastly.com/stats/aggregate"

type fastlyStatsResponse struct {
	Status string `json:"status"`
	Data   []struct {
		StartTime int64   `json:"start_time"`
		Bandwidth float64 `json:"bandwidth"` // bytes
	} `json:"data"`
	Meta struct {
		NextPage string `json:"next_page"`
	} `json:"meta"`
}

// CollectFastly pulls daily bandwidth usage aggregated across all services and
// returns records whose Amount is GB (Currency = "GB"), not dollars.
func CollectFastly(ctx context.Context, cfg FastlyConfig) ([]model.DailySpend, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("Fastly API token is required")
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	q := url.Values{}
	q.Set("from", strconv.FormatInt(cfg.Start.Unix(), 10))
	q.Set("to", strconv.FormatInt(cfg.End.Unix(), 10))
	q.Set("by", "day")
	q.Set("region", "all")
	reqURL := fastlyStatsAggregate + "?" + q.Encode()

	var out []model.DailySpend
	for reqURL != "" {
		var resp fastlyStatsResponse
		if err := fastlyGet(ctx, client, cfg.Token, reqURL, &resp); err != nil {
			return nil, err
		}
		for _, d := range resp.Data {
			day := time.Unix(d.StartTime, 0).UTC()
			out = append(out, model.DailySpend{
				Provider: model.ProviderFastly,
				Date:     model.NewDate(day),
				Amount:   d.Bandwidth / 1e9, // bytes -> GB
				Currency: UnitGB,
			})
		}
		reqURL = resp.Meta.NextPage
	}
	return out, nil
}

func fastlyGet(ctx context.Context, client *http.Client, token, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Fastly-Key", token)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("Fastly API %s (token lacks stats access)", resp.Status)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("Fastly API %s for %s", resp.Status, u)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

