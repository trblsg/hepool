## Cài đặt nhanh

Tải binary đúng kiến trúc trong mục **Assets** bên dưới và đối chiếu mã SHA256 với tệp `SHA256SUMS`.

### Linux + systemd

```bash
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/; s/armv7l/arm/; s/i686/386/; s/riscv64/riscv64/')
curl -fLo /tmp/he-ipv6-pool https://github.com/wsyqhh/he-ipv6-pool/releases/latest/download/he-ipv6-pool-linux-$ARCH
chmod +x /tmp/he-ipv6-pool
sudo /tmp/he-ipv6-pool install -server <Server IPv4> -prefix <Routed /64>,<Routed /48>
```

### Void Linux (runit)

Void Linux không dùng systemd nên lệnh `install` không hoạt động. Xem hướng dẫn cài thủ công đầy đủ tại
[README – Void Linux (runit)](https://github.com/wsyqhh/he-ipv6-pool/blob/main/README.md#void-linux-runit).

### Docker

Job build image Docker đang tạm tắt trong workflow. Image của các bản phát hành trước và cách dùng Docker Compose xem tại
[README – Docker Compose](https://github.com/wsyqhh/he-ipv6-pool/blob/main/README.md#hoặc-dùng-docker-compose).
