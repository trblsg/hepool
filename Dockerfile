# syntax=docker/dockerfile:1
# Biên dịch chéo trên kiến trúc của máy build, image đa kiến trúc không cần giả lập QEMU
FROM --platform=$BUILDPLATFORM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH VERSION
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /he-ipv6-pool .

# Binary tĩnh, chỉ chuyển tiếp, không khởi tạo TLS, không cần chứng chỉ CA và shell
FROM scratch
COPY --from=build /he-ipv6-pool /he-ipv6-pool
ENTRYPOINT ["/he-ipv6-pool"]
CMD ["-c", "/etc/he-ipv6-pool/config.json"]
