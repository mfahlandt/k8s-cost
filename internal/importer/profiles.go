package importer

import (
	"io"

	"github.com/kubernetes/k8s-cost/internal/model"
)

// csvImporter adapts a CSVProfile to the Importer interface.
type csvImporter struct{ profile CSVProfile }

func (c csvImporter) Provider() model.Provider { return c.profile.Provider }
func (c csvImporter) Parse(r io.Reader, opts Options) ([]model.DailySpend, error) {
	return parseCSV(r, c.profile, opts)
}

func init() {
	// AWS Cost Explorer / CUR "unblended cost" style export. Cost Explorer's
	// "Group by: Service, Granularity: Daily" CSV has a date column plus one
	// amount column per service; the more common long form has explicit
	// service/date/amount columns which we target here.
	register("aws-csv", csvImporter{CSVProfile{
		Provider:        model.ProviderAWS,
		DateAliases:     []string{"date", "usage date", "lineItem/UsageStartDate", "bill/BillingPeriodStartDate", "day"},
		DateLayouts:     []string{"2006-01-02", "01/02/2006", "2006/01/02", "2006-01-02T15:04:05Z"},
		AmountAliases:   []string{"cost", "amount", "unblended cost", "lineItem/UnblendedCost", "total costs($)", "total"},
		ServiceAliases:  []string{"service", "product name", "lineItem/ProductCode", "product/ProductName"},
		CurrencyAliases: []string{"currency", "currency code", "lineItem/CurrencyCode"},
	}})

	// GCP BigQuery billing export, either the raw daily resource export or the
	// service-level monthly breakdown produced by the provided SQL query
	// (columns: "Service Description", "Cost", ..., "Subtotal"). The monthly
	// breakdown has no date column, so callers pass --period YYYY-MM.
	register("gcp-csv", csvImporter{CSVProfile{
		Provider:        model.ProviderGCP,
		DateAliases:     []string{"date", "usage_start_time", "usage date", "day"},
		DateLayouts:     []string{"2006-01-02", "2006/01/02", "2006-01-02T15:04:05Z07:00"},
		AmountAliases:   []string{"subtotal", "cost", "amount", "total"},
		ServiceAliases:  []string{"service description", "service.description", "service", "sku description"},
		CurrencyAliases: []string{"currency"},
	}})

	// DigitalOcean billing history CSV export.
	register("digitalocean-csv", csvImporter{CSVProfile{
		Provider:        model.ProviderDigitalOcean,
		DateAliases:     []string{"date", "start", "period"},
		DateLayouts:     []string{"2006-01-02", "01/02/2006", "2006-01-02T15:04:05Z"},
		AmountAliases:   []string{"amount", "cost", "usd", "total"},
		ServiceAliases:  []string{"product", "description", "name", "category"},
		CurrencyAliases: []string{"currency"},
	}})
}

