package wallet

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeRegisterRequest(t *testing.T) {
	request, err := normalizeRegisterRequest(RegisterRequest{
		AssetID: " usdt-tron-nile ", Address: "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb",
		Role: RoleHot, Actor: " ops@example.com ", Reason: " primary payout wallet ",
	})
	if err != nil || request.AssetID != "usdt-tron-nile" ||
		request.Address != "410000000000000000000000000000000000000000" ||
		request.Actor != "ops@example.com" || request.Reason != "primary payout wallet" {
		t.Fatalf("normalizeRegisterRequest() = %+v, %v", request, err)
	}
}

func TestNormalizeRegisterRequestRejectsInvalidInput(t *testing.T) {
	valid := RegisterRequest{
		AssetID: "usdt-tron-nile", Address: "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb",
		Role: RoleHot, Actor: "ops@example.com", Reason: "primary payout wallet",
	}
	tests := []struct {
		name   string
		mutate func(*RegisterRequest)
	}{
		{name: "充值角色", mutate: func(request *RegisterRequest) { request.Role = "deposit" }},
		{name: "地址无效", mutate: func(request *RegisterRequest) { request.Address = "invalid" }},
		{name: "操作人为空", mutate: func(request *RegisterRequest) { request.Actor = "" }},
		{name: "原因过长", mutate: func(request *RegisterRequest) { request.Reason = strings.Repeat("a", 513) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if _, err := normalizeRegisterRequest(request); !errors.Is(err, ErrInvalidRegistration) {
				t.Fatalf("normalizeRegisterRequest() error = %v", err)
			}
		})
	}
}
