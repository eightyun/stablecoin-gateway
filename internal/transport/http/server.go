package http

import (
	"encoding/json"
	"log/slog"
	stdhttp "net/http"
)

// NewHandler 创建 HTTP 路由。
func NewHandler() stdhttp.Handler {
	mux := stdhttp.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	return mux
}

func health(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(stdhttp.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		slog.Error("写入健康检查响应失败", "error", err)
	}
}
