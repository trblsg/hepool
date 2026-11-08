package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wsyqhh/he-ipv6-pool/tunnel"
)

// failKind là phân loại kết quả yêu cầu, dùng chung cho thống kê, log và mã phản hồi SOCKS5.
type failKind int

const (
	kindOK       failKind = iota
	kindAuth              // sai tên người dùng hoặc mật khẩu
	kindRejected          // hậu tố tên người dùng không hợp lệ, hoặc địa chỉ đầu ra chỉ định không nằm trong pool
	kindNoIPv6            // đích không có địa chỉ IPv6, hoặc phân giải DNS thất bại
	kindRefused           // đích từ chối kết nối
	kindTimeout           // kết nối tới đích quá thời gian
	kindOther
	numKinds
)

var kindNames = [numKinds]string{"ok", "auth", "rejected", "no_ipv6", "refused", "timeout", "other"}

func (k failKind) String() string { return kindNames[k] }

func classify(err error) failKind {
	switch {
	case err == nil:
		return kindOK
	case errors.Is(err, errAuth):
		return kindAuth
	case errors.Is(err, tunnel.ErrNotInPool):
		return kindRejected
	case errors.Is(err, tunnel.ErrNoIPv6):
		return kindNoIPv6
	// gonet chuyển lỗi tcpip thành văn bản thuần, chỉ có thể khớp theo văn bản
	case strings.Contains(err.Error(), "refused"):
		return kindRefused
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "timed out"):
		return kindTimeout
	}
	return kindOther
}

// Stats là thống kê lũy kế từ khi proxy khởi động.
type Stats struct {
	Count    [numKinds]uint64 // số yêu cầu theo phân loại kết quả
	Active   int64            // số kết nối đang được chuyển tiếp
	Up, Down uint64           // số byte client gửi tới đích, đích gửi trả client
}

// Requests trả về tổng số yêu cầu.
func (s Stats) Requests() uint64 {
	var n uint64
	for _, c := range s.Count {
		n += c
	}
	return n
}

// Sub trả về mức tăng của s so với prev; Active là giá trị hiện tại, không trừ đi.
func (s Stats) Sub(prev Stats) Stats {
	for k := range s.Count {
		s.Count[k] -= prev.Count[k]
	}
	s.Up -= prev.Up
	s.Down -= prev.Down
	return s
}

// Attrs trả về các cặp khóa-giá trị dùng cho slog, chỉ liệt kê các phân loại khác 0.
func (s Stats) Attrs() []any {
	a := []any{"requests", s.Requests()}
	for k, n := range s.Count {
		if n > 0 {
			a = append(a, failKind(k).String(), n)
		}
	}
	return append(a, "active", s.Active, "up", size(s.Up), "down", size(s.Down))
}

func size(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	v, i := float64(n), -1
	for v >= unit && i < 4 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.1f%ciB", v, "KMGTP"[i])
}

type stats struct {
	count    [numKinds]atomic.Uint64
	active   atomic.Int64
	up, down atomic.Uint64
}

// Stats trả về ảnh chụp thống kê lũy kế.
func (p *Proxy) Stats() Stats {
	var s Stats
	for k := range s.Count {
		s.Count[k] = p.stats.count[k].Load()
	}
	s.Active = p.stats.active.Load()
	s.Up, s.Down = p.stats.up.Load(), p.stats.down.Load()
	return s
}

// request ghi lại một yêu cầu proxy. Chỉ được tạo sau khi client gửi thông tin xác thực (hoặc không cần xác thực),
// lỗi giao thức ở giai đoạn bắt tay (ví dụ quét cổng) không được tính.
type request struct {
	proto    string // socks5, socks5-udp, http-connect, http
	client   string
	user     string // tên người dùng đầy đủ (gồm hậu tố), không ghi mật khẩu
	dst      string
	src      netip.Addr // địa chỉ đầu ra thực tế được dùng
	start    time.Time
	up, down int64
}

func newRequest(proto, client string) *request {
	return &request{proto: proto, client: client, start: time.Now()}
}

// finish tính vào thống kê và ghi một dòng ở mức debug.
func (p *Proxy) finish(r *request, err error) {
	k := classify(err)
	p.stats.count[k].Add(1)
	p.stats.up.Add(uint64(r.up))
	p.stats.down.Add(uint64(r.down))
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	attrs := []any{"proto", r.proto, "client", r.client, "user", r.user, "dst", r.dst}
	if r.src.IsValid() {
		attrs = append(attrs, "src", r.src)
	}
	attrs = append(attrs, "dur", time.Since(r.start).Round(time.Millisecond), "up", r.up, "down", r.down)
	if err != nil {
		slog.Debug("Yêu cầu thất bại", append(attrs, "result", k.String(), "err", err)...)
		return
	}
	slog.Debug("Yêu cầu hoàn tất", attrs...)
}

// active tính vào số kết nối đang hoạt động trong thời gian chuyển tiếp, hàm trả về được gọi khi chuyển tiếp kết thúc.
func (p *Proxy) active() func() {
	p.stats.active.Add(1)
	return func() { p.stats.active.Add(-1) }
}

func localAddr(c net.Conn) netip.Addr {
	ap, _ := netip.ParseAddrPort(c.LocalAddr().String())
	return ap.Addr()
}

// countConn đếm số byte đọc/ghi của kết nối upstream, dùng cho thống kê lưu lượng chuyển tiếp HTTP thông thường.
type countConn struct {
	net.Conn
	up, down *atomic.Int64
}

func (c countConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.down.Add(int64(n))
	return n, err
}

func (c countConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.up.Add(int64(n))
	return n, err
}
