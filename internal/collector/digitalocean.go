package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kubernetes/k8s-cost/internal/model"
)

// DOConfig configures a DigitalOcean billing collection run.
type DOConfig struct {
	// Token is a DigitalOcean API personal access token (read scope). If empty
	// the collector falls back to the DIGITALOCEAN_TOKEN env var (handled by the
	// caller).
	Token string
	// IncludePreview adds the current in-progress month from invoice_preview.
	IncludePreview bool
	// HTTPClient is optional; a sane default is used when nil.
	HTTPClient *http.Client
}

const doAPIBase = "https://api.digitalocean.com/v2"

type doInvoice struct {
	InvoiceUUID   string `json:"invoice_uuid"`
	Amount        string `json:"amount"`
	InvoicePeriod string `json:"invoice_period"` // "YYYY-MM"
}

type doInvoicesResponse struct {
	Invoices       []doInvoice `json:"invoices"`
	InvoicePreview doInvoice   `json:"invoice_preview"`
	Links          struct {
		Pages struct {
			Next string `json:"next"`
		} `json:"pages"`
	} `json:"links"`
}

// doSummaryItem is a grouped charge block in an invoice summary. DigitalOcean
// zeroes out the top-level invoice amount when credits/discounts apply, so the
// real spend must be read from product_charges (+ overages), before credits.
// Items breaks the group down per product (Droplets, Spaces, Load Balancers…).
type doSummaryItem struct {
	Name  string       `json:"name"`
	Amount string      `json:"amount"`
	Items []doLineItem `json:"items"`
}

// doLineItem is a single per-product line within a summary group.
type doLineItem struct {
	Name   string `json:"name"`
	Amount string `json:"amount"`
}

type doInvoiceSummary struct {
	InvoiceUUID    string        `json:"invoice_uuid"`
	BillingPeriod  string        `json:"billing_period"`
	Amount         string        `json:"amount"` // amount due AFTER credits (often 0)
	ProductCharges doSummaryItem `json:"product_charges"`
	Overages       doSummaryItem `json:"overages"`
}

// grossSpend returns the usage cost before credits/discounts: product charges
// plus any overages. This matches the sheet's "Billing Monthly Cost".
func (s doInvoiceSummary) grossSpend() float64 {
	var total float64
	for _, v := range []string{s.ProductCharges.Amount, s.Overages.Amount} {
		if v == "" {
			continue
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			total += f
		}
	}
	return total
}

// byProduct returns the gross spend broken down per product, keyed by product
// name. It uses the product_charges line items (and folds overages into an
// "Overages" bucket). Any rounding remainder between Σ items and grossSpend is
// added as "(other)" so the breakdown reconciles exactly with the headline.
func (s doInvoiceSummary) byProduct() map[string]float64 {
	out := map[string]float64{}
	for _, it := range s.ProductCharges.Items {
		name := strings.TrimSpace(it.Name)
		if name == "" {
			name = "Product charges"
		}
		out[name] += parseFloatSafe(it.Amount)
	}
	if ov := parseFloatSafe(s.Overages.Amount); ov != 0 {
		out["Overages"] += ov
	}
	if len(out) == 0 {
		return nil // no line-item detail available
	}
	var sum float64
	for _, v := range out {
		sum += v
	}
	if rem := s.grossSpend() - sum; rem > 0.005 || rem < -0.005 {
		out["(other)"] += rem
	}
	return out
}

// CollectDO pulls monthly invoices from the DigitalOcean billing API and maps
// each to a model.DailySpend dated to the first day of its invoice period,
// using the invoice summary's product charges (spend before credits/discounts).
// DigitalOcean bills monthly, so there is one record per month (no daily data).
func CollectDO(ctx context.Context, cfg DOConfig) ([]model.DailySpend, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("DigitalOcean API token is required")
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	url := doAPIBase + "/customers/my/invoices?per_page=200"
	var out []model.DailySpend
	previewDone := false

	for url != "" {
		var resp doInvoicesResponse
		if err := doGet(ctx, client, cfg.Token, url, &resp); err != nil {
			return nil, err
		}
		for _, inv := range resp.Invoices {
			recs, err := invoiceToSpend(ctx, client, cfg.Token, inv)
			if err != nil {
				return nil, err
			}
			out = append(out, recs...)
		}
		if cfg.IncludePreview && !previewDone && resp.InvoicePreview.InvoicePeriod != "" {
			if recs, err := invoiceToSpend(ctx, client, cfg.Token, resp.InvoicePreview); err == nil {
				out = append(out, recs...)
			}
			previewDone = true
		}
		url = resp.Links.Pages.Next
	}
	return out, nil
}

// invoiceToSpend resolves an invoice to its gross monthly spend, broken down per
// product (Droplets, Spaces, …) from the invoice summary's line items. Falls
// back to a single record (service unset) when no summary/line items are
// available (e.g. the current preview). Every record is dated to the first of
// the invoice period; the per-product amounts sum to the gross monthly spend.
func invoiceToSpend(ctx context.Context, client *http.Client, token string, inv doInvoice) ([]model.DailySpend, error) {
	if inv.InvoicePeriod == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01", strings.TrimSpace(inv.InvoicePeriod))
	if err != nil {
		return nil, nil
	}
	date := model.NewDate(t)

	// No uuid (e.g. the preview): fall back to the net invoice amount, no split.
	if inv.InvoiceUUID == "" {
		return []model.DailySpend{{
			Provider: model.ProviderDigitalOcean,
			Date:     date,
			Amount:   parseFloatSafe(inv.Amount),
			Currency: "USD",
		}}, nil
	}

	var sum doInvoiceSummary
	surl := doAPIBase + "/customers/my/invoices/" + inv.InvoiceUUID + "/summary"
	if err := doGet(ctx, client, token, surl, &sum); err != nil {
		return nil, err
	}

	byProduct := sum.byProduct()
	if len(byProduct) == 0 {
		// No line-item detail: one record with the gross monthly spend.
		return []model.DailySpend{{
			Provider: model.ProviderDigitalOcean,
			Date:     date,
			Amount:   sum.grossSpend(),
			Currency: "USD",
		}}, nil
	}

	out := make([]model.DailySpend, 0, len(byProduct))
	for name, amt := range byProduct {
		if amt == 0 {
			continue
		}
		out = append(out, model.DailySpend{
			Provider: model.ProviderDigitalOcean,
			Date:     date,
			Amount:   amt,
			Currency: "USD",
			Service:  name,
		})
	}
	return out, nil
}

func parseFloatSafe(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

func doGet(ctx context.Context, client *http.Client, token, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("digitalocean API 401: invalid or unscoped token")
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("digitalocean API %s for %s", resp.Status, url)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}


