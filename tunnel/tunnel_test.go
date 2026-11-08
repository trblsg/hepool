package tunnel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

var (
	localPrefix  = netip.MustParsePrefix("2001:db8:1::/48")
	remotePrefix = netip.MustParsePrefix("2001:db8:ffff::/64")
	remoteAddr   = netip.MustParseAddr("2001:db8:ffff::1")
)

func listenUDP(t *testing.T) net.PacketConn {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func ctxTimeout(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// pair dùng hai UDP socket mô phỏng liên kết 6in4: local là tunnel được kiểm thử, remote đóng vai một máy chủ trên Internet.
// peerConn là socket mà local nhìn thấy của peer (máy chủ HE).
func pair(t *testing.T) (local, remote *Tunnel, peerConn net.PacketConn) {
	t.Helper()
	a, b := listenUDP(t), listenUDP(t)
	var err error
	if local, err = NewWithConn(Config{Prefixes: []netip.Prefix{localPrefix}}, a, b.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	if remote, err = NewWithConn(Config{Prefixes: []netip.Prefix{remotePrefix}}, b, a.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { local.Close(); remote.Close() })
	return local, remote, b
}

func TestDialTCP(t *testing.T) {
	local, remote, _ := pair(t)
	ln, err := remote.ListenTCP(netip.AddrPortFrom(remoteAddr, 7))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			io.WriteString(c, c.RemoteAddr().String())
			c.Close()
		}
	}()

	explicit := netip.MustParseAddr("2001:db8:1:2::3")
	for _, src := range []netip.Addr{explicit, {}} {
		c, err := local.Dial(ctxTimeout(t), src, "tcp", "[2001:db8:ffff::1]:7")
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(c)
		c.Close()
		if err != nil {
			t.Fatal(err)
		}
		got := netip.MustParseAddrPort(string(b)).Addr()
		if (src.IsValid() && got != src) || !localPrefix.Contains(got) {
			t.Fatalf("Địa chỉ nguồn mà phía đối diện thấy %s (chỉ định %v, tiền tố %s)", got, src, localPrefix)
		}
	}
}

func TestDialUDP(t *testing.T) {
	local, remote, _ := pair(t)
	srv, err := remote.ListenUDP(netip.AddrPortFrom(remoteAddr, 53))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			_, from, err := srv.ReadFrom(buf)
			if err != nil {
				return
			}
			srv.WriteTo([]byte(from.String()), from)
		}
	}()

	src := local.StickyAddr("udp")
	c, err := local.Dial(ctxTimeout(t), src, "udp", "[2001:db8:ffff::1]:53")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1500)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := netip.MustParseAddrPort(string(buf[:n])).Addr(); got != src {
		t.Fatalf("Địa chỉ nguồn mà phía đối diện thấy %s, mong đợi %s", got, src)
	}
}

func TestErrors(t *testing.T) {
	local, _, _ := pair(t)
	outside := netip.MustParseAddr("2001:db8:2::1")
	if _, err := local.Dial(ctxTimeout(t), outside, "tcp", "[2001:db8:ffff::1]:7"); !errors.Is(err, ErrNotInPool) {
		t.Fatalf("Địa chỉ nguồn ngoài pool: err = %v", err)
	}
	if _, err := local.ListenUDP(netip.AddrPortFrom(outside, 0)); !errors.Is(err, ErrNotInPool) {
		t.Fatalf("ListenUDP địa chỉ ngoài pool: err = %v", err)
	}
	if _, err := local.Dial(ctxTimeout(t), netip.Addr{}, "tcp", "192.0.2.1:80"); !errors.Is(err, ErrNoIPv6) {
		t.Fatalf("Đích IPv4: err = %v", err)
	}
	if _, err := NewWithConn(Config{Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}, listenUDP(t), nil); err == nil {
		t.Fatal("Tiền tố IPv4 phải báo lỗi")
	}
	if _, err := NewWithConn(Config{Prefixes: []netip.Prefix{localPrefix}, MTU: 1000}, listenUDP(t), nil); err == nil {
		t.Fatal("MTU < 1280 phải báo lỗi")
	}
	unsupported := &net.UnixAddr{Name: "x", Net: "unixgram"}
	if _, err := NewWithConn(Config{Prefixes: []netip.Prefix{localPrefix}}, listenUDP(t), unsupported); err == nil {
		t.Fatal("peer có kiểu địa chỉ không được hỗ trợ phải báo lỗi")
	}
}

// Resolve phải giữ nguyên lỗi gốc khi tra cứu tên miền thất bại: vừa có thể dùng errors.Is(err, ErrNoIPv6) để xác định
// "không có địa chỉ IPv6", vừa có thể dùng errors.Is(err, context.Canceled) v.v. để xác định nguyên nhân cụ thể.
func TestResolveDNSErrorCause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Resolve(ctx, "example.invalid:80")
	if !errors.Is(err, ErrNoIPv6) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, mong đợi bọc đồng thời ErrNoIPv6 và context.Canceled", err)
	}
}

// Khi NIC của kernel bật GRO, các phân đoạn TCP/UDP liên tiếp ở tầng trong được gộp thành siêu gói vượt quá MTU của tunnel;
// trước khi gộp kernel đã kiểm tra checksum từng phân đoạn, nhưng sau khi gộp không tính lại trường checksum của gói trong
// (có thể còn sót giá trị cũ của một phân đoạn). Gói không vượt quá MTU phải được kiểm tra theo quy tắc thông thường; gói vượt
// quá MTU chỉ có thể là sản phẩm của GRO, phải cho qua, dù bản thân trường checksum sai.
func TestOversizedPacketSkipsChecksum(t *testing.T) {
	local, _, peerConn := pair(t)
	ln, err := local.ListenUDP(netip.MustParseAddrPort("[2001:db8:1::1]:9"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	src, dst := netip.MustParseAddr("2001:db8:ffff::9"), netip.MustParseAddr("2001:db8:1::1")
	srcPort, dstPort := uint16(5353), uint16(9)
	wrongChecksum := uint16(0x1234) // cố ý viết sai và khác 0, mô phỏng checksum cũ còn sót sau khi GRO gộp
	put16 := func(b []byte, v uint16) { b[0], b[1] = byte(v>>8), byte(v) }

	// Tự ghép một gói IPv6 + UDP: pkt[0] là version, pkt[4:6] là payload length,
	// pkt[6]/pkt[7] là next header(UDP=17)/hop limit, tiếp theo là 16 byte địa chỉ nguồn,
	// 16 byte địa chỉ đích, rồi header UDP (src port/dst port/length/checksum) và dữ liệu.
	packet := func(data []byte) []byte {
		pkt := make([]byte, 40+8+len(data))
		udpLen := uint16(8 + len(data))
		pkt[0] = 0x60
		put16(pkt[4:6], udpLen)
		pkt[6], pkt[7] = 17, 64
		s, d := src.As16(), dst.As16()
		copy(pkt[8:24], s[:])
		copy(pkt[24:40], d[:])
		put16(pkt[40:42], srcPort)
		put16(pkt[42:44], dstPort)
		put16(pkt[44:46], udpLen)
		put16(pkt[46:48], wrongChecksum)
		copy(pkt[48:], data)
		return pkt
	}
	fill := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(i)
		}
		return b
	}

	buf := make([]byte, 2200)
	checksumErrors := local.stack.Stats().UDP.ChecksumErrors

	// Gói nhỏ (không vượt MTU): checksum sai phải bị từ chối theo quy tắc thông thường, không được nhận dữ liệu.
	small := fill(100) // 40 + 8 + 100 = 148 byte, nhỏ hơn nhiều so với MTU 1480
	before := checksumErrors.Value()
	peerConn.WriteTo(packet(small), local.pc.LocalAddr())
	ln.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, _, err := ln.ReadFrom(buf); err == nil {
		t.Fatal("Gói nhỏ có checksum sai (không vượt MTU) lại được nhận như dữ liệu bình thường")
	}
	if checksumErrors.Value() == before {
		t.Fatal("UDP.ChecksumErrors phải tăng")
	}

	// Gói lớn (vượt MTU): chỉ có thể là sản phẩm của GRO, phải cho qua, dù trường checksum sai.
	big := fill(2000) // 40 + 8 + 2000 = 2048 byte, vượt MTU 1480
	peerConn.WriteTo(packet(big), local.pc.LocalAddr())
	ln.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := ln.ReadFrom(buf)
	if err != nil {
		t.Fatalf("Gói vượt MTU phải được coi là sản phẩm của GRO và cho qua: %v", err)
	}
	if !bytes.Equal(buf[:n], big) {
		t.Fatal("Payload nhận được không khớp với dữ liệu đã gửi")
	}
}

// Chỉ các gói 6in4 đến từ peer và có địa chỉ đích nằm trong pool mới được đưa vào ngăn xếp giao thức.
func TestDropForeign(t *testing.T) {
	local, _, peerConn := pair(t)
	attacker := listenUDP(t)
	packet := func(dst string) []byte {
		pkt := make([]byte, 40) // gói IPv6 tối thiểu: Next Header = 59 (No Next Header), không có payload
		pkt[0], pkt[6], pkt[7] = 0x60, 59, 64
		s, d := netip.MustParseAddr("2001:db8:ffff::9").As16(), netip.MustParseAddr(dst).As16()
		copy(pkt[8:24], s[:])
		copy(pkt[24:40], d[:])
		return pkt
	}
	received := local.stack.Stats().IP.PacketsReceived
	before := received.Value()

	attacker.WriteTo(packet("2001:db8:1::1"), local.pc.LocalAddr())
	peerConn.WriteTo(packet("2001:db8:2::1"), local.pc.LocalAddr())
	time.Sleep(200 * time.Millisecond)
	if received.Value() != before {
		t.Fatal("Đã nhận gói không phải từ peer, hoặc gói có địa chỉ đích ngoài pool")
	}

	peerConn.WriteTo(packet("2001:db8:1::1"), local.pc.LocalAddr())
	deadline := time.Now().Add(2 * time.Second)
	for received.Value() == before {
		if time.Now().After(deadline) {
			t.Fatal("Không nhận gói hợp lệ do peer gửi")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// dropTo loại bỏ các gói vào có địa chỉ đích trong drop, mô phỏng HE không định tuyến tiền tố này tới tunnel.
type dropTo struct {
	net.PacketConn
	drop netip.Prefix
}

func (d dropTo) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		n, a, err := d.PacketConn.ReadFrom(b)
		if err == nil && n >= 40 && d.drop.Contains(netip.AddrFrom16([16]byte(b[24:40]))) {
			continue
		}
		return n, a, err
	}
}

func TestCheck(t *testing.T) {
	routed, unrouted := netip.MustParsePrefix("2001:db8:1::/48"), netip.MustParsePrefix("2001:db8:2::/48")
	a, b := listenUDP(t), listenUDP(t)
	local, err := NewWithConn(Config{Prefixes: []netip.Prefix{routed, unrouted}}, dropTo{a, unrouted}, b.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	remote, err := NewWithConn(Config{Prefixes: []netip.Prefix{remotePrefix}}, b, a.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { local.Close(); remote.Close() })
	ln, err := remote.ListenTCP(netip.AddrPortFrom(remoteAddr, 443))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	// 443 có lắng nghe; 444 không lắng nghe, phía đối diện trả RST, cũng chứng minh cả hai chiều đều thông
	for _, port := range []uint16{443, 444} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		res := local.Check(ctx, []netip.AddrPort{netip.AddrPortFrom(remoteAddr, port)})
		cancel()
		if len(res) != 2 || res[0].Prefix != routed || res[1].Prefix != unrouted {
			t.Fatalf("Cổng %d: kết quả phải được sắp xếp theo thứ tự tiền tố: %+v", port, res)
		}
		if r := res[0]; r.Err != nil || !routed.Contains(r.Src) || r.RTT <= 0 {
			t.Errorf("Cổng %d: tiền tố đã được định tuyến phải đạt: %+v", port, r)
		}
		if r := res[1]; r.Err == nil || !unrouted.Contains(r.Src) {
			t.Errorf("Cổng %d: tiền tố không nhận được gói hồi đáp phải thất bại: %+v", port, r)
		}
	}
}
