package nodehttp

import (
	"net/url"
	"strings"
)

const maxNodeEndpoints = 8

func parseBaseURLs(raw string) ([]*url.URL, error) {
	parts := strings.Split(raw, ",")
	if len(parts) == 0 || len(parts) > maxNodeEndpoints {
		return nil, ErrInvalidConfig
	}
	endpoints := make([]*url.URL, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, ErrInvalidConfig
		}
		parsed, err := url.Parse(part)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
			return nil, ErrInvalidConfig
		}
		parsed.Path = strings.TrimRight(parsed.Path, "/")
		parsed.RawPath = strings.TrimRight(parsed.RawPath, "/")
		normalized := parsed.String()
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		endpoints = append(endpoints, parsed)
	}
	if len(endpoints) == 0 {
		return nil, ErrInvalidConfig
	}
	return endpoints, nil
}

func retryableHTTPStatus(status int) bool {
	return status == 408 || status == 425 || status == 429 || status >= 500
}
