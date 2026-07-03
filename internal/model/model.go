// Package model defines the normalized domain types shared across the
// importer, calculation, report and storage layers.
package model

import (
	"fmt"
	"time"
)

// Provider is a supported cloud billing source.
type Provider string

const (
	ProviderAWS          Provider = "aws"
	ProviderGCP          Provider = "gcp"
	ProviderAzure        Provider = "azure"
	ProviderDigitalOcean Provider = "digitalocean"
	ProviderFastly       Provider = "fastly"
	ProviderIBMPower     Provider = "ibm-power"
	ProviderIBMZ         Provider = "ibm-z"
)

// DisplayName returns a human readable provider label used in reports.
func (p Provider) DisplayName() string {
	switch p {
	case ProviderAWS:
		return "AWS"
	case ProviderGCP:
		return "GCP"
	case ProviderAzure:
		return "Azure"
	case ProviderDigitalOcean:
		return "DigitalOcean"
	case ProviderFastly:
		return "Fastly"
	case ProviderIBMPower:
		return "IBM Power"
	case ProviderIBMZ:
		return "IBM Z (s390x)"
	default:
		return string(p)
	}
}

// AllProviders lists every provider in the canonical report order.
var AllProviders = []Provider{
	ProviderAWS,
	ProviderAzure,
	ProviderDigitalOcean,
	ProviderFastly,
	ProviderGCP,
	ProviderIBMPower,
	ProviderIBMZ,
}

// ParseProvider validates and normalizes a provider identifier.
func ParseProvider(s string) (Provider, error) {
	p := Provider(s)
	for _, known := range AllProviders {
		if p == known {
			return p, nil
		}
	}
	return "", fmt.Errorf("unknown provider %q", s)
}

// Date is a calendar day (no time-of-day component) serialized as YYYY-MM-DD.
type Date struct {
	time.Time
}

const dateLayout = "2006-01-02"

// NewDate builds a Date truncated to the day in UTC.
func NewDate(t time.Time) Date {
	y, m, d := t.Date()
	return Date{time.Date(y, m, d, 0, 0, 0, 0, time.UTC)}
}

// ParseDate parses a YYYY-MM-DD string.
func ParseDate(s string) (Date, error) {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return Date{}, err
	}
	return Date{t}, nil
}

func (d Date) String() string { return d.Format(dateLayout) }

// MarshalJSON renders the date as YYYY-MM-DD.
func (d Date) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.Format(dateLayout) + `"`), nil
}

// UnmarshalJSON parses a YYYY-MM-DD string.
func (d *Date) UnmarshalJSON(b []byte) error {
	if len(b) < 2 {
		return fmt.Errorf("invalid date %q", string(b))
	}
	t, err := time.Parse(dateLayout, string(b[1:len(b)-1]))
	if err != nil {
		return err
	}
	d.Time = t
	return nil
}

// DailySpend is a single normalized cost record for one provider on one day.
// Optional Service allows service-level breakdowns while still rolling up to a
// per-day total.
type DailySpend struct {
	Provider Provider `json:"provider"`
	Date     Date     `json:"date"`
	Amount   float64  `json:"amount"`
	Currency string   `json:"currency"`
	Service  string   `json:"service,omitempty"`
}

// BudgetConfig captures the annual budget and alerting configuration for a
// single provider.
type BudgetConfig struct {
	Provider       Provider `json:"provider"`
	Year           int      `json:"year"`
	AnnualBudget   float64  `json:"annualBudget"`
	Currency       string   `json:"currency"`
	AlertThreshold float64  `json:"alertThreshold"` // e.g. 0.9 == warn at 90%
}




