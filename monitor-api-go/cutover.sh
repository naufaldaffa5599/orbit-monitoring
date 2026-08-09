#!/usr/bin/env bash
# One-shot cutover: Python monitor-api -> Go monitor-api, on the same port 8585.
#
#   sudo bash cutover.sh
#
# Installs the Go unit over monitor-api.service, verifies the API actually
# answers on 8585, and only then deletes the Python tree. If verification
# fails, the old unit is restored and nothing is deleted.
set -euo pipefail

GO_DIR=/home/daffa/projects/monitoring/monitor-api-go
PY_DIR=/home/daffa/projects/monitoring/monitor-api
UNIT=/etc/systemd/system/monitor-api.service
UNIT_BAK=/etc/systemd/system/monitor-api.service.python.bak
PORT=8585

[ "$(id -u)" -eq 0 ] || { echo "harus root: sudo bash cutover.sh"; exit 1; }

# ── Preflight ───────────────────────────────────────────────────────────────
[ -x "$GO_DIR/monitor-api" ]   || { echo "FATAL: binary belum dibuild ($GO_DIR/monitor-api). Jalanin ./build.sh dulu."; exit 1; }
[ -f "$GO_DIR/devices.json" ]  || { echo "FATAL: $GO_DIR/devices.json gak ada."; exit 1; }
[ -f "$GO_DIR/deploy/monitor-api.service" ] || { echo "FATAL: unit file gak ada."; exit 1; }

echo "==> backup unit lama -> $UNIT_BAK"
cp -a "$UNIT" "$UNIT_BAK"

echo "==> pasang unit Go"
install -m 644 "$GO_DIR/deploy/monitor-api.service" "$UNIT"
systemctl daemon-reload
systemctl restart monitor-api

# ── Verify ──────────────────────────────────────────────────────────────────
# Restart returns as soon as the process is spawned, so poll rather than
# assuming a fixed sleep is long enough.
echo "==> verifikasi port $PORT"
ok=0
for i in $(seq 1 15); do
    if curl -sf -o /dev/null --max-time 3 "http://127.0.0.1:$PORT/api/system"; then ok=1; break; fi
    sleep 1
done

if [ "$ok" -ne 1 ]; then
    echo "!!! GAGAL jawab di $PORT — rollback ke Python"
    cp -a "$UNIT_BAK" "$UNIT"
    systemctl daemon-reload
    systemctl restart monitor-api
    echo "!!! sudah di-rollback. Python tree TIDAK dihapus."
    echo "!!! log: journalctl -u monitor-api -n 50 --no-pager"
    exit 1
fi

hostname_seen=$(curl -sf --max-time 5 "http://127.0.0.1:$PORT/api/system" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["hostname"])')
echo "    OK — API jawab di $PORT (hostname: $hostname_seen)"

# ── Delete the Python tree ──────────────────────────────────────────────────
# The sanity check is the point: rm -rf with a variable is only safe if we
# confirm the variable still points at what we think it does.
if [ -f "$PY_DIR/server.py" ] && [ -d "$PY_DIR/.venv" ]; then
    echo "==> hapus $PY_DIR"
    rm -rf "$PY_DIR"
    echo "    terhapus"
elif [ -e "$PY_DIR" ]; then
    echo "!!! $PY_DIR ada tapi isinya bukan yang diharapkan — TIDAK dihapus, cek manual"
else
    echo "==> $PY_DIR sudah tidak ada, dilewati"
fi

# Confirm the API survives the deletion (it must — nothing references that path
# anymore, but proving it beats assuming it).
sleep 1
if curl -sf -o /dev/null --max-time 5 "http://127.0.0.1:$PORT/api/system"; then
    echo "    API masih hidup setelah penghapusan"
else
    echo "!!! API mati setelah penghapusan — pulihkan dari monitor-api-python-backup.tar.gz"
    exit 1
fi

systemctl is-enabled monitor-api >/dev/null && echo "==> enabled saat boot: ya"
echo
echo "SELESAI. Go sekarang melayani monitor-api di :$PORT"
echo "  rollback unit lama masih ada di $UNIT_BAK"
echo "  (unit itu nunjuk ke tree Python yang sudah dihapus — pulihkan dari"
echo "   monitor-api-python-backup.tar.gz dulu kalau mau benar-benar balik)"
