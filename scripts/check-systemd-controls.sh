#!/usr/bin/env bash
set -euo pipefail

# 仅在一次性 CI runner 中验证真实 systemd 编排，不连接数据库或钱包。
if [[ "${GITHUB_ACTIONS:-}" != true || "$(id -u)" != 0 ]]; then
  echo 'This check requires root on an ephemeral GitHub Actions runner.' >&2
  exit 1
fi
systemctl show-environment > /dev/null
repository="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixture_dir="$(mktemp -d /run/gateway-controls-check.XXXXXX)"
unit_prefix="stablecoin-gateway-controls-ci-$$"
service_file="/run/systemd/system/$unit_prefix@.service"
timer_file="/run/systemd/system/$unit_prefix@.timer"
cases=(success failure mismatch timeout timer timer-failure)

cleanup() {
  for test_case in "${cases[@]}"; do
    systemctl stop "$unit_prefix@$test_case.timer" "$unit_prefix@$test_case.service" 2>/dev/null || true
    systemctl reset-failed "$unit_prefix@$test_case.service" 2>/dev/null || true
  done
  # 仅清理由本轮创建的已知文件；保留 journal 供失败排查。
  unlink "$service_file" 2>/dev/null || true
  unlink "$timer_file" 2>/dev/null || true
  systemctl daemon-reload
  for test_case in "${cases[@]}"; do unlink "$fixture_dir/controls-$test_case.env" 2>/dev/null || true; done
  unlink "$fixture_dir/gateway-wallet-snapshot" 2>/dev/null || true
  unlink "$fixture_dir/gateway-reconcile" 2>/dev/null || true
  rmdir "$fixture_dir" 2>/dev/null || true
}
if [[ -e "$service_file" || -e "$timer_file" ]]; then
  rmdir "$fixture_dir"
  echo 'Refusing to replace existing CI units.' >&2
  exit 1
fi
trap cleanup EXIT
chmod 0755 "$fixture_dir"
install -m 0755 "$repository/scripts/testdata/controls/gateway-wallet-snapshot" "$fixture_dir/"
install -m 0755 "$repository/scripts/testdata/controls/gateway-reconcile" "$fixture_dir/"

# 仅替换安装位置、运行身份和测试超时，保留模板的资产绑定、串行和安全限制。
sed \
  -e "s|^WorkingDirectory=.*|WorkingDirectory=$fixture_dir|" \
  -e 's|^User=.*|User=nobody|' \
  -e 's|^Group=.*|Group=nogroup|' \
  -e "s|^EnvironmentFile=.*|EnvironmentFile=$fixture_dir/controls-%i.env|" \
  -e "s|/opt/stablecoin-gateway/current/bin/|$fixture_dir/|g" \
  -e 's|^TimeoutStartSec=.*|TimeoutStartSec=3s|' \
  "$repository/configs/systemd/stablecoin-gateway-controls@.service" > "$service_file"
sed \
  -e 's|^OnBootSec=.*|OnBootSec=200ms|' \
  -e 's|^OnUnitInactiveSec=.*|OnUnitInactiveSec=200ms|' \
  -e 's|^RandomizedDelaySec=.*|RandomizedDelaySec=0|' \
  -e 's|^AccuracySec=.*|AccuracySec=1ms|' \
  -e "s|^Unit=.*|Unit=$unit_prefix@%i.service|" \
  "$repository/configs/systemd/stablecoin-gateway-controls@.timer" > "$timer_file"
chmod 0644 "$service_file" "$timer_file"
for test_case in "${cases[@]}"; do
  printf '# Temporary CI fixture\n' > "$fixture_dir/controls-$test_case.env"
  chmod 0600 "$fixture_dir/controls-$test_case.env"
done
printf 'CONTROL_TEST_SNAPSHOT_EXIT=17\n' >> "$fixture_dir/controls-failure.env"
printf 'GATEWAY_WALLET_SNAPSHOT_ASSET_ID=wrong-asset\n' >> "$fixture_dir/controls-mismatch.env"
printf 'CONTROL_TEST_SNAPSHOT_DELAY=30\n' >> "$fixture_dir/controls-timeout.env"
printf 'CONTROL_TEST_SNAPSHOT_DELAY=0.5\nCONTROL_TEST_RECONCILE_DELAY=0.5\n' >> "$fixture_dir/controls-timer.env"
printf 'CONTROL_TEST_SNAPSHOT_DELAY=0.2\nCONTROL_TEST_SNAPSHOT_EXIT=17\n' >> "$fixture_dir/controls-timer-failure.env"
systemctl daemon-reload
systemd-analyze verify "$unit_prefix@success.service" "$unit_prefix@timer.timer"

phases() {
  journalctl --sync
  journalctl --unit="$unit_prefix@$1.service" --no-pager --output=cat | sed -n '/^phase=/p'
}

systemctl start "$unit_prefix@success.service"
[[ "$(systemctl show "$unit_prefix@success.service" --property=Result --value)" == success ]]
[[ "$(phases success)" == $'phase=snapshot-start\nphase=snapshot-complete\nphase=reconcile-start\nphase=reconcile-complete' ]]
echo 'PASS: snapshot and reconciliation execute in order'

for test_case in failure mismatch timeout; do
  if systemctl start "$unit_prefix@$test_case.service"; then
    echo "Unexpected success: $test_case" >&2
    exit 1
  fi
  case "$test_case" in
    failure)
      [[ "$(phases failure)" == $'phase=snapshot-start\nphase=snapshot-failed' ]]
      [[ "$(systemctl show "$unit_prefix@failure.service" --property=ExecMainStatus --value)" == 17 ]]
      ;;
    mismatch) [[ -z "$(phases mismatch)" ]] ;;
    timeout)
      [[ "$(systemctl show "$unit_prefix@timeout.service" --property=Result --value)" == timeout ]]
      [[ "$(phases timeout)" == 'phase=snapshot-start' ]]
      ;;
  esac
  echo "PASS: $test_case fails closed without reconciliation"
done

systemctl start "$unit_prefix@timer.timer"
completed=0
for attempt in {1..120}; do
  completed="$(phases timer | awk '/^phase=reconcile-complete$/ {n++} END {print n+0}')"
  if (( completed >= 2 )); then break; fi
  sleep 0.1
done
(( completed >= 2 ))
systemctl stop "$unit_prefix@timer.timer"
for attempt in {1..60}; do
  state="$(systemctl show "$unit_prefix@timer.service" --property=ActiveState --value)"
  if [[ "$state" == inactive ]]; then break; fi
  sleep 0.1
done
[[ "$state" == inactive ]]
phases timer | awk '
  /^phase=snapshot-start$/ {if (state != 0) exit 1; state=1; next}
  /^phase=snapshot-complete$/ {if (state != 1) exit 1; state=2; next}
  /^phase=reconcile-start$/ {if (state != 2) exit 1; state=3; next}
  /^phase=reconcile-complete$/ {if (state != 3) exit 1; state=0; completed++; next}
  END {if (state != 0 || completed < 2) exit 1}
'
echo 'PASS: timer repeats completed cycles without overlap'

systemctl start "$unit_prefix@timer-failure.timer"
failed=0
for attempt in {1..100}; do
  failed="$(phases timer-failure | awk '/^phase=snapshot-failed$/ {n++} END {print n+0}')"
  if (( failed >= 2 )); then break; fi
  sleep 0.1
done
systemctl stop "$unit_prefix@timer-failure.timer"
(( failed >= 2 ))
if phases timer-failure | grep -q '^phase=reconcile-'; then
  echo 'Reconciliation unexpectedly ran after a timed snapshot failure.' >&2
  exit 1
fi
echo 'PASS: timer retries failed snapshots without running reconciliation'
