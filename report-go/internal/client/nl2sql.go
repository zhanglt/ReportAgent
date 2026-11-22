package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/zhanglt/ReportAgent/report-go/internal/querysession"
)

type NL2SQL struct {
	URL  string
	HTTP *http.Client
}

func (n NL2SQL) Query(ctx context.Context, question string) (string, error) {
	body, err := json.Marshal(map[string]string{"query": question})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	if id := querysession.ID(ctx); id != "" {
		request.Header.Set(querysession.Header, id)
	}
	response, err := n.HTTP.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("NL2SQL HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil {
		return "", err
	}
	if len(data) > 8<<20 {
		return "", fmt.Errorf("NL2SQL response exceeds 8 MiB")
	}
	var output struct {
		Text *string `json:"output"`
	}
	if err := json.Unmarshal(data, &output); err != nil {
		return "", fmt.Errorf("invalid NL2SQL response: %w", err)
	}
	if output.Text == nil {
		return "", fmt.Errorf("NL2SQL JSON has no 'output'")
	}
	return *output.Text, nil
}
