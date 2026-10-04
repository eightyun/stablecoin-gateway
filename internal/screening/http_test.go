package screening

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const screeningTestPayoutID = "123e4567-e89b-42d3-a456-426614174000"

func TestHTTPProviderScreensAddress(t *testing.T) {
	now := time.Now().UTC()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/adapter/v1/address-screenings" ||
			request.Header.Get("Authorization") != "Bearer secret" ||
			request.Header.Get("Idempotency-Key") != screeningTestPayoutID+":1" {
			t.Fatalf("请求 = %s %s headers=%v", request.Method, request.URL.Path, request.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["direction"] != "outbound" ||
			body["payout_id"] != screeningTestPayoutID || body["source_address"] != nil ||
			body["destination_address"] != "410000000000000000000000000000000000000000" {
			t.Fatalf("请求体 = %+v, %v", body, err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"decision": "allow", "reason_codes": []string{"low_risk"},
			"provider_reference": "provider-123", "checked_at": now,
			"valid_until": now.Add(time.Hour),
		})
	}))
	defer server.Close()
	provider, err := NewHTTPProvider(testHTTPConfig(server.URL+"/adapter"), server.Client())
	if err != nil {
		t.Fatalf("NewHTTPProvider() error = %v", err)
	}
	result, err := provider.Screen(context.Background(), validScreeningRequest())
	if err != nil || result.Provider != "test-provider" || result.Decision != DecisionAllow ||
		result.ProviderReference != "provider-123" || len(result.ResponseHash) != 64 ||
		len(result.ReasonCodes) != 1 || result.ReasonCodes[0] != "low_risk" {
		t.Fatalf("Screen() = %+v, %v", result, err)
	}
}

func TestHTTPProviderScreensInboundAddress(t *testing.T) {
	now := time.Now().UTC()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["direction"] != "inbound" ||
			body["deposit_screening_id"] != screeningTestPayoutID || body["payout_id"] != nil ||
			body["source_address"] != "411111111111111111111111111111111111111111" {
			t.Fatalf("请求体 = %+v, %v", body, err)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"decision": "allow", "reason_codes": []string{"low_risk"},
			"provider_reference": "inbound-123", "checked_at": now,
			"valid_until": now.Add(time.Hour),
		})
	}))
	defer server.Close()
	provider, err := NewHTTPProvider(testHTTPConfig(server.URL), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	request := validScreeningRequest()
	request.Direction = DirectionInbound
	request.PayoutID = ""
	request.DepositScreeningID = screeningTestPayoutID
	request.SourceAddress = "411111111111111111111111111111111111111111"
	if result, err := provider.Screen(context.Background(), request); err != nil || result.Decision != DecisionAllow {
		t.Fatalf("Screen() = %+v, %v", result, err)
	}
}

func TestHTTPProviderRejectsInvalidConfigurationAndRequest(t *testing.T) {
	config := testHTTPConfig("http://provider.example")
	if _, err := NewHTTPProvider(config, nil); !errors.Is(err, ErrInvalidProviderConfig) {
		t.Fatalf("NewHTTPProvider() error = %v", err)
	}
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	provider, err := NewHTTPProvider(testHTTPConfig(server.URL), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Screen(context.Background(), Request{}); !errors.Is(err, ErrInvalidProviderRequest) {
		t.Fatalf("Screen() error = %v", err)
	}
}

func TestHTTPProviderRejectsExpiredAndUnknownResponse(t *testing.T) {
	for _, test := range []struct {
		name     string
		response map[string]any
	}{
		{
			name: "过期结果",
			response: map[string]any{
				"decision": "allow", "reason_codes": []string{}, "provider_reference": "expired",
				"checked_at":  time.Now().UTC().Add(-2 * time.Hour),
				"valid_until": time.Now().UTC().Add(-time.Hour),
			},
		},
		{
			name: "未知字段",
			response: map[string]any{
				"decision": "allow", "reason_codes": []string{}, "provider_reference": "unknown",
				"checked_at": time.Now().UTC(), "valid_until": time.Now().UTC().Add(time.Hour),
				"unexpected": true,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(writer).Encode(test.response)
			}))
			defer server.Close()
			provider, err := NewHTTPProvider(testHTTPConfig(server.URL), server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.Screen(context.Background(), validScreeningRequest()); !errors.Is(err, ErrInvalidProviderResponse) {
				t.Fatalf("Screen() error = %v", err)
			}
		})
	}
}

func TestHTTPProviderRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("x", 1025)))
	}))
	defer server.Close()
	config := testHTTPConfig(server.URL)
	config.MaxResponseBytes = 1024
	provider, err := NewHTTPProvider(config, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Screen(context.Background(), validScreeningRequest()); !errors.Is(err, ErrProviderResponseTooLarge) {
		t.Fatalf("Screen() error = %v", err)
	}
}

func testHTTPConfig(baseURL string) HTTPConfig {
	return HTTPConfig{
		BaseURL: baseURL, BearerToken: "secret", ProviderName: "test-provider",
		RequestTimeout: time.Second, MaxResultValidity: 24 * time.Hour,
	}
}

func validScreeningRequest() Request {
	return Request{
		RequestID: screeningTestPayoutID + ":1", Direction: DirectionOutbound,
		PayoutID: screeningTestPayoutID,
		Network:  "tron-nile", AssetID: "usdt-tron-nile",
		ContractAddress:    "41eca9bc828a3005b9a3b909f2cc5c2a54794de05f",
		DestinationAddress: "410000000000000000000000000000000000000000", Amount: "1000000",
	}
}
