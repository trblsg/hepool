package proxy

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// Serve cung cấp đồng thời proxy SOCKS5 và HTTP trên l: đọc byte đầu tiên của mỗi kết nối, 0x05 giao cho SOCKS5,
// phần còn lại (yêu cầu HTTP luôn bắt đầu bằng tên phương thức ASCII) giao cho http.Server nội bộ. Chỉ trả về khi l đóng;
// các lỗi Accept ngoài việc l đóng (ví dụ lỗi tạm thời như EMFILE) chỉ ghi cảnh báo và tiếp tục phục vụ.
func (p *Proxy) Serve(l net.Listener) error {
	hl := &chanListener{ch: make(chan net.Conn), done: make(chan struct{}), addr: l.Addr()}
	defer hl.Close()
	go (&http.Server{Handler: p, ReadHeaderTimeout: dialTimeout}).Serve(hl)
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			slog.Warn("accept thất bại", "err", err)
			time.Sleep(100 * time.Millisecond) // ponytail: backoff cố định 100ms, khi cần thì đổi sang backoff lũy thừa
			continue
		}
		go p.dispatch(c, hl)
	}
}

// dispatch phân loại kết nối cho SOCKS5 hoặc HTTP dựa trên byte đầu tiên.
func (p *Proxy) dispatch(c net.Conn, hl *chanListener) {
	var first [1]byte
	c.SetReadDeadline(time.Now().Add(dialTimeout))
	if _, err := io.ReadFull(c, first[:]); err != nil {
		c.Close()
		return
	}
	c.SetReadDeadline(time.Time{})
	pc := &peekConn{Conn: c, r: io.MultiReader(bytes.NewReader(first[:]), c)}
	if first[0] == socksVer {
		defer pc.Close()
		if err := p.socks5(pc); err != nil {
			slog.Debug("bắt tay socks5 thất bại", "client", c.RemoteAddr(), "err", err)
		}
		return
	}
	select {
	case hl.ch <- pc:
	case <-hl.done:
		c.Close()
	}
}

// peekConn đưa byte đầu tiên đã bị đọc khi phân loại trở lại luồng đọc.
type peekConn struct {
	net.Conn
	r io.Reader
}

func (c *peekConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// CloseWrite chuyển tiếp xuống kết nối bên dưới: interface nhúng không thăng cấp CloseWrite, việc bán đóng của relay phụ thuộc vào nó.
func (c *peekConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}

// chanListener giao các kết nối HTTP đã được phân loại cho http.Server.
type chanListener struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
	addr net.Addr
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return l.addr }
