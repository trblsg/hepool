# he-ipv6-pool

[![release](https://github.com/wsyqhh/he-ipv6-pool/actions/workflows/release.yml/badge.svg)](https://github.com/wsyqhh/he-ipv6-pool/actions/workflows/release.yml)
[![license](https://img.shields.io/badge/license-GPL--3.0-blue)](LICENSE)

> **Giới thiệu về kho lưu trữ này**
>
> Đây là bản sao của [x6nux/he-ipv6-pool](https://gitlab.com/x6nux/he-ipv6-pool) (kho GitHub upstream không còn tồn tại, mã gốc đến từ GitLab),
> chỉ thay đổi những điểm sau để tự build và phát hành Docker image:
>
> - Đường dẫn module, địa chỉ tải xuống và địa chỉ image được đổi sang kho này (`github.com/wsyqhh/he-ipv6-pool`, `ghcr.io/wsyqhh/he-ipv6-pool`)
> - Quy trình phát hành thêm một bước tự động đặt gói GHCR thành công khai
>
> Bản quyền mã thuộc về tác giả gốc, theo giấy phép GPL-3.0. Các liên kết tải xuống và địa chỉ image trong README upstream **đều không còn hiệu lực**, vui lòng dùng Releases và image của kho này.


Biến một tunnel IPv6 [Hurricane Electric](https://tunnelbroker.net) miễn phí thành **pool proxy IPv6**: dùng **bất kỳ địa chỉ nào** trong Routed /64, /48 do HE cấp làm đầu ra, một /48 đã có 2^80 IP khả dụng.

- **Không tạo card mạng**: không cần sit/gif/TUN, không sửa bảng định tuyến, không ảnh hưởng mạng của chính máy chủ. Toàn bộ ngăn xếp giao thức IPv6 chạy trong không gian người dùng
- **SOCKS5 + HTTP cùng cổng**: SOCKS5 (gồm UDP) và HTTP proxy (gồm CONNECT) dùng chung một cổng, tự động nhận diện
- **Tên người dùng điều khiển đầu ra**: ngẫu nhiên mỗi lần, hoặc cố định theo phiên, đặt thời gian sống, luân chuyển theo /64, cũng có thể chỉ định địa chỉ cụ thể
- **Triển khai bằng một lệnh**: `install` sẽ cấu hình dịch vụ systemd (chỉ cấp `CAP_NET_RAW`) và iptables, tự động tạo cổng và mật khẩu ngẫu nhiên
- Hỗ trợ Linux (gồm máy chủ cloud dùng 1:1 NAT), cũng có thể dùng như thư viện Go; macOS có thể biên dịch và chạy nhưng chưa được kiểm chứng trên tunnel thật

```
Máy khách ─SOCKS5/HTTP─→ he-ipv6-pool (ngăn xếp giao thức không gian người dùng gVisor) ─6in4 (giao thức IPv4 41)─→ máy chủ tunnel HE ─→ Internet IPv6
```

## 1. Đăng ký tunnel HE

1. Đăng ký miễn phí tài khoản tại **<https://tunnelbroker.net/register.php>**, mật khẩu sẽ được gửi tới email
2. Sau khi đăng nhập, nhấn **Create Regular Tunnel** ở bên trái:
   - **IPv4 Endpoint** điền IPv4 công cộng của máy chủ. HE sẽ ping địa chỉ này từ `66.220.2.74`, **máy chủ và security group của nhà cung cấp cloud phải cho phép ICMP vào**, nếu không sẽ báo `IP is not ICMP pingable`. Phải là IP công cộng thật, địa chỉ sau CGNAT không dùng được
   - **Chọn máy chủ tunnel: khuyến nghị khu vực châu Âu/Mỹ** (lý do xem bên dưới)
3. Sau khi tạo, mở trang **Tunnel Details** của tunnel, ghi lại:
   - **Server IPv4 Address**: tương ứng `-server`
   - **Routed /64**: tương ứng `-prefix`
   - Nhấn **Assign /48** bên cạnh **Routed /48** để được cấp thêm một /48, cũng ghi vào `-prefix`
4. Giữ **MTU** ở trang Advanced là 1480 mặc định (khớp với giá trị mặc định của chương trình)

### Vì sao khuyến nghị máy chủ tunnel ở châu Âu/Mỹ

- **Thực đo**: /48 được cấp từ node châu Á (Singapore) phía HE không hề được định tuyến, máy chủ tunnel HE trả thẳng ICMPv6 unreachable; sau khi đổi sang node Ashburn (Mỹ), cả /64 và /48 lập tức dùng được
- **Nhiều node**: HE có 30 máy chủ tunnel ở Bắc Mỹ và châu Âu (Bắc Mỹ 18, châu Âu 12), châu Á chỉ có Hồng Kông, Singapore, Tokyo 3 node. Mạng xương sống và peering của HE cũng chủ yếu ở hai khu vực này
- **Vị trí địa lý IP**: địa chỉ IPv6 của HE thường được đăng ký theo khu vực đặt máy chủ tunnel

| Máy chủ của bạn ở | Máy chủ tunnel khuyến nghị |
|---|---|
| Bắc Mỹ | Node Bắc Mỹ gần máy chủ nhất, ví dụ Ashburn, New York, Chicago, Dallas, Fremont, Los Angeles, Seattle |
| Châu Âu | Frankfurt, Amsterdam, London, Paris, Zurich, Stockholm, v.v., chọn node gần nhất |
| Châu Á, châu Đại Dương | Bờ Tây Hoa Kỳ: Fremont, Los Angeles, San Jose, Seattle, là các node châu Âu/Mỹ có độ trễ thấp nhất |

Trang tạo sẽ liệt kê IPv4 của từng máy chủ tunnel, có thể `ping` thử trên máy chủ để so sánh độ trễ trước.

## 2. Cài đặt (Linux + systemd)

```bash
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
curl -fLo /tmp/he-ipv6-pool https://github.com/wsyqhh/he-ipv6-pool/releases/latest/download/he-ipv6-pool-linux-$ARCH
chmod +x /tmp/he-ipv6-pool
sudo /tmp/he-ipv6-pool install -server <Server IPv4> -prefix <Routed /64>,<Routed /48>
```

Nâng cấp lên phiên bản mới: tải xuống theo cách tương tự, rồi chạy lại `sudo /tmp/he-ipv6-pool install`, cấu hình cũ được giữ nguyên.

`install` sẽ cài chương trình vào `/opt/he-ipv6-pool`, tạo người dùng hệ thống `hepool` và dịch vụ systemd `he-ipv6-pool`, cho phép giao thức 41 và cổng proxy trong iptables (lưu lại nếu đã cài netfilter-persistent), sau đó khởi động dịch vụ và in thông tin kết nối. Cổng, tên người dùng, mật khẩu sẽ được tạo ngẫu nhiên nếu không chỉ định.

> **Security group của nhà cung cấp cloud** (OCI Security List, AWS Security Group, v.v.) cần tự cho phép: **giao thức 41** từ HE Server IPv4 và **cổng TCP** của proxy.

| Lệnh | Tác dụng |
|---|---|
| `install [tùy chọn]` | Cài đặt. Chạy lại bằng binary mới là nâng cấp, cấu hình cũ được giữ |
| `config` | Hiển thị cấu hình hiện tại và thông tin kết nối |
| `config -listen :31234`, `config -pass mật-khẩu-mới`, v.v. | Sửa cấu hình, đồng bộ quy tắc tường lửa và khởi động lại dịch vụ |
| `uninstall` | Xóa dịch vụ, thư mục cài đặt, người dùng và quy tắc iptables tương ứng |

Tùy chọn: `-server`, `-prefix` (nhiều tiền tố phân tách bằng dấu phẩy), `-local`, `-mtu`, `-listen`, `-user`, `-pass`, chi tiết xem `install -h` hoặc [tham khảo tham số](docs/reference.md#12-tùy-chọn-của-install--config).

### Hoặc dùng Docker Compose

Image được phát hành tại `ghcr.io/wsyqhh/he-ipv6-pool` (linux/amd64, linux/arm64, sau khi nén khoảng 4MB), toàn bộ cấu hình truyền qua biến môi trường:

```bash
mkdir he-ipv6-pool && cd he-ipv6-pool
curl -fLO https://raw.githubusercontent.com/wsyqhh/he-ipv6-pool/main/compose.yaml
curl -fL https://raw.githubusercontent.com/wsyqhh/he-ipv6-pool/main/.env.example -o .env
vi .env              # điền HE_SERVER, HE_PREFIXES, PROXY_LISTEN, PROXY_USER, PROXY_PASS
docker compose up -d
```

| Biến môi trường | Mô tả |
|---|---|
| `HE_SERVER` | Server IPv4 Address trên trang chi tiết tunnel (bắt buộc) |
| `HE_PREFIXES` | Routed /64, /48, phân tách bằng dấu phẩy (bắt buộc) |
| `PROXY_LISTEN` | Địa chỉ lắng nghe proxy, ví dụ `:24601` (bắt buộc) |
| `PROXY_USER` / `PROXY_PASS` | Tên người dùng và mật khẩu proxy (bắt buộc), mật khẩu có thể tạo bằng `openssl rand -hex 16` |
| `HE_LOCAL` / `HE_MTU` | IPv4 ràng buộc cục bộ (để trống với 1:1 NAT), MTU (mặc định 1480) |

- **Phải dùng mạng host** (tệp compose đã viết sẵn): với mạng bridge, gói 6in4 phải đi qua NAT của Docker, giao thức 41 không có cổng, khi trên máy chủ có lưu lượng 6in4 khác hoặc container đổi IP sẽ mất gói do xung đột NAT
- Trong container không có `install`, tường lửa trên máy chủ phải tự cho phép: `iptables -I INPUT -p 41 -s <Server IPv4> -j ACCEPT`, và cổng proxy
- Không chạy đồng thời bản Docker và dịch vụ cài bằng `install` trên cùng một máy: hai thực thể sẽ reset kết nối của nhau

## 3. Sử dụng

```bash
# SOCKS5 và HTTP dùng cùng một cổng; hãy dùng socks5h:// để proxy phân giải tên miền
curl -x socks5h://<tên-người-dùng>:<mật-khẩu>@<máy-chủ>:<cổng> https://api64.ipify.org
curl -x http://<tên-người-dùng>:<mật-khẩu>@<máy-chủ>:<cổng> https://api64.ipify.org
```

**Thêm hậu tố sau tên người dùng để chọn IP đầu ra**, mật khẩu không đổi. Thứ tự hậu tố tùy ý, có thể kết hợp:

| Tên người dùng | Đầu ra |
|---|---|
| `alice` | Ngẫu nhiên mỗi kết nối |
| `alice-cidr-64` | Ngẫu nhiên mỗi kết nối, và mỗi lần đổi một /64 (không lặp lại trước khi dùng hết mọi /64) |
| `alice-session-abc` | Cố định vĩnh viễn một IP (không đổi cả sau khi khởi động lại dịch vụ) |
| `alice-session-abc-ttl-10` | Cố định 10 phút, sau đó đổi sang IP mới (`-ttl-` cũng có thể viết `90s`, `2h`) |
| `alice-session-abc-ttl-30-cidr-64` | Cố định 30 phút, hết hạn thì đổi sang một /64 mới |
| `alice-ttl-10` | Mọi kết nối của người dùng này dùng chung một IP, 10 phút đổi một lần |
| `alice-ip-2001-db8-abcd--1` | Chỉ định `2001:db8:abcd::1` (`:` viết thành `-`), không thể kết hợp với hậu tố khác |

Đích phải có địa chỉ IPv6 (bản ghi AAAA). Quy tắc đầy đủ, cách phiên hết hạn, mã lỗi xem **[docs/reference.md](docs/reference.md)**.

## 4. Chạy thủ công (macOS hoặc hệ thống không có systemd)

Sao chép [`config.example.json`](config.example.json) thành `config.json` và điền theo trang chi tiết tunnel (mô tả trường xem [tham khảo tham số](docs/reference.md#2-tệp-cấu-hình-configjson)), cũng có thể không viết tệp cấu hình mà dùng [biến môi trường](docs/reference.md#21-biến-môi-trường) giống compose. Sau đó:

```bash
sudo setcap cap_net_raw+ep ./he-ipv6-pool   # Linux; macOS chạy trực tiếp bằng sudo
./he-ipv6-pool -c config.json               # -v xuất log debug
```

Cũng cần tự cho phép giao thức 41: `iptables -I INPUT -p 41 -s <Server IPv4> -j ACCEPT`. Khi ở sau router gia đình, cần chuyển tiếp giao thức 41 (hoặc DMZ) tới máy này, `local_ipv4` để trống hoặc điền IP nội bộ.

## 5. Dùng như thư viện Go

```bash
go get github.com/wsyqhh/he-ipv6-pool
```

```go
import "github.com/wsyqhh/he-ipv6-pool/tunnel"

t, err := tunnel.New(tunnel.Config{
	ServerIPv4: netip.MustParseAddr("216.66.80.26"),
	Prefixes:   []netip.Prefix{netip.MustParsePrefix("2001:db8:abcd::/48")},
})
if err != nil {
	log.Fatal(err)
}
defer t.Close()

// Truyền địa chỉ nguồn zero để chọn ngẫu nhiên; cũng có thể truyền t.StickyAddr("key"), t.SpreadAddr(64) hoặc địa chỉ cụ thể
client := &http.Client{Transport: &http.Transport{
	DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return t.Dial(ctx, netip.Addr{}, network, addr)
	},
}}
```

API khác: `ListenUDP`, `ListenTCP` (nhận kết nối vào trên địa chỉ trong pool), `Resolve`, `RandomAddr`, `Contains`, `NewWithConn` (chạy tunnel trên bất kỳ `net.PacketConn` nào). Trả về `tunnel.ErrNotInPool` khi địa chỉ nguồn không nằm trong pool, `tunnel.ErrNoIPv6` khi đích không có địa chỉ IPv6, dùng `errors.Is` để kiểm tra. Proxy nằm trong gói `proxy`: `(&proxy.Proxy{Tunnel: t, Users: users}).Serve(l)`.

## 6. Xử lý sự cố

| Hiện tượng | Nguyên nhân và cách xử lý |
|---|---|
| Một số kết nối ngẫu nhiên bị timeout, hoặc một số phiên luôn thất bại | Có tiền tố chưa được HE định tuyến tới tunnel này. Log khởi động sẽ báo từng tiền tố "Kiểm tra tunnel đạt / thất bại"; cũng có thể từ bất kỳ máy nào có IPv6 `ping -6` địa chỉ trong từng tiền tố, tiền tố đã định tuyến sẽ có phản hồi |
| /48 mới được cấp không thông | Tiền tố mới của HE đôi khi cần một thời gian mới có hiệu lực, có thể dùng `scripts/check-prefix.sh` kiểm tra định kỳ (xem [tham khảo tham số](docs/reference.md#6-scriptscheck-prefixsh)). Nếu lâu vẫn không thông thì đổi sang máy chủ tunnel châu Âu/Mỹ khác để tạo lại |
| Mọi kết nối đều không thông | Kiểm tra security group đã cho phép giao thức 41 chưa; có đang đồng thời bật tunnel HE của kernel không (`iface he-ipv6 inet6 v4tunnel`), nếu có thì `ifdown he-ipv6` trước, vẫn lỗi thì `modprobe -r sit` |
| Cùng một phiên có lưu lượng cao đồng thời bị 429 hoặc timeout | Trang đích giới hạn theo IP. Mọi yêu cầu cùng một phiên dùng chung IP, hãy đổi sang `-cidr-64` không có phiên, hoặc giảm số kết nối đồng thời của mỗi phiên |
| Không mở được trang chỉ có IPv4 | Chương trình này chỉ ra IPv6, không fallback về IPv4 |
| Muốn xem IP đầu ra và lý do thất bại của từng yêu cầu | Đặt `LOG_LEVEL=debug` (hoặc `-v`), mỗi yêu cầu in một dòng; bình thường mỗi 10 phút in thống kê một lần. Xem [mô tả log](docs/reference.md#19-log-và-kiểm-tra-khởi-động) |

## 7. Hạn chế

- Chỉ ra IPv6; đích chỉ có IPv4 sẽ bị từ chối ngay
- Không hỗ trợ SOCKS5 `BIND` và phân mảnh UDP. SOCKS5 UDP dùng cổng ngẫu nhiên trên máy chủ, chỉ phù hợp dùng trên cùng máy hoặc cùng mạng nội bộ
- Không tự động cập nhật endpoint IPv4 của tunnel HE (IP động có thể dùng cron gọi update URL của tunnelbroker)
- Lệnh quản trị chỉ hỗ trợ Linux + systemd; không hỗ trợ Windows

## Biên dịch từ mã nguồn

Cần Go 1.25.5 hoặc 1.25.x mới hơn:

```bash
go install github.com/wsyqhh/he-ipv6-pool@latest
# hoặc
git clone https://github.com/wsyqhh/he-ipv6-pool && cd he-ipv6-pool
CGO_ENABLED=0 go build -o he-ipv6-pool .
```

## Phát hành

GitHub Actions sẽ chạy test, biên dịch binary Linux (amd64/arm64/arm/386/riscv64) và macOS (amd64/arm64), cùng `SHA256SUMS` phát hành lên [Releases](https://github.com/wsyqhh/he-ipv6-pool/releases):

```bash
gh workflow run release.yml -f version=v0.1.0   # gắn thẻ và phát hành trên commit mới nhất của main, cũng có thể vào trang Actions nhấn Run workflow
# hoặc
git tag v0.1.0 && git push origin v0.1.0
```

Định dạng số phiên bản là `vX.Y.Z`, bản có hậu tố (như `v0.2.0-rc1`) là bản phát hành trước. Dùng `he-ipv6-pool -version` để xem phiên bản, khi nâng cấp tải phiên bản mới rồi chạy lại `install`. Chi tiết xem [tham khảo tham số](docs/reference.md#7-phát-hành-github-actions).

## Giấy phép

[GPL-3.0](LICENSE)
