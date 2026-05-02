package elasticsearch

import (
	"fmt"
	"io"
	"strings"

	"github.com/elastic/go-elasticsearch/v9"
	"github.com/elastic/go-elasticsearch/v9/esapi"
	"github.com/mfranz/elastic-security-mcp/internal/util"
)

const maxLoggedBodyChars = 2048

type Client struct {
	Raw   *elasticsearch.Client
	Typed *elasticsearch.TypedClient
}

func NewClient(url, apiKey string) (*Client, error) {
	cfg := elasticsearch.Config{
		Addresses: []string{url},
		APIKey:    apiKey,
	}
	raw, err := elasticsearch.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	typed, err := elasticsearch.NewTypedClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{
		Raw:   raw,
		Typed: typed,
	}, nil
}

func HttpError(method string, res *esapi.Response) error {
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("%s failed with status %s: reading error body: %w", method, res.Status(), err)
	}
	return fmt.Errorf("%s failed with status %s: %s", method, res.Status(), util.TruncateForLog(strings.TrimSpace(string(body)), maxLoggedBodyChars))
}
