package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kubernetes/k8s-cost/internal/model"
)

// IBMConfig configures an IBM Cloud billing collection run.
type IBMConfig struct {
	// APIKey is an IBM Cloud IAM API key. Exchanged for an access token.
	APIKey string
	// AccountID is the IBM Cloud account GUID. If empty it is auto-discovered
	// from the first account the key can access.
	AccountID string
	// Provider is the normalized provider to store records under. Defaults to
	// ProviderIBMPower; use ProviderIBMZ for the s390x account.
	Provider model.Provider
	// Start and End bound the collected months (End exclusive).
	Start time.Time
	End   time.Time
	// HTTPClient is optional.
	HTTPClient *http.Client
}

const (
	ibmIAMTokenURL = "https://iam.cloud.ibm.com/identity/token"
	ibmAccountsURL = "https://accounts.cloud.ibm.com/v1/accounts"
	ibmBillingBase = "https://billing.cloud.ibm.com/v4/accounts"
)

// CollectIBM pulls monthly billable cost from the IBM Cloud usage API for each
// month in [Start, End) and maps it to one model.DailySpend per month (dated to
// the first of the month). IBM bills monthly, so there is no daily data.
func CollectIBM(ctx context.Context, cfg IBMConfig) ([]model.DailySpend, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("IBM Cloud API key is required")
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	token, err := ibmAccessToken(ctx, client, cfg.APIKey)
	if err != nil {
		return nil, err
	}

	accountID := cfg.AccountID
	if accountID == "" {
		accountID, err = ibmDiscoverAccount(ctx, client, token)
		if err != nil {
			return nil, err
		}
	}

	provider := cfg.Provider
	if provider == "" {
		provider = model.ProviderIBMPower
	}

	var out []model.DailySpend
	for m := monthStart(cfg.Start); m.Before(cfg.End); m = m.AddDate(0, 1, 0) {
		billingMonth := m.Format("2006-01")
		cost, found, err := ibmMonthlyBillableCost(ctx, client, token, accountID, billingMonth)
		if err != nil {
			return nil, fmt.Errorf("usage %s: %w", billingMonth, err)
		}
		if !found {
			continue // no usage data for this month (account inactive / too old)
		}
		out = append(out, model.DailySpend{
			Provider: provider,
			Date:     model.NewDate(m),
			Amount:   cost,
			Currency: "USD",
		})
	}
	return out, nil
}

func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// ibmAccessToken exchanges an API key for an IAM bearer access token.
func ibmAccessToken(ctx context.Context, client *http.Client, apiKey string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "urn:ibm:params:oauth:grant-type:apikey")
	form.Set("apikey", apiKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ibmIAMTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("IBM IAM token %s", resp.Status)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", err
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("IBM IAM returned empty access_token")
	}
	return tr.AccessToken, nil
}

// ibmDiscoverAccount returns the GUID of the first accessible account.
func ibmDiscoverAccount(ctx context.Context, client *http.Client, token string) (string, error) {
	var resp struct {
		Resources []struct {
			Metadata struct {
				GUID string `json:"guid"`
			} `json:"metadata"`
		} `json:"resources"`
	}
	if _, err := ibmGet(ctx, client, token, ibmAccountsURL, &resp); err != nil {
		return "", err
	}
	if len(resp.Resources) == 0 || resp.Resources[0].Metadata.GUID == "" {
		return "", fmt.Errorf("no IBM Cloud account found; pass --account explicitly")
	}
	return resp.Resources[0].Metadata.GUID, nil
}

// ibmMonthlyBillableCost sums billable_cost across all resources for a month.
// found is false when the month has no usage report (HTTP 404).
func ibmMonthlyBillableCost(ctx context.Context, client *http.Client, token, accountID, billingMonth string) (float64, bool, error) {
	u := fmt.Sprintf("%s/%s/usage/%s", ibmBillingBase, accountID, billingMonth)
	var resp struct {
		Resources []struct {
			BillableCost float64 `json:"billable_cost"`
		} `json:"resources"`
	}
	status, err := ibmGet(ctx, client, token, u, &resp)
	if err != nil {
		return 0, false, err
	}
	if status == http.StatusNotFound {
		return 0, false, nil
	}
	var total float64
	for _, r := range resp.Resources {
		total += r.BillableCost
	}
	return total, true, nil
}

// ibmGet performs a GET and decodes the body. It returns the HTTP status so
// callers can treat 404 (no usage for a month) as a non-error. The body is only
// decoded for 2xx responses.
func ibmGet(ctx context.Context, client *http.Client, token, u string, v any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return resp.StatusCode, fmt.Errorf("IBM API %s (token lacks billing/usage access)", resp.Status)
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("IBM API %s for %s", resp.Status, u)
	}
	return resp.StatusCode, json.NewDecoder(resp.Body).Decode(v)
}






