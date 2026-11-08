#!/bin/sh
# Kiểm tra xem một tiền tố routed đã sẵn sàng chưa: dùng địa chỉ trong tiền tố truy cập api64.ipify.org qua he-ipv6-pool,
# nếu địa chỉ đầu ra trả về bằng địa chỉ đó thì coi là sẵn sàng (cả chiều đi và chiều về đều thông). Phù hợp để chạy định kỳ sau khi HE cấp tiền tố mới.
#
# Cách dùng: check-prefix.sh <địa chỉ kiểm thử trong tiền tố>    ví dụ check-prefix.sh 2001:db8:abcd::1
# Địa chỉ phải viết dạng chuẩn nén (khớp định dạng api64.ipify.org trả về), và tiền tố chứa nó đã được ghi vào prefixes trong cấu hình.
# Có thể ghi đè bằng biến môi trường: BIN, CONF, SOCKS, PROXY_USER, PROXY_PASS, LOG.
# Mã thoát: 0 sẵn sàng, 1 không sẵn sàng. Nếu he-ipv6-pool chưa chạy sẽ tạm khởi động, kiểm tra xong thì dừng.
set -u
ADDR=${1:?Cách dùng: $0 <địa chỉ kiểm thử trong tiền tố>}
DIR=$(cd "$(dirname "$0")" && pwd)
BIN=${BIN:-$DIR/he-ipv6-pool}
CONF=${CONF:-$DIR/config.json}
SOCKS=${SOCKS:-127.0.0.1:1080}
PROXY_USER=${PROXY_USER:-check}
PROXY_PASS=${PROXY_PASS:-x}
LOG=${LOG:-$DIR/check-prefix.log}

started=""
if ! pgrep -x he-ipv6-pool >/dev/null; then
	"$BIN" -c "$CONF" >/dev/null 2>&1 &
	started=$!
	sleep 2
fi
user="$PROXY_USER-ip-$(echo "$ADDR" | tr ':' '-')"
got=$(curl -s -m 20 -x "socks5h://$user:$PROXY_PASS@$SOCKS" https://api64.ipify.org)
[ -n "$started" ] && kill "$started"

ts=$(date -u '+%F %T UTC')
if [ "$got" = "$ADDR" ]; then
	echo "$ts READY $ADDR" | tee -a "$LOG"
	exit 0
fi
echo "$ts NOT-READY $ADDR (đầu ra: ${got:-không phản hồi})" | tee -a "$LOG"
exit 1
