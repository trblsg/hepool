package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wsyqhh/he-ipv6-pool/tunnel"
)

var remoteAddr = netip.MustParseAddr("2001:db8:ffff::1")

func listenTCP(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func listenUDP(t *testing.T) net.PacketConn {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// setup dựng mô hình kiểm thử: tunnel cần kiểm thử local (pool 2001:db8:1::/48) kết nối qua UDP tới tunnel remote đóng vai Internet.
// Trên remote chạy: cổng 80 HTTP (nội dung phản hồi là IP của bên yêu cầu), cổng 7 TCP echo, cổng 7 UDP (trả lời địa chỉ bên gửi),
// cổng 8 TCP (sau khi đọc tới EOF ghi lại "resp:" + nội dung nhận được, dùng để kiểm tra bán đóng).
func setup(t *testing.T, users map[string]string) (*tunnel.Tunnel, *Proxy) {
	t.Helper()
	a, b := listenUDP(t), listenUDP(t)
	local, err := tunnel.NewWithConn(tunnel.Config{Prefixes: []netip.Prefix{netip.MustParsePrefix("2001:db8:1::/48")}}, a, b.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	remote, err := tunnel.NewWithConn(tunnel.Config{Prefixes: []netip.Prefix{netip.MustParsePrefix("2001:db8:ffff::/64")}}, b, a.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { local.Close(); remote.Close() })

	web, err := remote.ListenTCP(netip.AddrPortFrom(remoteAddr, 80))
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(web, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, netip.MustParseAddrPort(r.RemoteAddr).Addr().String())
	}))

	echo, err := remote.ListenTCP(netip.AddrPortFrom(remoteAddr, 7))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()

	halfClose, err := remote.ListenTCP(netip.AddrPortFrom(remoteAddr, 8))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := halfClose.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				b, _ := io.ReadAll(c)
				io.WriteString(c, "resp:"+string(b))
				c.Close()
			}(c)
		}
	}()

	udp, err := remote.ListenUDP(netip.AddrPortFrom(remoteAddr, 7))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			_, from, err := udp.ReadFrom(buf)
			if err != nil {
				return
			}
			udp.WriteTo([]byte(from.String()), from)
		}
	}()
	return local, &Proxy{Tunnel: local, Users: users}
}

// serveProxy chạy p.Serve trên cổng ngẫu nhiên (SOCKS5 và HTTP dùng chung), trả về địa chỉ lắng nghe.
func serveProxy(t *testing.T, p *Proxy) string {
	l := listenTCP(t)
	go p.Serve(l)
	return l.Addr().String()
}

// get yêu cầu dịch vụ HTTP trên remote qua proxyURL, trả về mã trạng thái và nội dung phản hồi (IP nguồn mà remote thấy).
// Khi kết nối thất bại, mã trạng thái là 0 và nội dung là thông tin lỗi.
func get(t *testing.T, proxyURL string) (int, string) {
	t.Helper()
	u, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true}, Timeout: 5 * time.Second}
	resp, err := c.Get("http://[2001:db8:ffff::1]/")
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestParseSelector(t *testing.T) {
	cases := []struct {
		in   string
		want selector
	}{
		{"alice", selector{user: "alice"}},
		{"", selector{}},
		{"alice-session-abc", selector{user: "alice", session: "abc", sticky: true}},
		{"alice-ip-2001-db8-1--42", selector{user: "alice", ip: "2001-db8-1--42"}},
		{"alice-cidr-64", selector{user: "alice", cidr: 64}},
		{"alice-ttl-10", selector{user: "alice", ttl: 10 * time.Minute, sticky: true}},
		// Kết hợp, thứ tự tùy ý; ID phiên có thể chứa -; dấu - trong tên người dùng không bị ảnh hưởng
		{"my-name-session-a-b-ttl-10-cidr-64", selector{user: "my-name", session: "a-b", ttl: 10 * time.Minute, cidr: 64, sticky: true}},
		{"alice-cidr-56-ttl-1h30m-session-x", selector{user: "alice", session: "x", ttl: 90 * time.Minute, cidr: 56, sticky: true}},
		{"alice-session-ip-x", selector{user: "alice", session: "ip-x", sticky: true}}, // ngay sau tên hậu tố không được coi là hậu tố mới
	}
	for _, c := range cases {
		if got, err := parseSelector(c.in); err != nil || got != c.want {
			t.Errorf("parseSelector(%q) = %+v, %v; mong đợi %+v", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{
		"alice-ip-2001-db8-1--1-session-x", // -ip- không thể kết hợp
		"alice-session-a-session-b",        // lặp
		"alice-cidr-0", "alice-cidr-129", "alice-cidr-x",
		"alice-ttl-0", "alice-ttl-x", "alice-ttl--5",
	} {
		sel, err := parseSelector(in)
		if err == nil || sel.user != "alice" {
			t.Errorf("parseSelector(%q) phải báo lỗi và vẫn tách ra tên người dùng: %+v, %v", in, sel, err)
		}
	}
}

// Tổ hợp phiên, thời gian sống, cidr: cùng một phiên có IP cố định trong thời gian sống, hết hạn thì đổi mới; có cidr thì các phiên khác nhau rơi vào các subnet khác nhau.
func TestSessionTTL(t *testing.T) {
	local, p := setup(t, map[string]string{"alice": "pw"})
	addr := serveProxy(t, p)
	ip := func(user string) string {
		t.Helper()
		code, body := get(t, "socks5://"+user+":pw@"+addr)
		if code != 200 || !local.Contains(netip.MustParseAddr(body)) {
			t.Fatalf("%s: %d %s", user, code, body)
		}
		return body
	}

	// Việc đổi mới khi hết hạn do TestSessions bao phủ: dưới -race mỗi yêu cầu mất vài trăm mili giây, ở đây không thể canh thời gian chính xác
	if a, b := ip("alice-session-t-ttl-10"), ip("alice-session-t-ttl-10"); a != b {
		t.Fatalf("Trong thời gian sống phải cố định: %s %s", a, b)
	}
	if a, b := ip("alice-ttl-10"), ip("alice-ttl-10"); a != b {
		t.Fatalf("Chỉ có ttl thì toàn bộ người dùng dùng chung một IP: %s %s", a, b)
	}

	seen := map[netip.Prefix]bool{}
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		u := "alice-session-" + id + "-cidr-64"
		a, b := ip(u), ip(u)
		s := netip.PrefixFrom(netip.MustParseAddr(a), 64).Masked()
		if a != b || seen[s] {
			t.Fatalf("%s: %s %s, subnet trùng %v", u, a, b, seen[s])
		}
		seen[s] = true
	}

	for _, bad := range []string{"alice-ip-2001-db8-1--1-session-x", "alice-ttl-0"} {
		if code, body := get(t, "http://"+bad+":pw@"+addr); code != http.StatusForbidden {
			t.Errorf("%s phải 403: %d %s", bad, code, body)
		}
	}
}

// Thời gian sống tính từ lần dùng đầu tiên, việc sử dụng không kéo dài nó, hết hạn thì đổi địa chỉ mới; phiên hết hạn được dọn khi bảng tăng.
func TestSessions(t *testing.T) {
	n := 0
	pick := func() netip.Addr { n++; return netip.AddrFrom16([16]byte{0: 0x20, 15: byte(n)}) }
	var s sessions
	a := s.get("k", 200*time.Millisecond, pick)
	time.Sleep(50 * time.Millisecond)
	if b := s.get("k", 200*time.Millisecond, pick); b != a {
		t.Fatalf("Trong thời gian sống phải dùng lại: %s %s", a, b)
	}
	time.Sleep(200 * time.Millisecond)
	if b := s.get("k", 200*time.Millisecond, pick); b == a {
		t.Fatalf("Sau khi hết hạn phải đổi địa chỉ mới: %s", b)
	}

	var big sessions
	for i := range 1024 {
		big.get(strconv.Itoa(i), time.Nanosecond, pick)
	}
	time.Sleep(time.Millisecond)
	big.get("new", time.Minute, pick)
	if len(big.m) != 1 {
		t.Fatalf("Phiên hết hạn chưa được dọn: còn lại %d", len(big.m))
	}
}

// SOCKS5 và HTTP dùng chung một cổng: phân loại theo byte đầu tiên, cả hai loại client đều dùng được;
// mọi dữ liệu không bắt đầu bằng 0x05 đều giao cho máy chủ HTTP xử lý (dữ liệu rác nhận 400).
func TestMixedPort(t *testing.T) {
	local, p := setup(t, map[string]string{"alice": "pw"})
	addr := serveProxy(t, p)
	for _, scheme := range []string{"socks5", "http"} {
		if code, body := get(t, scheme+"://alice:pw@"+addr); code != 200 || !local.Contains(netip.MustParseAddr(body)) {
			t.Fatalf("%s: %d %s", scheme, code, body)
		}
	}
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(c, "\x01garbage\r\n\r\n")
	line, _ := bufio.NewReader(c).ReadString('\n')
	if !strings.HasPrefix(line, "HTTP/1.1 400") {
		t.Fatalf("Dữ liệu rác phải được máy chủ HTTP trả 400, nhận được %q", line)
	}
}

// -cidr-<số bit>: đầu ra ngẫu nhiên luân chuyển subnet theo /số bit, các kết nối liên tiếp không rơi vào cùng một subnet; số bit không hợp lệ thì từ chối.
func TestCIDR(t *testing.T) {
	local, p := setup(t, map[string]string{"alice": "pw"})
	addr := serveProxy(t, p)
	seen := map[netip.Prefix]bool{}
	for _, scheme := range []string{"socks5", "http", "socks5", "http"} {
		code, body := get(t, scheme+"://alice-cidr-64:pw@"+addr)
		if code != 200 || !local.Contains(netip.MustParseAddr(body)) {
			t.Fatalf("%s: %d %s", scheme, code, body)
		}
		s := netip.PrefixFrom(netip.MustParseAddr(body), 64).Masked()
		if seen[s] {
			t.Fatalf("%s đã dùng lại subnet %s", body, s)
		}
		seen[s] = true
	}
	for _, bad := range []string{"0", "129", "x"} {
		if code, body := get(t, "http://alice-cidr-"+bad+":pw@"+addr); code != http.StatusForbidden {
			t.Errorf("cidr-%s phải 403: %d %s", bad, code, body)
		}
		if code, _ := get(t, "socks5://alice-cidr-"+bad+":pw@"+addr); code != 0 {
			t.Errorf("socks5 cidr-%s phải thất bại: %d", bad, code)
		}
	}
}

func TestSOCKS5(t *testing.T) {
	local, p := setup(t, map[string]string{"alice": "pw"})
	addr := serveProxy(t, p)

	if code, body := get(t, "socks5://alice:pw@"+addr); code != 200 || !local.Contains(netip.MustParseAddr(body)) {
		t.Fatalf("Ngẫu nhiên: %d %s", code, body)
	}
	want := local.StickyAddr("alice-session-s1").String()
	for range 2 {
		if code, body := get(t, "socks5://alice-session-s1:pw@"+addr); code != 200 || body != want {
			t.Fatalf("session: %d %s, mong đợi %s", code, body, want)
		}
	}
	if code, body := get(t, "socks5://alice-ip-2001-db8-1--42:pw@"+addr); code != 200 || body != "2001:db8:1::42" {
		t.Fatalf("IP chỉ định: %d %s", code, body)
	}
	if code, body := get(t, "socks5://alice-ip-2001-db8-2--1:pw@"+addr); code != 0 {
		t.Fatalf("IP chỉ định ngoài pool phải thất bại: %d %s", code, body)
	}
	if code, body := get(t, "socks5://alice:wrong@"+addr); code != 0 {
		t.Fatalf("Sai mật khẩu phải thất bại: %d %s", code, body)
	}
}

func TestSOCKS5NoAuth(t *testing.T) {
	local, p := setup(t, nil)
	addr := serveProxy(t, p)
	if code, body := get(t, "socks5://"+addr); code != 200 || !local.Contains(netip.MustParseAddr(body)) {
		t.Fatalf("Không xác thực: %d %s", code, body)
	}
	if code, body := get(t, "socks5://x-session-a:any@"+addr); code != 200 || body != local.StickyAddr("x-session-a").String() {
		t.Fatalf("Không xác thực + session: %d %s", code, body)
	}
}

func TestSOCKS5UDP(t *testing.T) {
	local, p := setup(t, map[string]string{"alice": "pw"})
	c, err := net.Dial("tcp", serveProxy(t, p))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	user := "alice-session-udp"
	c.Write([]byte{5, 1, 2})
	c.Write(append(append([]byte{1, byte(len(user))}, user...), 2, 'p', 'w'))
	c.Write([]byte{5, cmdAssociate, 0, atypIPv4, 0, 0, 0, 0, 0, 0})
	resp := make([]byte, 2+2+10) // chọn phương thức + kết quả xác thực + phản hồi (BND là IPv4)
	if _, err := io.ReadFull(c, resp); err != nil {
		t.Fatal(err)
	}
	if resp[1] != 2 || resp[3] != 0 || resp[5] != repOK || resp[7] != atypIPv4 {
		t.Fatalf("Phản hồi bắt tay bất thường: %v", resp)
	}
	bnd := netip.AddrPortFrom(netip.AddrFrom4([4]byte(resp[8:12])), binary.BigEndian.Uint16(resp[12:14]))

	u, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(bnd))
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	u.SetDeadline(time.Now().Add(5 * time.Second))
	hdr := appendAddr([]byte{0, 0, 0}, netip.AddrPortFrom(remoteAddr, 7))
	if _, err := u.Write(append(hdr, "ping"...)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1500)
	n, err := u.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n < len(hdr) || !bytes.Equal(buf[:len(hdr)], hdr) {
		t.Fatalf("Header phản hồi UDP bất thường: %v", buf[:n])
	}
	got := netip.MustParseAddrPort(string(buf[len(hdr):n])).Addr()
	if want := local.StickyAddr(user); got != want {
		t.Fatalf("Địa chỉ nguồn mà phía đối diện thấy %s, mong đợi %s", got, want)
	}
}

// Sau khi client gửi xong yêu cầu rồi CloseWrite (bán đóng) vẫn phải nhận được phản hồi đầy đủ: relay không được
// đóng toàn bộ hai đầu khi một chiều đọc tới EOF, mà phải chuyển EOF thành CloseWrite cho phía bên kia.
func TestSOCKS5HalfClose(t *testing.T) {
	_, p := setup(t, map[string]string{"alice": "pw"})
	c, err := net.Dial("tcp", serveProxy(t, p))
	if err != nil {
		t.Fatal(err)
	}
	tc := c.(*net.TCPConn)
	defer tc.Close()
	tc.SetDeadline(time.Now().Add(5 * time.Second))

	user := "alice"
	tc.Write([]byte{5, 1, 2})
	tc.Write(append(append([]byte{1, byte(len(user))}, user...), 2, 'p', 'w'))
	req := append([]byte{5, cmdConnect, 0, atypIPv6}, remoteAddr.AsSlice()...)
	req = binary.BigEndian.AppendUint16(req, 8)
	tc.Write(req)

	// Chọn phương thức(2) + kết quả xác thực(2) + phản hồi CONNECT(VER REP RSV ATYP ADDR(16) PORT(2) = 22)
	resp := make([]byte, 2+2+22)
	if _, err := io.ReadFull(tc, resp); err != nil {
		t.Fatal(err)
	}
	if resp[1] != 2 || resp[3] != 0 || resp[5] != repOK || resp[7] != atypIPv6 {
		t.Fatalf("Phản hồi CONNECT bất thường: %v", resp)
	}

	if _, err := tc.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if err := tc.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(tc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "resp:q" {
		t.Fatalf("got = %q, want %q", got, "resp:q")
	}
}

func TestHTTPProxy(t *testing.T) {
	local, p := setup(t, map[string]string{"alice": "pw"})
	addr := serveProxy(t, p)

	if code, body := get(t, "http://alice:pw@"+addr); code != 200 || !local.Contains(netip.MustParseAddr(body)) {
		t.Fatalf("Ngẫu nhiên: %d %s", code, body)
	}
	want := local.StickyAddr("alice-session-h1").String()
	if code, body := get(t, "http://alice-session-h1:pw@"+addr); code != 200 || body != want {
		t.Fatalf("session: %d %s, mong đợi %s", code, body, want)
	}
	if code, body := get(t, "http://alice-ip-2001-db8-1--43:pw@"+addr); code != 200 || body != "2001:db8:1::43" {
		t.Fatalf("IP chỉ định: %d %s", code, body)
	}
	if code, body := get(t, "http://alice-ip-2001-db8-2--1:pw@"+addr); code != http.StatusForbidden {
		t.Fatalf("IP chỉ định ngoài pool phải 403: %d %s", code, body)
	}
	if code, body := get(t, "http://alice:wrong@"+addr); code != http.StatusProxyAuthRequired {
		t.Fatalf("Sai mật khẩu phải 407: %d %s", code, body)
	}
	if code, body := get(t, "http://"+addr); code != http.StatusProxyAuthRequired {
		t.Fatalf("Thiếu xác thực phải 407: %d %s", code, body)
	}
}

func TestHTTPConnect(t *testing.T) {
	_, p := setup(t, map[string]string{"alice": "pw"})
	c, err := net.Dial("tcp", serveProxy(t, p))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	auth := base64.StdEncoding.EncodeToString([]byte("alice:pw"))
	fmt.Fprintf(c, "CONNECT [2001:db8:ffff::1]:7 HTTP/1.1\r\nHost: [2001:db8:ffff::1]:7\r\nProxy-Authorization: Basic %s\r\n\r\n", auth)
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Mã trạng thái CONNECT %d", resp.StatusCode)
	}
	io.WriteString(c, "hello")
	buf := make([]byte, 5)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("echo: %q %v", buf, err)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want failKind
	}{
		{nil, kindOK},
		{errAuth, kindAuth},
		{fmt.Errorf("x: %w", tunnel.ErrNotInPool), kindRejected},
		{fmt.Errorf("%w: example.com", tunnel.ErrNoIPv6), kindNoIPv6},
		{errors.New("connection was refused"), kindRefused},
		{context.DeadlineExceeded, kindTimeout},
		{&net.OpError{Op: "connect", Err: errors.New("operation timed out")}, kindTimeout},
		{io.ErrUnexpectedEOF, kindOther},
	}
	for _, c := range cases {
		if got := classify(c.err); got != c.want {
			t.Errorf("classify(%v) = %s, mong đợi %s", c.err, got, c.want)
		}
	}
}

// lockedBuffer là bộ đệm log an toàn khi dùng đồng thời.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Mỗi yêu cầu khi kết thúc được tính vào thống kê và ghi một dòng ở mức debug; log không được xuất hiện mật khẩu.
func TestRequestLog(t *testing.T) {
	var logs lockedBuffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	local, p := setup(t, map[string]string{"alice": "secret-pw"})
	addr := serveProxy(t, p)
	if code, body := get(t, "socks5://alice-session-z:secret-pw@"+addr); code != 200 || !local.Contains(netip.MustParseAddr(body)) {
		t.Fatalf("socks5: %d %s", code, body)
	}
	if code, _ := get(t, "http://alice:secret-pw@"+addr); code != 200 {
		t.Fatalf("http: %d", code)
	}
	get(t, "http://alice:wrong@"+addr)
	get(t, "socks5://alice-ip-2001-db8-2--1:secret-pw@"+addr)

	// Việc chuyển tiếp chỉ kết thúc sau khi client đóng kết nối, thống kê được cập nhật bất đồng bộ
	deadline := time.Now().Add(3 * time.Second)
	var s Stats
	for {
		s = p.Stats()
		if s.Requests() >= 4 && s.Active == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s.Count[kindOK] != 2 || s.Count[kindAuth] != 1 || s.Count[kindRejected] != 1 || s.Active != 0 || s.Up == 0 || s.Down == 0 {
		t.Fatalf("Thống kê không đúng: %+v", s)
	}

	out := logs.String()
	if strings.Contains(out, "secret-pw") {
		t.Fatalf("Mật khẩu xuất hiện trong log:\n%s", out)
	}
	for _, want := range []string{"proto=socks5 ", "user=alice-session-z", "dst=[2001:db8:ffff::1]:80", "src=2001:db8:1:", "proto=http ", "result=auth", "result=rejected"} {
		if !strings.Contains(out, want) {
			t.Errorf("Log thiếu %q:\n%s", want, out)
		}
	}
}
