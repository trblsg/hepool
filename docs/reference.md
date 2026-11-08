# Tham khảo tham số he-ipv6-pool

Tài liệu này giải thích từng mục về tất cả tham số dòng lệnh, trường cấu hình, hậu tố tên người dùng, hành vi giao thức và mã lỗi. Phần giới thiệu và nguyên lý xem tại [README](../README.md).
Các giá trị `203.0.113.10`, `alice`, `change-me` trong ví dụ đều là giá trị giữ chỗ.

- [1. Dòng lệnh](#1-dòng-lệnh)
- [2. Tệp cấu hình](#2-tệp-cấu-hình-configjson)
- [3. Hậu tố tên người dùng: chọn IP đầu ra](#3-hậu-tố-tên-người-dùng-chọn-ip-đầu-ra)
- [4. Giao thức proxy](#4-giao-thức-proxy)
- [5. Bảng tra mã lỗi](#5-bảng-tra-mã-lỗi)
- [6. scripts/check-prefix.sh](#6-scriptscheck-prefixsh)
- [7. Phát hành](#7-phát-hành-github-actions)
- [8. Hạn chế](#8-hạn-chế)

---

## 1. Dòng lệnh

```
he-ipv6-pool [-c config.json] [-v]                                  Chạy trực tiếp
he-ipv6-pool -version                                               Hiển thị phiên bản
he-ipv6-pool install -server <HE IPv4> -prefix <tiền tố[,tiền tố]>  Cài đặt dưới dạng dịch vụ systemd
he-ipv6-pool config [tùy chọn]                                      Xem hoặc sửa cấu hình dịch vụ đã cài đặt
he-ipv6-pool uninstall                                              Gỡ cài đặt
```

Mọi lệnh con đều có thể thêm `-h` để xem tùy chọn.

### 1.1 Chạy trực tiếp

| Tham số | Giá trị mặc định | Mô tả |
|---|---|---|
| `-c <đường dẫn>` | `config.json` | Đường dẫn tệp cấu hình, định dạng xem [mục 2](#2-tệp-cấu-hình-configjson). Cũng có thể chỉ cấu hình bằng [biến môi trường](#21-biến-môi-trường), khi đó tệp cấu hình có thể không tồn tại |
| `-v` | Tắt | Xuất log debug (mỗi yêu cầu một dòng), tương đương biến môi trường `LOG_LEVEL=debug`, xem [1.9](#19-log-và-kiểm-tra-khởi-động) |
| `-version` | — | In số phiên bản rồi thoát. Bản phát hành là tên thẻ (ví dụ `v1.0.0`), bản tự biên dịch là `dev` |

Cần root hoặc `CAP_NET_RAW` (khuyến nghị dùng `sudo setcap cap_net_raw+ep ./he-ipv6-pool` rồi chạy bằng người dùng thường). Khi nhận SIGINT hoặc SIGTERM, chương trình đóng listener và tunnel rồi thoát.

### 1.2 Tùy chọn của install / config

Hai lệnh có cùng các tùy chọn. **Chỉ những tùy chọn được chỉ định rõ ràng trên dòng lệnh mới thay đổi cấu hình**, tùy chọn không được chỉ định sẽ giữ nguyên giá trị cũ.

| Tùy chọn | Trường cấu hình tương ứng | Giá trị mặc định | Mô tả |
|---|---|---|---|
| `-server <IPv4>` | `server_ipv4` | Không có, bắt buộc khi cài lần đầu | Server IPv4 Address trên trang chi tiết tunnel HE |
| `-prefix <tiền tố,…>` | `prefixes` | Không có, bắt buộc khi cài lần đầu | Routed /64, /48, nhiều tiền tố phân tách bằng dấu phẩy, ví dụ `2001:db8:1::/64,2001:db8:abcd::/48`. Khi được cung cấp, thay thế toàn bộ danh sách cũ |
| `-local <IPv4>` | `local_ipv4` | Trống | IPv4 ràng buộc cục bộ. Máy chủ cloud dùng 1:1 NAT để trống; `-local ""` để xóa |
| `-mtu <số>` | `mtu` | `1480` | MTU tunnel, phạm vi 1280~65535, phải khớp với bảng điều khiển HE |
| `-listen <[địa chỉ]:cổng>` | `listen` | `:cổng ngẫu nhiên` | Địa chỉ lắng nghe proxy, SOCKS5 và HTTP dùng chung |
| `-user <tên người dùng>` | `users` | Ngẫu nhiên | Tên người dùng proxy |
| `-pass <mật khẩu>` | `users` | Ngẫu nhiên | Mật khẩu proxy |

Lệnh quản trị chỉ duy trì một người dùng: chỉ cung cấp `-user` thì giữ mật khẩu cũ, chỉ cung cấp `-pass` thì giữ tên người dùng cũ.

**Các mục chưa cấu hình sẽ được tạo tự động:**

| Mục | Quy tắc |
|---|---|
| Cổng | Cổng TCP trống trên máy trong khoảng 20000~59999, bỏ qua các cổng số tròn như x000, x001 |
| Tên người dùng | `u` + 9 ký tự chữ thường và chữ số |
| Mật khẩu | 26 ký tự base32 (chữ in hoa và 2~7), khoảng 130 bit |

**Kiểm tra hợp lệ** (nếu không qua sẽ báo lỗi và thoát, không thay đổi gì):

- `-server` phải là IPv4, mỗi mục `-prefix` phải là tiền tố IPv6, `-local` để trống hoặc là IPv4
- `listen` phải đúng định dạng `[địa chỉ]:cổng`
- Tên người dùng và mật khẩu không được để trống, không được chứa `:`, `@`, `/`, khoảng trắng (sẽ làm hỏng URL proxy)
- Tên người dùng không được chứa `-session-`, `-ip-`, `-ttl-`, `-cidr-` (sẽ xung đột với [hậu tố tên người dùng](#3-hậu-tố-tên-người-dùng-chọn-ip-đầu-ra))

### 1.3 install

Chỉ hỗ trợ Linux + systemd, cần root. Void Linux dùng runit không có systemd nên phải cài thủ công theo [hướng dẫn trong README](../README.md#void-linux-runit). Thực hiện lần lượt:

1. Đọc `/opt/he-ipv6-pool/config.json` hiện có (nếu có), ghi đè bằng tùy chọn dòng lệnh, bổ sung giá trị mặc định và kiểm tra hợp lệ
2. Tạo người dùng và nhóm hệ thống `hepool` (không thể đăng nhập, không có thư mục home)
3. Sao chép binary đang chạy vào `/opt/he-ipv6-pool/he-ipv6-pool` (ghi tệp tạm rồi đổi tên, có thể thay thế khi dịch vụ đang chạy)
4. Ghi dịch vụ systemd `/etc/systemd/system/he-ipv6-pool.service`
5. Ghi cấu hình, quyền `root:hepool 0640` (dịch vụ đọc được, người dùng thường khác không thấy mật khẩu)
6. Đồng bộ quy tắc iptables và lưu lại, xem [1.6](#16-quy-tắc-tường-lửa)
7. `systemctl enable` + `restart`, đợi 2 giây xác nhận dịch vụ đang chạy, nếu thất bại in 20 dòng log gần nhất
8. In cấu hình và ví dụ kết nối

**Chạy lại install chính là nâng cấp**: dùng binary phiên bản mới chạy `sudo ./he-ipv6-pool-phien-ban-moi install`, toàn bộ cấu hình cũ được giữ lại.

### 1.4 config

| Cách dùng | Tác dụng |
|---|---|
| `config` | In cấu hình hiện tại và ví dụ kết nối, không thay đổi gì |
| `config <tùy chọn>` | Sửa cấu hình, đồng bộ quy tắc tường lửa, khởi động lại dịch vụ |

Ví dụ: `config -listen :31234` đổi cổng (quy tắc của cổng cũ sẽ bị xóa), `config -pass mat-khau-moi` đổi mật khẩu, `config -prefix tien-to-moi` đổi tiền tố. Nếu chưa cài đặt sẽ báo lỗi.

### 1.5 uninstall

Dừng và vô hiệu hóa dịch vụ, sau đó xóa tệp dịch vụ, `/opt/he-ipv6-pool` (gồm cấu hình), người dùng `hepool`, và các quy tắc iptables tương ứng trong cấu hình (đồng thời lưu lại).

### 1.6 Quy tắc tường lửa

install và config sẽ chèn vào đầu chuỗi `INPUT` của iptables (bỏ qua nếu đã tồn tại):

```
-p 41 -s <server_ipv4> -j ACCEPT                    # tunnel 6in4
-p tcp -m tcp --dport <cổng listen> -j ACCEPT       # cổng proxy; không thêm khi lắng nghe địa chỉ loopback
```

- Khi sửa cấu hình, xóa quy tắc tương ứng với cấu hình cũ trước, rồi thêm quy tắc mới
- Nếu đã cài `netfilter-persistent` thì chạy `netfilter-persistent save` để lưu lại, nếu không chỉ in gợi ý
- Không có iptables thì chỉ in gợi ý
- Security group của nhà cung cấp cloud (OCI Security List, AWS Security Group, v.v.) cần tự cho phép giao thức 41 và cổng proxy

### 1.7 Dịch vụ systemd

Chạy dưới người dùng `hepool`, `ExecStart=/opt/he-ipv6-pool/he-ipv6-pool -c /opt/he-ipv6-pool/config.json`, tự khởi động lại sau 5 giây nếu thoát bất thường. Quyền chỉ có `CAP_NET_RAW`, đồng thời bật các tùy chọn sandbox như `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `PrivateDevices`, `ProtectKernel*`, `RestrictAddressFamilies`, `LimitNOFILE=65536`.

```bash
systemctl status he-ipv6-pool      # trạng thái
journalctl -u he-ipv6-pool -f      # log
```

### 1.8 Docker

Image `ghcr.io/wsyqhh/he-ipv6-pool`, hỗ trợ linux/amd64 và linux/arm64, dựa trên `scratch`, bên trong chỉ có một binary tĩnh. Khuyến nghị cấu hình bằng [biến môi trường](#21-biến-môi-trường); cũng có thể mount tệp cấu hình vào `/etc/he-ipv6-pool/config.json` (lệnh mặc định là `-c /etc/he-ipv6-pool/config.json`), khi dùng đồng thời cả hai thì biến môi trường được ưu tiên.

| Thẻ image | Mô tả |
|---|---|
| `latest` | Bản chính thức mới nhất |
| `0.3.0` | Phiên bản chỉ định (tên thẻ bỏ `v`) |
| `0.3` | Phiên bản mới nhất của dòng 0.3 |
| `0.4.0-rc1` v.v. | Bản phát hành trước chỉ gắn số phiên bản đầy đủ, không cập nhật `latest` và `0.4` |

**Docker Compose** (khuyến nghị): [`compose.yaml`](../compose.yaml) trong kho đã viết sẵn các tham số dưới đây, cấu hình đọc từ `.env` cùng thư mục, mẫu là [`.env.example`](../.env.example):

```bash
curl -fLO https://raw.githubusercontent.com/wsyqhh/he-ipv6-pool/main/compose.yaml
curl -fL https://raw.githubusercontent.com/wsyqhh/he-ipv6-pool/main/.env.example -o .env
vi .env                       # nếu để trống mục bắt buộc, docker compose sẽ báo lỗi và nhắc cần điền gì
docker compose up -d
docker compose logs -f        # log
docker compose pull && docker compose up -d   # nâng cấp
```

Đặt `IMAGE_TAG=0.3.0` trong `.env` để cố định phiên bản image, mặc định là `latest`.

**docker run**:

```bash
docker run -d --name he-ipv6-pool --restart unless-stopped \
  --network host --cap-drop ALL --cap-add NET_RAW --read-only \
  --env-file .env \
  ghcr.io/wsyqhh/he-ipv6-pool:latest

docker run --rm ghcr.io/wsyqhh/he-ipv6-pool:latest -version
```

| Tham số | Tác dụng |
|---|---|
| `--network host` | **Bắt buộc**. Dùng trực tiếp mạng của máy chủ, gói 6in4 không đi qua NAT |
| `--cap-drop ALL --cap-add NET_RAW` | Chỉ giữ quyền cần thiết cho raw socket |
| `--read-only` | Chương trình không ghi tệp, hệ thống tệp gốc có thể chỉ đọc |
| `--env-file .env` | Cấu hình bằng biến môi trường; khi chuyển sang tệp cấu hình thì thay bằng `-v /path/config.json:/etc/he-ipv6-pool/config.json:ro` |

**Vì sao không thể dùng mạng bridge**: gói giao thức 41 do container gửi ra phải đi qua MASQUERADE của Docker. Giao thức 41 không có cổng, conntrack chỉ phân biệt kết nối theo «IP máy này ↔ IP máy chủ HE», cùng một cặp địa chỉ chỉ có thể có một bản ghi. Lưu lượng 6in4 của chính máy chủ, bản ghi còn lại từ container trước (sau khi container được tạo lại thì IP đã đổi) đều sẽ chiếm tổ hợp này, gói mới sẽ bị bỏ do xung đột NAT. Bản ghi này chỉ hết hạn sau 10 phút rảnh, trong khi các gói phản hồi HE liên tục gửi tới sẽ làm mới nó mãi. Thực đo cho thấy mạng bridge hoàn toàn không thông ngay sau khi máy chủ vừa dừng dịch vụ 6in4, phải chạy tay `conntrack -D -p 41` xóa bản ghi cũ mới hồi phục.

**Các lưu ý khác:**

- Trong container không có lệnh quản trị. Tường lửa trên máy chủ phải tự cho phép: `iptables -I INPUT -p 41 -s <server_ipv4> -j ACCEPT`, và cổng TCP của `listen`
- Không chạy đồng thời bản Docker và dịch vụ systemd cài bằng `install` trên cùng một máy: hai thực thể sẽ cùng nhận mọi gói giao thức 41, ngăn xếp giao thức của mỗi bên sẽ coi kết nối của bên kia là kết nối lạ và reset. Khi chuyển từ systemd sang Docker, hãy chạy `uninstall` trước, hoặc `systemctl disable --now he-ipv6-pool`
- Tự build: `docker build --build-arg VERSION=v0.3.0 -t he-ipv6-pool .`; đa kiến trúc: `docker buildx build --platform linux/amd64,linux/arm64 …`

### 1.9 Log và kiểm tra khởi động

Log được ghi ra lỗi chuẩn (stderr). Khi chạy dưới systemd, log không có dấu thời gian (journald sẽ ghi thời gian), xem bằng `journalctl -u he-ipv6-pool -f`; với Docker dùng `docker compose logs -f`.

| Log | Mức | Nội dung |
|---|---|---|
| `Đã khởi động` | info | Phiên bản, máy chủ HE, địa chỉ ràng buộc cục bộ, MTU, tiền tố, địa chỉ lắng nghe, xác thực (bao nhiêu người dùng) |
| `Kiểm tra tunnel đạt` / `Kiểm tra tunnel thất bại` | info / warn | Mỗi tiền tố một dòng, xem bên dưới |
| `Tất cả tiền tố đều không thông` | error | Xuất thêm khi mọi tiền tố đều kiểm tra thất bại, nhắc kiểm tra việc cho phép giao thức 41 và endpoint IPv4 |
| `Trong 10 phút gần đây` | info | Mỗi 10 phút một dòng thống kê yêu cầu, không xuất khi không có hoạt động nào |
| `Yêu cầu hoàn tất` / `Yêu cầu thất bại` | debug | Mỗi yêu cầu một dòng, cần `-v` hoặc `LOG_LEVEL=debug` |

**Kiểm tra khởi động**: sau khi khởi động, ở chế độ nền lấy ngẫu nhiên một địa chỉ từ mỗi tiền tố, đồng thời kết nối tới `[2606:4700:4700::1111]:443` (Cloudflare) và `[2001:4860:4860::8888]:443` (Google), nếu trong 5 giây có bất kỳ kết nối thành công hoặc bị từ chối (nhận RST cũng chứng tỏ gói phản hồi đã tới) thì coi tiền tố đó khả dụng.

```
INFO Kiểm tra tunnel đạt prefix=2001:db8:1::/64 src=2001:db8:1:0:6dc7:e2d3:ce58:cdcc rtt=232ms
WARN Kiểm tra tunnel thất bại: tiền tố này không nhận được gói phản hồi, có thể HE chưa định tuyến nó tới tunnel này prefix=2001:db8:abcd::/48 …
```

Kết quả kiểm tra chỉ ghi vào log, không làm dịch vụ thoát (nếu thoát khi HE gặp sự cố tạm thời, systemd sẽ liên tục khởi động lại), cũng không loại bỏ tiền tố không thông (sau khi loại bỏ, số tiền tố thay đổi, IP cố định của mọi phiên sẽ đổi). Khi một tiền tố mãi không thông, hãy xóa nó khỏi `prefixes`, hoặc đổi sang máy chủ tunnel khác để tạo lại.

**Thống kê định kỳ**:

```
INFO Trong 10 phút gần đây requests=1532 ok=1490 no_ipv6=30 timeout=12 active=87 up=12.3MiB down=845.1MiB
```

| Trường | Ý nghĩa |
|---|---|
| `requests` | Số yêu cầu kết thúc trong khoảng thời gian này (kết nối SOCKS5, liên kết UDP, HTTP CONNECT, HTTP chuyển tiếp mỗi loại tính một; kết nối dài chỉ được tính khi đóng) |
| `ok` | Thành công |
| `auth` | Tên người dùng hoặc mật khẩu sai |
| `rejected` | Hậu tố tên người dùng không hợp lệ, hoặc địa chỉ đầu ra chỉ định không nằm trong pool |
| `no_ipv6` | Đích không có địa chỉ IPv6, hoặc phân giải DNS thất bại |
| `refused` / `timeout` | Đích từ chối kết nối / kết nối tới đích quá thời gian |
| `other` | Lỗi khác, ví dụ máy khách ngắt giữa chừng |
| `active` | Số kết nối đang được chuyển tiếp |
| `up` / `down` | Lưu lượng từ máy khách tới đích / từ đích về máy khách |

Chỉ liệt kê các phân loại khác 0. Máy khách ngắt kết nối trước khi xác thực hoặc gửi dữ liệu không hợp lệ (ví dụ quét cổng) không được tính vào thống kê, chỉ ghi ở mức debug `Bắt tay SOCKS5 thất bại`.

**Log của từng yêu cầu** (debug):

```
DEBUG Yêu cầu hoàn tất proto=socks5 client=203.0.113.7:59128 user=alice-session-x dst=example.com:443 src=2001:db8:abcd::1 dur=2.7s up=592 down=4971
DEBUG Yêu cầu thất bại proto=http client=203.0.113.7:59148 user=alice dst=example.com result=auth err="proxy: xác thực thất bại"
```

`proto` là `socks5`, `socks5-udp`, `http-connect` hoặc `http`; `user` là tên người dùng đầy đủ (gồm hậu tố), **không ghi mật khẩu**; `src` là địa chỉ đầu ra thực tế; `up`, `down` là số byte; khi thất bại `result` là phân loại trong bảng trên.

---

## 2. Tệp cấu hình config.json

```json
{
  "server_ipv4": "216.66.80.26",
  "local_ipv4": "",
  "prefixes": ["2001:db8:1234::/64", "2001:db8:abcd::/48"],
  "mtu": 1480,
  "listen": "127.0.0.1:1080",
  "users": {"alice": "change-me"}
}
```

| Trường | Kiểu | Bắt buộc | Giá trị mặc định | Mô tả |
|---|---|---|---|---|
| `server_ipv4` | Chuỗi | Có | — | Server IPv4 Address trên trang chi tiết tunnel HE. Chỉ chấp nhận gói 6in4 từ địa chỉ này |
| `local_ipv4` | Chuỗi | Không | Trống | IPv4 cục bộ mà raw socket ràng buộc, để trống nghĩa là kernel tự chọn. Máy chủ cloud 1:1 NAT để trống; sau router gia đình để trống hoặc điền IP nội bộ |
| `prefixes` | Mảng chuỗi | Có | — | Tiền tố IPv6 có thể dùng làm đầu ra, chỉ được điền Routed /64, /48 mà HE đã định tuyến tới tunnel này. Phần host bit sẽ tự động bị xóa về 0 |
| `mtu` | Số nguyên | Không | `1480` | MTU tunnel, phạm vi 1280~65535 |
| `listen` | Chuỗi | Có | — | Địa chỉ lắng nghe proxy, ví dụ `:24601` (mọi địa chỉ), `127.0.0.1:1080` (chỉ máy này). SOCKS5 và HTTP dùng chung |
| `users` | Đối tượng | Không | Trống | Tên người dùng → mật khẩu. Khi để trống thì không yêu cầu xác thực, khi đó chỉ nên lắng nghe địa chỉ loopback, nếu không sẽ in cảnh báo khi khởi động |

Khi sửa cấu hình thủ công có thể viết nhiều người dùng; lệnh quản trị chỉ duy trì một người trong đó. Tên người dùng cơ bản không được chứa `-session-`, `-ip-`, `-ttl-`, `-cidr-`, nếu không sẽ bị tách thành hậu tố và xác thực thất bại.

### 2.1 Biến môi trường

Mỗi biến môi trường tương ứng một trường cấu hình, cũng tương ứng một tùy chọn của `install`, quy tắc phân tích và kiểm tra giống nhau. **Biến môi trường ghi đè mục cùng tên trong tệp cấu hình**, biến không được đặt sẽ giữ giá trị trong tệp cấu hình; khi tệp cấu hình không tồn tại nhưng có đặt bất kỳ biến môi trường nào, chương trình chỉ dùng biến môi trường để khởi động (phù hợp triển khai bằng container).

| Biến môi trường | Trường cấu hình | Tùy chọn `install` | Mô tả |
|---|---|---|---|
| `HE_SERVER` | `server_ipv4` | `-server` | Bắt buộc |
| `HE_PREFIXES` | `prefixes` | `-prefix` | Bắt buộc, nhiều tiền tố phân tách bằng dấu phẩy; khi được đặt sẽ thay thế toàn bộ danh sách trong tệp cấu hình |
| `HE_LOCAL` | `local_ipv4` | `-local` | Đặt thành chuỗi rỗng nghĩa là xóa |
| `HE_MTU` | `mtu` | `-mtu` | Số nguyên, mặc định 1480 |
| `PROXY_LISTEN` | `listen` | `-listen` | Bắt buộc |
| `PROXY_USER` | `users` | `-user` | Khi chỉ đặt tên người dùng thì giữ mật khẩu trong tệp cấu hình |
| `PROXY_PASS` | `users` | `-pass` | Khi chỉ đặt mật khẩu thì giữ tên người dùng trong tệp cấu hình |
| `LOG_LEVEL` | — | — | Mức log: `debug`, `info` (mặc định), `warn`, `error`, xem [1.9](#19-log-và-kiểm-tra-khởi-động) |

```bash
HE_SERVER=216.66.80.26 HE_PREFIXES=2001:db8:1::/64,2001:db8:abcd::/48 \
PROXY_LISTEN=:24601 PROXY_USER=alice PROXY_PASS=$(openssl rand -hex 16) \
sudo -E ./he-ipv6-pool
```

Biến môi trường chỉ cấu hình được một người dùng; cần nhiều người dùng thì dùng tệp cấu hình. Khi giá trị không hợp lệ hoặc thiếu mục bắt buộc, lỗi khởi động sẽ chỉ ra biến môi trường nào.

---

## 3. Hậu tố tên người dùng: chọn IP đầu ra

IP đầu ra được quyết định bởi **hậu tố sau tên người dùng**, mật khẩu không đổi. Quy tắc của SOCKS5 và HTTP proxy hoàn toàn giống nhau.

```
<tên người dùng>[-session-<id>][-ttl-<thời lượng>][-cidr-<số bit>]     thứ tự hậu tố tùy ý, có thể kết hợp
<tên người dùng>-ip-<địa chỉ>                                         chỉ định địa chỉ, không thể kết hợp với hậu tố khác
```

### 3.1 Hậu tố

| Hậu tố | Giá trị | Tác dụng |
|---|---|---|
| (không có) | — | Ngẫu nhiên mỗi kết nối: chọn đều xác suất một tiền tố trong `prefixes`, rồi điền ngẫu nhiên phần host bit |
| `-session-<id>` | Chuỗi tùy ý, có thể chứa `-`, có thể trống | Cùng một phiên cố định cùng một IP |
| `-ttl-<thời lượng>` | Số thuần túy tính theo **phút**; cũng có thể viết `90s`, `10m`, `2h`, `1h30m`; phải lớn hơn 0 | Thời gian sống của IP cố định, tính từ **lần sử dụng đầu tiên**, việc sử dụng trong thời gian đó không kéo dài nó, hết hạn thì đổi sang IP mới |
| `-cidr-<số bit>` | Số nguyên 1~128 | Luân chuyển subnet theo /số bit, xem [3.3](#33-luân-chuyển-subnet) |
| `-ip-<địa chỉ>` | Địa chỉ IPv6, `:` viết thành `-`, ví dụ `2001-db8-abcd--1`; SOCKS5 cũng có thể viết trực tiếp dấu hai chấm | Chỉ định địa chỉ đầu ra, phải nằm trong `prefixes` |

### 3.2 Hiệu ứng kết hợp

| Tên người dùng ví dụ | Đầu ra | Trạng thái |
|---|---|---|
| `alice` | Ngẫu nhiên mỗi kết nối | Không |
| `alice-cidr-64` | Ngẫu nhiên mỗi kết nối, mỗi lần đổi một /64 | Không |
| `alice-session-abc` | Cố định vĩnh viễn | Không trạng thái: băm `alice-session-abc` thành ánh xạ, miễn số lượng và thứ tự `prefixes` không đổi thì sau khi khởi động lại cũng không đổi |
| `alice-session-abc-ttl-10` | Cố định 10 phút, sau đó đổi IP mới | Bộ nhớ |
| `alice-session-abc-cidr-64` | Cố định IP, các phiên khác nhau đảm bảo nằm ở các /64 khác nhau | Bộ nhớ |
| `alice-session-abc-ttl-30-cidr-64` | Cố định 30 phút, hết hạn đổi sang /64 mới | Bộ nhớ |
| `alice-ttl-10` | Không có phiên: mọi kết nối của người dùng này dùng chung một IP, 10 phút đổi một lần | Bộ nhớ |
| `alice-ttl-10-cidr-64` | Như trên, mỗi lần đổi sang /64 mới | Bộ nhớ |
| `alice-ip-2001-db8-abcd--1` | Cố định là `2001:db8:abcd::1` | Không |

**Phiên trong bộ nhớ:**

- Phiên được nhận diện bằng «tên người dùng cơ bản + ID phiên». Cùng một ID phiên khi đổi giá trị `-ttl-` hoặc `-cidr-` vẫn là cùng một phiên; giá trị `-ttl-` chỉ có hiệu lực khi cấp IP mới
- `-session-<id>` đơn lẻ đi theo ánh xạ băm, không liên quan tới phiên cùng tên có `-ttl-`, `-cidr-`: `alice-session-abc` và `alice-session-abc-ttl-10` là hai IP khác nhau
- Bị xóa sau khi khởi động lại dịch vụ, rồi cấp lại
- Phiên có `-ttl-` mất hiệu lực sau khi hết hạn; phiên không có `-ttl-` (chỉ có `-cidr-`) mất hiệu lực sau 24 giờ rảnh, mỗi lần sử dụng trong thời gian đó đều được gia hạn
- Phiên hết hạn được dọn dẹp nhân lúc bảng phiên tăng trưởng, không chiếm bộ nhớ mãi
- Kết nối đang dùng không bị ảnh hưởng bởi hết hạn, chỉ kết nối mới sau khi hết hạn mới đổi IP

### 3.3 Luân chuyển subnet

Chia tất cả tiền tố thành các subnet /số bit và đánh số thống nhất. Ví dụ với `-cidr-64`, /48 chia thành 65536 /64, bản thân tiền tố /64 tính là 1, tiền tố nhỏ hơn số bit cũng tính là 1. Mỗi lần lấy subnet tương ứng với số thứ tự tiếp theo, phần host bit ngẫu nhiên:

- Trước khi dùng hết mọi subnet **không lặp lại** cùng một subnet
- Hai lần liền nhau không rơi vào các subnet sát nhau (không phải tăng tuần tự kiểu `…:1::`, `…:2::`)
- Số thứ tự luân chuyển được chia sẻ bởi mọi kết nối và phiên có `-cidr-`; khi dịch vụ khởi động bắt đầu từ vị trí ngẫu nhiên, sau khi khởi động lại không phát lại cùng thứ tự
- Xác suất chọn từng tiền tố tỉ lệ với số subnet: `/48 + /64` với `-cidr-64` gần như đều rơi vào /48. Không có `-cidr-` thì mỗi tiền tố chiếm một nửa
- Mỗi tiền tố tham gia luân chuyển tối đa 2^40 subnet (HE cấp tối đa /48, theo /64 chỉ có 2^16, không chạm tới)

Giá trị thường dùng: `-cidr-64` (hầu hết trang web giới hạn tốc độ, chặn theo /64), `-cidr-56`, `-cidr-48` (luân phiên giữa nhiều tiền tố).

### 3.4 Quy tắc phân tích

- Phần trước hậu tố đầu tiên là tên người dùng cơ bản, dùng để xác thực
- Giá trị của mỗi hậu tố kéo dài đến hậu tố tiếp theo, nên **ID phiên không được chứa** `-session-`, `-ttl-`, `-cidr-`, `-ip-`
- Phần ngay sau tên hậu tố thuộc về giá trị: ID phiên của `alice-session-ip-x` là `ip-x`
- Cùng một hậu tố xuất hiện hai lần, giá trị không hợp lệ, `-ip-` kết hợp với hậu tố khác thì trả lỗi (HTTP 403, SOCKS5 trả lời 0x02)
- Kiểm tra mật khẩu trước, rồi mới báo lỗi hậu tố; mật khẩu sai thì luôn xử lý là xác thực thất bại
- Tên người dùng SOCKS5 dài tối đa 255 byte (RFC 1929)

### 3.5 Ví dụ

```bash
P=change-me; S=203.0.113.10:24601
curl -x socks5h://alice:$P@$S https://api64.ipify.org                              # ngẫu nhiên
curl -x socks5h://alice-cidr-64:$P@$S https://api64.ipify.org                      # mỗi lần đổi /64
curl -x socks5h://alice-session-job1:$P@$S https://api64.ipify.org                 # cố định vĩnh viễn
curl -x socks5h://alice-session-job1-ttl-30-cidr-64:$P@$S https://api64.ipify.org  # cố định 30 phút
curl -x http://alice-ip-2001-db8-abcd--1:$P@$S https://api64.ipify.org             # chỉ định địa chỉ
```

```python
# Python requests (cần pip install requests[socks])
proxies = {"https": "socks5h://alice-session-job1-ttl-30:change-me@203.0.113.10:24601"}
requests.get("https://api64.ipify.org", proxies=proxies)
```

---

## 4. Giao thức proxy

### 4.1 Cùng một cổng

Cổng `listen` đồng thời cung cấp proxy SOCKS5 và HTTP: phân biệt theo byte đầu tiên của kết nối, `0x05` là SOCKS5, các trường hợp khác giao cho máy chủ HTTP xử lý (dữ liệu không phải HTTP hợp lệ sẽ nhận `400`).

### 4.2 Xác thực

| `users` | SOCKS5 | HTTP |
|---|---|---|
| Không rỗng | Phải xác thực bằng tên người dùng/mật khẩu (phương thức 0x02) | Phải có `Proxy-Authorization: Basic …`, nếu không trả `407` |
| Rỗng | Chấp nhận không xác thực (phương thức 0x00); khi máy khách cung cấp tên người dùng/mật khẩu thì mật khẩu nào cũng qua, hậu tố tên người dùng vẫn có hiệu lực | Không yêu cầu xác thực, hậu tố vẫn có hiệu lực |

Mật khẩu được so sánh trong thời gian hằng số.

### 4.3 SOCKS5

| Mục | Mức hỗ trợ |
|---|---|
| Phiên bản | SOCKS5 (RFC 1928, RFC 1929), không hỗ trợ SOCKS4 |
| Lệnh | `CONNECT` (TCP), `UDP ASSOCIATE` (UDP); không hỗ trợ `BIND` |
| Địa chỉ đích | Tên miền, IPv6; đích IPv4 sẽ bị từ chối |
| UDP | Không hỗ trợ phân mảnh; chỉ chấp nhận gói từ IP máy khách đã thiết lập liên kết; cổng UDP là cổng ngẫu nhiên trên máy chủ, quy tắc iptables chỉ cho phép TCP, nên UDP chỉ phù hợp dùng trên cùng máy hoặc cùng mạng nội bộ |

**Khuyến nghị dùng `socks5h://`** để proxy phân giải tên miền. Nếu dùng `socks5://`, máy khách sẽ phân giải cục bộ, có thể nhận địa chỉ IPv4 và bị từ chối.

### 4.4 HTTP

| Yêu cầu | Hành vi |
|---|---|
| `CONNECT host:port` (https, v.v.) | Thiết lập tunnel TCP, trả lời `200 Connection established` |
| `GET http://…` và các dạng absolute-URI | Chuyển tiếp, mỗi yêu cầu tạo kết nối upstream mới, không thêm `X-Forwarded-For`, không lộ IP máy khách |
| Yêu cầu đường dẫn tương đối | `400`, đây là máy chủ proxy chứ không phải trang web |

### 4.5 Thời gian chờ và DNS

| Mục | Giá trị |
|---|---|
| Chờ byte đầu tiên sau khi thiết lập kết nối | 30 giây |
| Bắt tay SOCKS5 (đến khi trả kết quả CONNECT) | 30 giây |
| Đọc header yêu cầu HTTP | 30 giây |
| Kết nối tới đích | 30 giây |
| Kết nối đang chuyển tiếp | Không có thời gian chờ rảnh, hỗ trợ đóng một chiều |

Tên miền được phân giải bằng DNS hệ thống của máy chủ, chỉ truy vấn bản ghi AAAA, lấy địa chỉ đầu tiên; bản thân yêu cầu DNS đi qua mạng thông thường của máy chủ, không qua tunnel.

---

## 5. Bảng tra mã lỗi

| Tình huống | Proxy HTTP | SOCKS5 |
|---|---|---|
| Tên người dùng hoặc mật khẩu sai | `407` | Trạng thái trả lời xác thực `0x01`, sau đó ngắt kết nối |
| Máy khách không có phương thức xác thực được chấp nhận | — | Trả lời phương thức `0xFF` |
| Hậu tố không hợp lệ, địa chỉ `-ip-` không nằm trong pool | `403` | `0x02` quy tắc không cho phép |
| Đích không có địa chỉ IPv6, phân giải DNS thất bại | `502` | `0x04` máy đích không thể tới |
| Đích từ chối kết nối | `502` | `0x05` kết nối bị từ chối |
| Lỗi kết nối khác, quá thời gian | `502` | `0x01` thất bại chung |
| Lệnh không được hỗ trợ (BIND) | — | `0x07` |
| Loại địa chỉ không được hỗ trợ | — | `0x08` |
| Yêu cầu đường dẫn tương đối, dữ liệu không hợp lệ | `400` | — |

---

## 6. scripts/check-prefix.sh

Kiểm tra xem một tiền tố đã được HE định tuyến chưa: dùng địa chỉ trong tiền tố truy cập `api64.ipify.org` qua proxy, nếu địa chỉ đầu ra trả về bằng địa chỉ đó thì coi là khả dụng. Phù hợp để chạy định kỳ sau khi HE cấp tiền tố mới.

```bash
check-prefix.sh <địa chỉ kiểm tra trong tiền tố>    # ví dụ check-prefix.sh 2001:db8:abcd::1
```

Địa chỉ phải viết ở dạng chuẩn nén gọn, và tiền tố chứa nó đã được ghi vào `prefixes` trong cấu hình. Mã thoát 0 nghĩa là khả dụng (in ra `READY`), 1 là không khả dụng (in ra `NOT-READY`), kết quả được ghi nối vào log. Nếu he-ipv6-pool chưa chạy, script sẽ tạm khởi động rồi dừng sau khi kiểm tra.

| Biến môi trường | Giá trị mặc định | Mô tả |
|---|---|---|
| `BIN` | `he-ipv6-pool` cùng thư mục script | Đường dẫn binary |
| `CONF` | `config.json` cùng thư mục script | Cấu hình dùng khi khởi động tạm |
| `SOCKS` | `127.0.0.1:1080` | Địa chỉ proxy |
| `PROXY_USER` | `check` | Tên người dùng proxy (không gồm hậu tố) |
| `PROXY_PASS` | `x` | Mật khẩu proxy |
| `LOG` | `check-prefix.log` cùng thư mục script | Tệp log |

Khi kiểm tra dịch vụ đã cài đặt:

```bash
SOCKS=127.0.0.1:<cổng> PROXY_USER=<tên-người-dùng> PROXY_PASS=<mật-khẩu> ./check-prefix.sh 2001:db8:abcd::1
```

---

## 7. Phát hành (GitHub Actions)

`.github/workflows/release.yml` chạy `go vet` và `go test -race` trước, sau đó dùng bản vá mới nhất của dòng Go 1.25 để biên dịch chéo, cuối cùng tạo GitHub Release (mô tả phát hành được tạo tự động từ lịch sử commit). Hai cách kích hoạt:

| Cách | Thao tác | Mô tả |
|---|---|---|
| Đẩy thẻ | `git tag v0.1.0 && git push origin v0.1.0` | Chọn commit cần phát hành ở máy cục bộ |
| Kích hoạt thủ công | Trang Actions chọn **release** → **Run workflow** → điền số phiên bản; hoặc `gh workflow run release.yml -f version=v0.1.0` | Tự động tạo thẻ trên commit mới nhất của nhánh đã chọn, không cần thao tác git cục bộ |

**Quy tắc số phiên bản** ([phiên bản ngữ nghĩa](https://semver.org/lang/vi/)):

- Định dạng phải là `vX.Y.Z`, nếu không workflow sẽ thất bại ngay; khi kích hoạt thủ công mà thẻ đã tồn tại cũng sẽ thất bại
- Phiên bản có hậu tố (như `v0.2.0-rc1`, `v1.0.0-beta.2`) được phát hành là **bản phát hành trước**, sẽ không trở thành `releases/latest`, lệnh cài đặt một bước trong README cũng sẽ không tải nó
- Chỉ sửa lỗi thì tăng Z, thêm tính năng thì tăng Y, có thay đổi không tương thích (trường cấu hình, ý nghĩa hậu tố tên người dùng thay đổi, v.v.) thì tăng X

| Sản phẩm | Nền tảng |
|---|---|
| `he-ipv6-pool-linux-amd64` / `-arm64` / `-arm` / `-386` / `-riscv64` | Linux |
| `he-ipv6-pool-darwin-amd64` / `-arm64` | macOS (chỉ chạy trực tiếp, không có lệnh quản trị; chưa được kiểm chứng trên tunnel thật) |
| `SHA256SUMS` | Mã kiểm tra |

Job Docker (build image linux/amd64 và linux/arm64 đẩy lên `ghcr.io/wsyqhh/he-ipv6-pool`, thẻ image xem [1.8](#18-docker)) đang tạm tắt trong workflow để kiểm thử build; khi bật lại, job này chỉ chạy sau khi test và kiểm tra số phiên bản đạt.

Binary được biên dịch tĩnh (`CGO_ENABLED=0`). `he-ipv6-pool -version` hiển thị phiên bản, thông tin in bởi `install` và `config` cũng có phiên bản:

| Cách build | `-version` hiển thị |
|---|---|
| Tải từ Release | Tên thẻ, ví dụ `v0.1.0` |
| `go install github.com/wsyqhh/he-ipv6-pool@v0.1.0` | Phiên bản module `v0.1.0` |
| `go build` từ mã nguồn | Thời gian và mã commit, ví dụ `v0.0.0-20261022085723-8dd9972282ed`, có thay đổi chưa commit thì kèm `+dirty` |

Nâng cấp dịch vụ đã cài: tải phiên bản mới rồi chạy `sudo ./he-ipv6-pool install`, cấu hình được giữ lại.

---

## 8. Hạn chế

- Chỉ ra IPv6: đích chỉ có IPv4 sẽ bị từ chối ngay, không fallback về kết nối trực tiếp
- Không hỗ trợ SOCKS5 `BIND` và phân mảnh UDP; UDP chỉ phù hợp dùng trên cùng máy hoặc cùng mạng nội bộ
- `BND.ADDR` trả về từ SOCKS5 `UDP ASSOCIATE` là địa chỉ của máy này, khi ở sau NAT máy khách từ xa sẽ nhận IP nội bộ
- Không cập nhật IPv4 động của HE (có thể dùng cron gọi update URL của tunnelbroker)
- Lệnh quản trị chỉ hỗ trợ Linux + systemd; không hỗ trợ Windows
- Không bật đồng thời tunnel HE của kernel (`iface he-ipv6 inet6 v4tunnel`): kernel sẽ xử lý cùng gói giao thức 41, có thể gửi ngược ICMP unreachable làm nhiễu kết nối. Chạy `ifdown he-ipv6` trước, vẫn lỗi thì `modprobe -r sit`
