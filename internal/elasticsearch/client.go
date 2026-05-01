package elasticsearch

import (
	"fmt"
	"io"
	"strings"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
	"github.com/mfranz/elastic-security-mcp/internal/util"
)

const maxLoggedBodyChars = 2048

func NewClient(url, apiKey string) (*elasticsearch.Client, error) {
	cfg := elasticsearch.Config{
		Addresses: []string{url},
		APIKey:    apiKey,
	}
	return elasticsearch.NewClient(cfg)
}

func HttpError(method string, res *esapi.Response) error {
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("%s failed with status %s: reading error body: %w", method, res.Status(), err)
	}
	return fmt.Errorf("%s failed with status %s: %s", method, res.Status(), util.TruncateForLog(strings.TrimSpace(string(body)), maxLoggedBodyChars))
}
