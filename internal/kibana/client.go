package kibana

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Client struct {
	BaseURL    string
	Username   string
	Password   string
	APIKey     string
	Space      string
	HTTPClient *http.Client
}

// NewClient creates a new Client configured to communicate with a Kibana instance.
func NewClient(url, username, password, apiKey string) (*Client, error) {
	if url == "" {
		return nil, fmt.Errorf("kibana URL is required")
	}
	url = strings.TrimSuffix(url, "/")

	if password != "" && username == "" {
		username = "elastic"
	}

	space := os.Getenv("KIBANA_SPACE")

	return &Client{
		BaseURL:    url,
		Username:   username,
		Password:   password,
		APIKey:     apiKey,
		Space:      space,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// pathWithSpace prefixes the path with the Kibana space identifier if configured,
// except for global endpoints like spaces management or status.
func (c *Client) pathWithSpace(path string) string {
	if c.Space == "" || c.Space == "default" {
		return path
	}
	if strings.HasPrefix(path, "/s/") {
		return path
	}
	// Exclude global endpoints
	if strings.HasPrefix(path, "/api/spaces/") || path == "/api/spaces" || strings.HasPrefix(path, "/api/status") {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "/s/" + c.Space + path
}

// DoRequest performs an HTTP request to Kibana, injecting XSRF and authorization headers.
func (c *Client) DoRequest(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		switch v := body.(type) {
		case string:
			bodyReader = strings.NewReader(v)
		case []byte:
			bodyReader = bytes.NewReader(v)
		default:
			jsonData, err := json.Marshal(body)
			if err != nil {
				return nil, 0, fmt.Errorf("failed to marshal request body: %w", err)
			}
			bodyReader = bytes.NewReader(jsonData)
		}
	}

	fullPath := c.pathWithSpace(path)
	url := c.BaseURL + fullPath
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// Kibana XSRF check: required for all non-GET/HEAD requests
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("kbn-xsrf", "true")
	}

	// Authentication
	if c.APIKey != "" {
		req.Header.Set("Authorization", "ApiKey "+c.APIKey)
	} else if c.Password != "" {
		req.Header.Set("Authorization", "Basic "+basicAuth(c.Username, c.Password))
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response body: %w", err)
	}

	return respBody, resp.StatusCode, nil
}

func basicAuth(username, password string) string {
	auth := username + ":" + password
	return base64.StdEncoding.EncodeToString([]byte(auth))
}
