package proxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/wsyqhh/he-ipv6-pool/tunnel"
)

// Hằng số SOCKS5 (RFC 1928).
const (
	socksVer     = 5
	cmdConnect   = 1
	cmdAssociate = 3
	atypIPv4     = 1
	atypDomain   = 3
	atypIPv6     = 4

	repOK              = 0
	repFail            = 1
	repNotAllowed      = 2
	repHostUnreach     = 4
	repRefused         = 5
	repCmdUnsupported  = 7
	repAtypUnsupported = 8
)

// socks5 xử lý một kết nối SOCKS5. Lỗi trả về chỉ gồm lỗi bắt tay trước khi xác thực;
// kết quả sau xác thực đều giao cho finish để tính thống kê và ghi log.
func (p *Proxy) socks5(c net.Conn) (err error) {
	c.SetDeadline(time.Now().Add(dialTimeout)) // thời gian chờ bắt tay, xóa trước khi vào chuyển tiếp
	rec := newRequest("socks5", c.RemoteAddr().String())
	src, srcErr := p.socksAuth(c, rec)
	switch {
	case errors.Is(srcErr, errAuth):
		p.finish(rec, srcErr)
		return nil
	case srcErr != nil && !errors.Is(srcErr, tunnel.ErrNotInPool):
		return srcErr
	}
	defer func() { p.finish(rec, err); err = nil }()
	// Yêu cầu: VER CMD RSV ATYP DST.ADDR DST.PORT
	var h [3]byte
	if _, err := io.ReadFull(c, h[:]); err != nil {
		return err
	}
	dst, err := readAddr(c)
	rec.dst = dst
	if err != nil {
		writeReply(c, repAtypUnsupported, netip.AddrPort{})
		return err
	}
	if srcErr != nil {
		writeReply(c, repNotAllowed, netip.AddrPort{})
		return srcErr
	}
	switch h[1] {
	case cmdConnect:
		return p.socksConnect(c, src, rec)
	case cmdAssociate:
		rec.proto = "socks5-udp"
		return p.socksAssociate(c, src, rec)
	}
	writeReply(c, repCmdUnsupported, netip.AddrPort{})
	return fmt.Errorf("lệnh không được hỗ trợ %d", h[1])
}

// socksAuth hoàn tất thương lượng phương thức và xác thực (RFC 1928 §3, RFC 1929), trả về địa chỉ nguồn, tên người dùng ghi vào rec.
// Trả về tunnel.ErrNotInPool nghĩa là xác thực đạt nhưng IP chỉ định không hợp lệ, để đến giai đoạn yêu cầu mới trả lời 0x02.
func (p *Proxy) socksAuth(c net.Conn, rec *request) (netip.Addr, error) {
	var h [2]byte
	if _, err := io.ReadFull(c, h[:]); err != nil {
		return netip.Addr{}, err
	}
	if h[0] != socksVer {
		return netip.Addr{}, fmt.Errorf("phiên bản SOCKS không được hỗ trợ %d", h[0])
	}
	methods := make([]byte, h[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return netip.Addr{}, err
	}
	switch {
	case bytes.IndexByte(methods, 2) >= 0:
		// Xác thực tên người dùng/mật khẩu; khi chưa cấu hình users cũng ưu tiên dùng nó để lấy bộ chọn trong tên người dùng
		if _, err := c.Write([]byte{socksVer, 2}); err != nil {
			return netip.Addr{}, err
		}
		user, pass, err := readUserPass(c)
		if err != nil {
			return netip.Addr{}, err
		}
		rec.user = user
		src, err := p.source(user, pass)
		if errors.Is(err, errAuth) {
			c.Write([]byte{1, 1})
			return netip.Addr{}, err
		}
		if _, werr := c.Write([]byte{1, 0}); werr != nil {
			return netip.Addr{}, werr
		}
		return src, err
	case len(p.Users) == 0 && bytes.IndexByte(methods, 0) >= 0:
		_, err := c.Write([]byte{socksVer, 0})
		return netip.Addr{}, err
	}
	c.Write([]byte{socksVer, 0xff})
	return netip.Addr{}, errors.New("không có phương thức xác thực nào được chấp nhận")
}

// readUserPass đọc VER ULEN UNAME PLEN PASSWD của RFC 1929.
func readUserPass(r io.Reader) (user, pass string, err error) {
	var b [2]byte
	if _, err = io.ReadFull(r, b[:]); err != nil {
		return
	}
	u := make([]byte, b[1])
	if _, err = io.ReadFull(r, u); err != nil {
		return
	}
	if _, err = io.ReadFull(r, b[:1]); err != nil {
		return
	}
	pw := make([]byte, b[0])
	if _, err = io.ReadFull(r, pw); err != nil {
		return
	}
	return string(u), string(pw), nil
}

// readAddr đọc ATYP DST.ADDR DST.PORT, trả về host:port.
func readAddr(r io.Reader) (string, error) {
	var atyp [1]byte
	if _, err := io.ReadFull(r, atyp[:]); err != nil {
		return "", err
	}
	var host string
	switch atyp[0] {
	case atypIPv4, atypIPv6:
		b := make([]byte, 4)
		if atyp[0] == atypIPv6 {
			b = make([]byte, 16)
		}
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		a, _ := netip.AddrFromSlice(b)
		host = a.String()
	case atypDomain:
		var n [1]byte
		if _, err := io.ReadFull(r, n[:]); err != nil {
			return "", err
		}
		b := make([]byte, n[0])
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		host = string(b)
	default:
		return "", fmt.Errorf("kiểu địa chỉ không được hỗ trợ %d", atyp[0])
	}
	var port [2]byte
	if _, err := io.ReadFull(r, port[:]); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:])))), nil
}

// appendAddr thêm địa chỉ theo định dạng SOCKS5 (ATYP ADDR PORT); giá trị zero ghi thành IPv4 0.0.0.0:0.
func appendAddr(b []byte, ap netip.AddrPort) []byte {
	a := ap.Addr().Unmap()
	switch {
	case a.Is4():
		b = append(b, atypIPv4)
		b = append(b, a.AsSlice()...)
	case a.Is6():
		b = append(b, atypIPv6)
		b = append(b, a.AsSlice()...)
	default:
		b = append(b, atypIPv4, 0, 0, 0, 0)
	}
	return binary.BigEndian.AppendUint16(b, ap.Port())
}

func writeReply(w io.Writer, rep byte, bind netip.AddrPort) error {
	_, err := w.Write(appendAddr([]byte{socksVer, rep, 0}, bind))
	return err
}

// dialReply ánh xạ lỗi quay số thành mã phản hồi SOCKS5.
func dialReply(err error) byte {
	switch classify(err) {
	case kindRejected:
		return repNotAllowed
	case kindNoIPv6:
		return repHostUnreach
	case kindRefused:
		return repRefused
	}
	return repFail
}

func (p *Proxy) socksConnect(c net.Conn, src netip.Addr, rec *request) error {
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	up, err := p.Tunnel.Dial(ctx, src, "tcp", rec.dst)
	if err != nil {
		writeReply(c, dialReply(err), netip.AddrPort{})
		return err
	}
	defer up.Close()
	bind, _ := netip.ParseAddrPort(up.LocalAddr().String())
	rec.src = bind.Addr()
	if err := writeReply(c, repOK, bind); err != nil {
		return err
	}
	c.SetDeadline(time.Time{})
	defer p.active()()
	rec.up, rec.down = relay(c, up)
	return nil
}

// socksAssociate xử lý UDP ASSOCIATE (RFC 1928 §7): socket UDP cục bộ giao tiếp với client,
// socket UDP phía tunnel dùng src làm địa chỉ nguồn để giao tiếp với đích, đóng cùng lúc khi kết nối điều khiển ngắt.
func (p *Proxy) socksAssociate(c net.Conn, src netip.Addr, rec *request) error {
	if !src.IsValid() {
		src = p.Tunnel.RandomAddr()
	}
	rec.src = src
	out, err := p.Tunnel.ListenUDP(netip.AddrPortFrom(src, 0))
	if err != nil {
		writeReply(c, dialReply(err), netip.AddrPort{})
		return err
	}
	defer out.Close()
	// Liên kết với địa chỉ cục bộ của kết nối điều khiển, để khi lắng nghe 0.0.0.0 vẫn trả về được một địa chỉ hợp lệ
	in, err := net.ListenUDP("udp", &net.UDPAddr{IP: c.LocalAddr().(*net.TCPAddr).IP})
	if err != nil {
		writeReply(c, repFail, netip.AddrPort{})
		return err
	}
	defer in.Close()
	if err := writeReply(c, repOK, in.LocalAddr().(*net.UDPAddr).AddrPort()); err != nil {
		return err
	}
	c.SetDeadline(time.Time{})

	clientIP := c.RemoteAddr().(*net.TCPAddr).AddrPort().Addr().Unmap()
	var client atomic.Pointer[net.UDPAddr] // địa chỉ client gửi gói gần nhất
	var up, down atomic.Int64
	defer p.active()()

	go func() { // đích → client
		buf := make([]byte, 65535)
		for {
			n, from, err := out.ReadFrom(buf)
			if err != nil {
				return
			}
			if to := client.Load(); to != nil {
				pkt := appendAddr([]byte{0, 0, 0}, from.(*net.UDPAddr).AddrPort())
				in.WriteToUDP(append(pkt, buf[:n]...), to)
				down.Add(int64(n))
			}
		}
	}()
	go func() { // client → đích
		buf := make([]byte, 65535)
		for {
			n, from, err := in.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if from.AddrPort().Addr().Unmap() != clientIP || n < 4 || buf[2] != 0 {
				continue // chỉ chấp nhận client thuộc kết nối điều khiển; không hỗ trợ phân mảnh
			}
			r := bytes.NewReader(buf[3:n])
			dst, err := readAddr(r)
			if err != nil {
				continue
			}
			// ponytail: đích dạng tên miền được phân giải lại mỗi gói, khi cần thì thêm cache
			ap, err := tunnel.Resolve(context.Background(), dst)
			if err != nil {
				continue
			}
			client.Store(from)
			out.WriteTo(buf[n-r.Len():n], net.UDPAddrFromAddrPort(ap))
			up.Add(int64(r.Len()))
		}
	}()
	io.Copy(io.Discard, c) // chặn cho đến khi kết nối điều khiển ngắt
	rec.up, rec.down = up.Load(), down.Load()
	return nil
}
