package webhook

import (
	"errors"
	"testing"
)

func TestNormalizeEndpointURL(t *testing.T) {
	value, err := normalizeEndpointURL(" https://example.com/hooks/gateway?tenant=1 ")
	if err != nil || value != "https://example.com/hooks/gateway?tenant=1" {
		t.Fatalf("normalizeEndpointURL() = %q, %v", value, err)
	}
	for _, value := range []string{
		"http://example.com/hook", "https://user:password@example.com/hook",
		"https://example.com/hook#fragment", "not-a-url",
	} {
		if _, err := normalizeEndpointURL(value); !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("normalizeEndpointURL(%q) error = %v", value, err)
		}
	}
}
