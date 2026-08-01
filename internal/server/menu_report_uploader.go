package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/menudebug"
)

const maxCollectorResponseBytes = 8 << 10

type menuReportHTTPUploader struct {
	endpoint string
	client   *http.Client
}

// NewMenuReportHTTPUploader creates the outbound-only client used after the
// operator explicitly consents to upload a completed sanitized report.
func NewMenuReportHTTPUploader(endpoint string, client *http.Client) (MenuDebugUploader, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("menu report collector URL is invalid")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return nil, errors.New("menu report collector URL must use HTTPS")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &menuReportHTTPUploader{endpoint: parsed.String(), client: &clientCopy}, nil
}

func (u *menuReportHTTPUploader) Upload(ctx context.Context, report menudebug.Report) error {
	body, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create collector request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "expert-amp-server/"+strings.TrimSpace(report.ServerVersion))

	response, err := u.client.Do(req)
	if err != nil {
		return fmt.Errorf("collector request: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxCollectorResponseBytes))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("collector returned HTTP %d", response.StatusCode)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
