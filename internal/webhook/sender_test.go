package webhook

import (
	"context"
	"crypto/hmac"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHTTPSenderSignsAndSendsEnvelope(t *testing.T) {
	body := []byte(`{"id":"event-1"}`)
	secret := []byte("whsec_12345678901234567890")
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		timestamp, err := strconv.ParseInt(request.Header.Get(HeaderEventTimestamp), 10, 64)
		if err != nil || request.Header.Get(HeaderEventID) != "event-1" || request.Header.Get(HeaderEventType) != "deposit.confirmed" ||
			!hmac.Equal([]byte(request.Header.Get(HeaderSignature)), []byte(sign(timestamp, "event-1", body, secret))) {
			t.Errorf("Webhook 请求头无效: %+v", request.Header)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	sender := &HTTPSender{client: server.Client(), maxResponseBodyBytes: 1024}
	result := sender.Send(context.Background(), DeliveryRequest{
		EventID: "event-1", Topic: "deposit.confirmed", URL: server.URL, Body: body, Secret: secret,
	})
	if result.Err != nil || result.ResponseStatus == nil || *result.ResponseStatus != http.StatusNoContent {
		t.Fatalf("Send() = %+v", result)
	}
}

func TestHTTPSenderBlocksPrivateNetworksByDefault(t *testing.T) {
	sender, err := NewHTTPSender(HTTPSenderConfig{Timeout: time.Second, MaxResponseBodyBytes: 1024})
	if err != nil {
		t.Fatalf("NewHTTPSender() error = %v", err)
	}
	result := sender.Send(context.Background(), DeliveryRequest{
		EventID: "event", Topic: "event", URL: "https://127.0.0.1:1/hook",
		Body: []byte(`{}`), Secret: []byte("1234567890123456"),
	})
	if result.Err == nil || !strings.Contains(result.Err.Error(), "受保护网络") {
		t.Fatalf("Send() error = %v", result.Err)
	}
}

func TestBlockedAddressKeepsNonUnicastProtected(t *testing.T) {
	if !blockedAddress(netip.MustParseAddr("10.0.0.1"), false) || blockedAddress(netip.MustParseAddr("10.0.0.1"), true) {
		t.Fatal("私网开关行为无效")
	}
	if !blockedAddress(netip.MustParseAddr("127.0.0.1"), true) ||
		!blockedAddress(netip.MustParseAddr("100.64.0.1"), false) ||
		blockedAddress(netip.MustParseAddr("8.8.8.8"), false) {
		t.Fatal("受保护网络判断无效")
	}
}
