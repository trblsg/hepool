package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"sync/atomic"

	"github.com/wsyqhh/he-ipv6-pool/tunnel"
)

// ServeHTTP triển khai proxy HTTP: tunnel CONNECT, và chuyển tiếp các yêu cầu http:// thông thường dạng absolute-URI.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Mượn cách phân tích Basic của thư viện chuẩn: coi Proxy-Authorization như Authorization
	user, pass, _ := (&http.Request{Header: http.Header{"Authorization": r.Header.Values("Proxy-Authorization")}}).BasicAuth()
	rec := newRequest("http", r.RemoteAddr)
	rec.user, rec.dst = user, r.Host
	src, err := p.source(user, pass)
	switch {
	case errors.Is(err, errAuth):
		w.Header().Set("Proxy-Authenticate", `Basic realm="he-ipv6-pool"`)
		http.Error(w, "Cần xác thực proxy", http.StatusProxyAuthRequired)
	case err != nil:
		http.Error(w, err.Error(), http.StatusForbidden)
	case r.Method == http.MethodConnect:
		rec.proto = "http-connect"
		err = p.httpConnect(w, r, src, rec)
	case !r.URL.IsAbs():
		err = errors.New("không phải yêu cầu proxy dạng absolute-URI")
		http.Error(w, "Đây là máy chủ proxy, yêu cầu cần dùng dạng absolute-URI", http.StatusBadRequest)
	default:
		err = p.forward(w, r, src, rec)
	}
	p.finish(rec, err)
}

func (p *Proxy) httpConnect(w http.ResponseWriter, r *http.Request, src netip.Addr, rec *request) error {
	ctx, cancel := context.WithTimeout(r.Context(), dialTimeout)
	defer cancel()
	up, err := p.Tunnel.Dial(ctx, src, "tcp", r.Host)
	if err != nil {
		http.Error(w, err.Error(), dialStatus(err))
		return err
	}
	defer up.Close()
	rec.src = localAddr(up)
	c, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return err
	}
	defer c.Close()
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return err
	}
	// Client có thể đã gửi dữ liệu trước khi nhận 200, phần đã bị bufio đọc mất trước tiên chuyển cho upstream
	var early int64
	if n := brw.Reader.Buffered(); n > 0 {
		b, _ := brw.Reader.Peek(n)
		if _, err := up.Write(b); err != nil {
			return err
		}
		early = int64(n)
	}
	defer p.active()()
	rec.up, rec.down = relay(c, up)
	rec.up += early
	return nil
}

// forward chuyển tiếp các yêu cầu http:// thông thường. Mỗi yêu cầu tạo kết nối upstream mới (DisableKeepAlives),
// tránh tái sử dụng kết nối được thiết lập với địa chỉ nguồn khác.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, src netip.Addr, rec *request) error {
	var up, down atomic.Int64
	var ferr error
	rp := &httputil.ReverseProxy{
		// Bản thân yêu cầu đã là absolute-URI, không cần viết lại; không gọi SetXForwarded, không lộ IP của client
		Rewrite: func(*httputil.ProxyRequest) {},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				c, err := p.Tunnel.Dial(ctx, src, network, addr)
				if err != nil {
					return nil, err
				}
				rec.src = localAddr(c)
				return countConn{Conn: c, up: &up, down: &down}, nil
			},
			DisableKeepAlives: true,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			ferr = err
			http.Error(w, err.Error(), dialStatus(err))
		},
	}
	defer p.active()()
	rp.ServeHTTP(w, r)
	rec.up, rec.down = up.Load(), down.Load()
	return ferr
}

// dialStatus ánh xạ lỗi quay số thành mã trạng thái HTTP.
func dialStatus(err error) int {
	if errors.Is(err, tunnel.ErrNotInPool) {
		return http.StatusForbidden
	}
	return http.StatusBadGateway
}
