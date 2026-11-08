// Package proxy cung cấp proxy SOCKS5 và HTTP trên nền tunnel, bộ chọn trong tên người dùng quyết định IPv6 đầu ra.
package proxy

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/wsyqhh/he-ipv6-pool/tunnel"
)

const dialTimeout = 30 * time.Second

var errAuth = errors.New("proxy: xác thực thất bại")

// Proxy giữ tunnel và thông tin xác thực. Điểm vào là Serve (cùng một cổng cung cấp cả SOCKS5 và HTTP), cũng có thể dùng riêng làm http.Handler.
type Proxy struct {
	Tunnel *tunnel.Tunnel
	Users  map[string]string // tên người dùng → mật khẩu; để trống thì không yêu cầu xác thực

	sessions sessions // phiên có -ttl- hoặc -cidr-
	stats    stats
}

// source kiểm tra mật khẩu và chọn địa chỉ nguồn theo hậu tố trong tên người dùng (giá trị zero nghĩa là tunnel chọn ngẫu nhiên).
//
//	alice                       ngẫu nhiên cho mỗi kết nối
//	alice-cidr-64               ngẫu nhiên cho mỗi kết nối, nhưng luân chuyển theo subnet /64 (SpreadAddr)
//	alice-session-<id>          IP cố định: băm qua StickyAddr, không trạng thái, không đổi sau khi khởi động lại
//	alice-session-<id>-ttl-10   IP cố định, đổi mới sau 10 phút kể từ lần sử dụng đầu tiên
//	alice-session-<id>-cidr-64  IP cố định, phân bổ luân chuyển theo /64, các phiên khác nhau rơi vào các /64 khác nhau
//	alice-ttl-10                không có session: toàn bộ người dùng dùng chung một IP, đổi mới mỗi 10 phút
//	alice-ip-<addr>             chỉ định địa chỉ, '-' trong addr được coi là ':' (tên người dùng xác thực HTTP Basic không được chứa dấu hai chấm)
//
// Xác thực thất bại trả về errAuth; hậu tố không hợp lệ, IP chỉ định không hợp lệ hoặc ngoài pool trả về tunnel.ErrNotInPool.
func (p *Proxy) source(username, password string) (netip.Addr, error) {
	sel, err := parseSelector(username)
	if len(p.Users) > 0 {
		want, ok := p.Users[sel.user]
		if !ok || subtle.ConstantTimeCompare([]byte(want), []byte(password)) != 1 {
			return netip.Addr{}, errAuth
		}
	}
	if err != nil {
		return netip.Addr{}, fmt.Errorf("%w: %v", tunnel.ErrNotInPool, err)
	}
	pick := func() netip.Addr {
		if sel.cidr > 0 {
			return p.Tunnel.SpreadAddr(sel.cidr)
		}
		return p.Tunnel.RandomAddr()
	}
	switch {
	case sel.ip != "":
		a, err := netip.ParseAddr(strings.ReplaceAll(sel.ip, "-", ":"))
		if err != nil || !p.Tunnel.Contains(a) {
			return netip.Addr{}, tunnel.ErrNotInPool
		}
		return a, nil
	case !sel.sticky:
		if sel.cidr > 0 {
			return pick(), nil
		}
		return netip.Addr{}, nil
	case sel.ttl == 0 && sel.cidr == 0:
		return p.Tunnel.StickyAddr(sel.key()), nil
	}
	return p.sessions.get(sel.key(), sel.ttl, pick), nil
}

// selector là tên người dùng cơ sở và hậu tố chọn đầu ra được tách ra.
type selector struct {
	user    string
	session string        // -session-<id>
	ip      string        // -ip-<addr>
	ttl     time.Duration // -ttl-<thời lượng>
	cidr    int           // -cidr-<số bit>
	sticky  bool          // có -session- hoặc -ttl-: cần IP cố định
}

// key là định danh phiên; giữ dạng "<user>-session-<id>", IP mà tên người dùng -session- cũ ánh xạ tới không thay đổi.
func (s selector) key() string { return s.user + "-session-" + s.session }

var selectorKeys = [...]string{"session", "ip", "ttl", "cidr"}

// parseSelector tách hậu tố trong tên người dùng, thứ tự tùy ý, ngoại trừ -ip- có thể kết hợp:
//
//	<user>[-session-<id>][-ttl-<thời lượng>][-cidr-<số bit>]    hoặc    <user>-ip-<addr>
//
// Trước hậu tố đầu tiên là tên người dùng cơ sở; giá trị của mỗi hậu tố kéo dài đến hậu tố tiếp theo, nên ID phiên không được chứa các hậu tố này.
// Thời lượng là số thuần túy thì tính theo phút, cũng có thể viết 90s, 10m, 1h30m. Khi lỗi vẫn trả về tên người dùng cơ sở để dùng cho xác thực.
func parseSelector(username string) (sel selector, err error) {
	type mark struct {
		at  int
		key string
	}
	var marks []mark
	for i := 0; i < len(username); i++ {
		for _, k := range selectorKeys {
			if strings.HasPrefix(username[i:], "-"+k+"-") {
				marks = append(marks, mark{i, k})
				i += len(k) + 1 // bỏ qua tên hậu tố, dấu '-' ngay sau đó thuộc về giá trị
				break
			}
		}
	}
	sel.user = username
	if len(marks) > 0 {
		sel.user = username[:marks[0].at]
	}
	seen := map[string]bool{}
	for j, m := range marks {
		end := len(username)
		if j+1 < len(marks) {
			end = marks[j+1].at
		}
		v := username[m.at+len(m.key)+2 : end]
		if seen[m.key] {
			return sel, fmt.Errorf("hậu tố -%s- bị lặp", m.key)
		}
		seen[m.key] = true
		switch m.key {
		case "session":
			sel.session, sel.sticky = v, true
		case "ip":
			sel.ip = v
		case "ttl":
			if n, err := strconv.Atoi(v); err == nil {
				v = strconv.Itoa(n) + "m"
			}
			if sel.ttl, err = time.ParseDuration(v); err != nil || sel.ttl <= 0 {
				return sel, fmt.Errorf("-ttl-%s không hợp lệ", v)
			}
			sel.sticky = true
		case "cidr":
			if sel.cidr, err = strconv.Atoi(v); err != nil || sel.cidr < 1 || sel.cidr > 128 {
				return sel, fmt.Errorf("-cidr-%s không hợp lệ, số bit phải là 1~128", v)
			}
		}
	}
	if seen["ip"] && len(marks) > 1 {
		return sel, errors.New("-ip- không thể dùng cùng các hậu tố khác")
	}
	return sel, nil
}

// relay chuyển tiếp hai chiều: khi một chiều đọc tới EOF (err == nil), chỉ CloseWrite
// bán đóng phía bên kia, truyền EOF tới ngăn xếp giao thức đối diện mà không làm gián đoạn chiều còn lại đang truyền
// (ví dụ client gửi xong yêu cầu thì bán đóng, nhưng vẫn cần nhận hết phản hồi); các kiểu hỗ trợ CloseWrite hiện chỉ có
// *net.TCPConn và *TCPConn của gonet, khi không hỗ trợ thì thoái hóa thành đóng toàn bộ. Khi một chiều gặp lỗi thì
// đóng thẳng cả hai đầu, giải tỏa tắc nghẽn của chiều còn lại. Sau khi cả hai chiều kết thúc mới đóng hai đầu để kết thúc.
// Trả về số byte mà từng chiều a→b và b→a đã chuyển tiếp.
func relay(a, b net.Conn) (ab, ba int64) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn, n *int64) {
		var err error
		*n, err = io.Copy(dst, src)
		if err == nil {
			if cw, ok := dst.(interface{ CloseWrite() error }); ok {
				cw.CloseWrite()
			} else {
				dst.Close()
			}
		} else {
			a.Close()
			b.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b, &ba)
	go cp(b, a, &ab)
	<-done
	<-done
	a.Close()
	b.Close()
	return ab, ba
}
