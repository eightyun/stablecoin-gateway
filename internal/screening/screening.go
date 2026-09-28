// Package screening 提供与具体供应商解耦的链上地址风险筛查能力。
package screening

import (
	"context"
	"time"
)

const (
	DecisionAllow  = "allow"
	DecisionDeny   = "deny"
	DecisionReview = "review"
)

// Request 是绑定到单次租约的出款地址筛查请求。
type Request struct {
	RequestID          string
	PayoutID           string
	Network            string
	AssetID            string
	ContractAddress    string
	DestinationAddress string
	Amount             string
}

// Result 是供应商响应经本地严格校验后的规范结果。
type Result struct {
	Provider          string
	Decision          string
	ReasonCodes       []string
	ProviderReference string
	ResponseHash      string
	CheckedAt         time.Time
	ValidUntil        time.Time
}

// Provider 对出款目的地址执行风险筛查。
type Provider interface {
	Screen(context.Context, Request) (Result, error)
}
