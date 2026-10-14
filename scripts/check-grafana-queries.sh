#!/usr/bin/env bash
set -euo pipefail

# 将 Grafana 变量展开为固定验收值，再由现有 Prometheus 工具解析查询。
dashboard_file="${1:-configs/grafana/dashboards/stablecoin-gateway.json}"
query_file="$(mktemp "${TMPDIR:-/tmp}/gateway-dashboard-queries.XXXXXX")"
trap 'rm -f "$query_file"' EXIT

jq '
  def expand:
    gsub("\\$__rate_interval"; "5m") |
    gsub("\\$monitor_job"; "stablecoin-gateway-monitor") |
    gsub("\\$monitor_instance"; ".+") |
    gsub("\\$worker_job"; "stablecoin-gateway-.+") |
    gsub("\\$monitor_max_age"; "120") |
    if contains("$") then error("查询包含未处理的 Grafana 变量") else . end;
  {groups: [{name: "stablecoin-gateway-dashboard", rules: [
    .panels[] as $panel | $panel.targets[]? |
    {record: ("gateway_dashboard_panel_" + ($panel.id | tostring) + "_" + .refId), expr: (.expr | expand)}
  ]}]} |
  if (.groups[0].rules | length) == 0 then error("仪表盘没有 PromQL 查询") else . end
' "$dashboard_file" > "$query_file"

# 临时文件只包含公开的指标查询，允许容器内非 root 用户读取。
chmod 644 "$query_file"
docker run --rm --entrypoint=promtool \
  -v "$query_file:/work/queries.json:ro" \
  prom/prometheus@sha256:63805ebb8d2b3920190daf1cb14a60871b16fd38bed42b857a3182bc621f4996 \
  check rules /work/queries.json

# 使用仪表盘本身的查询验证新鲜、过期、失联及零时间戳行为。
test_file="$(cd "$(dirname "$dashboard_file")/.." && pwd)/queries.test.json"
docker run --rm --entrypoint=promtool \
  -v "$query_file:/work/queries.json:ro" \
  -v "$test_file:/work/queries.test.json:ro" \
  prom/prometheus@sha256:63805ebb8d2b3920190daf1cb14a60871b16fd38bed42b857a3182bc621f4996 \
  test rules /work/queries.test.json
