package nodehttp

const (
	OperationRead      = "read"
	OperationBroadcast = "broadcast"

	FailureTransport        = "transport"
	FailureResponseRead     = "response_read"
	FailureResponseInvalid  = "response_invalid"
	FailureResponseTooLarge = "response_too_large"
	FailureHTTPRetryable    = "http_retryable"
	FailureRequestRejected  = "request_rejected"
)

// Observer 接收不包含节点 URL 和凭据的固定基数故障切换事件。
type Observer interface {
	Configured(operation string, endpointCount int)
	Failed(operation string, endpointIndex int, reason string)
	Switched(operation string, previousEndpointIndex, currentEndpointIndex int)
	Exhausted(operation string)
}

type noopObserver struct{}

func (noopObserver) Configured(string, int)     {}
func (noopObserver) Failed(string, int, string) {}
func (noopObserver) Switched(string, int, int)  {}
func (noopObserver) Exhausted(string)           {}

func observerOrNoop(observer Observer) Observer {
	if observer == nil {
		return noopObserver{}
	}
	return observer
}
