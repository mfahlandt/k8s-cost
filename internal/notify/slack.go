// Package notify sends budget alerts to Slack via an incoming webhook.
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/kubernetes/k8s-cost/internal/calc"
)

// Slack posts messages to a Slack incoming webhook URL.
type Slack struct {
	WebhookURL string
	Client     *http.Client
}

// NewSlack builds a Slack notifier with a sane default HTTP client.
func NewSlack(webhookURL string) *Slack {
	return &Slack{WebhookURL: webhookURL, Client: &http.Client{Timeout: 10 * time.Second}}
}

type slackPayload struct {
	Text string `json:"text"`
}

// PostBudgetAlerts sends one message summarizing every provider currently in an
// alert state. It is a no-op when there are no alerts or no webhook configured.
func (s *Slack) PostBudgetAlerts(metrics []calc.Metrics) error {
	if s.WebhookURL == "" {
		return nil
	}
	var msg bytes.Buffer
	count := 0
	fmt.Fprintf(&msg, ":rotating_light: *Cloud budget alerts*\n")
	for _, m := range metrics {
		if m.Budget == nil || !m.Budget.BudgetAlert {
			continue
		}
		count++
		fmt.Fprintf(&msg, "• *%s*: projected %.0f / budget %.0f (%.1f%% utilization)\n",
			m.Provider.DisplayName(),
			m.Budget.ProjectedYearTotal,
			m.Budget.AnnualBudget,
			m.Budget.BudgetUtilization*100,
		)
	}
	if count == 0 {
		return nil
	}
	return s.post(slackPayload{Text: msg.String()})
}

func (s *Slack) post(p slackPayload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	resp, err := s.Client.Post(s.WebhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook returned %s", resp.Status)
	}
	return nil
}

