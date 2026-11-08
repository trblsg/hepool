// Package tunnel kết nối tunnel HE 6in4 trong không gian người dùng: raw socket gửi nhận IPv4 giao thức 41,
// gVisor netstack xử lý IPv6, không tạo bất kỳ card mạng nào, và có thể dùng bất kỳ địa chỉ nào trong pool làm địa chỉ nguồn.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

const nicID = 1

var (
	ErrNotInPool = errors.New("tunnel: địa chỉ nguồn không nằm trong pool")
	ErrNoIPv6    = errors.New("tunnel: đích không có địa chỉ IPv6")
)

// Config tương ứng với thông tin trên trang chi tiết tunnel của tunnelbroker.net.
type Config struct {
	ServerIPv4 netip.Addr     // HE "Server IPv4 Address"
	LocalIPv4  netip.Addr     // tùy chọn, địa chỉ ràng buộc cục bộ; sau NAT điền IP nội bộ, zero là 0.0.0.0
	Prefixes   []netip.Prefix // tiền tố IPv6 có thể dùng làm địa chỉ nguồn (Routed /64, /48, v.v.)
	MTU        int            // MTU tunnel, 0 nghĩa là 1480
}

// Tunnel là một tunnel 6in4 trong không gian người dùng.
type Tunnel struct {
	pool   pool
	stack  *stack.Stack
	ep     *channel.Endpoint
	rxCap  *rxCapEndpoint
	pc     net.PacketConn
	peer   net.Addr
	peerAP netip.AddrPort
	mtu    int
	cancel context.CancelFunc
	wg     sync.WaitGroup

	spreadSeq atomic.Uint64 // số thứ tự của SpreadAddr, bắt đầu từ giá trị ngẫu nhiên, sau khi khởi động lại không phát lại cùng thứ tự subnet
}

// rxCapEndpoint bọc channel.Endpoint, chỉ ghi đè Capabilities(): nic.DeliverNetworkPacket của gVisor
// trước khi giao gói cho ngăn xếp giao thức sẽ dùng bit năng lực CapabilityRXChecksumOffload của lớp liên kết
// để ghi đè vô điều kiện PacketBuffer.RXChecksumValidated (xem pkg/tcpip/stack/nic.go), nên gán trực tiếp
// cho PacketBuffer của một gói sẽ không có tác dụng — quyết định "miễn kiểm tra gói này" chỉ có thể đến được
// ngăn xếp giao thức thông qua bit năng lực. Capabilities() được đọc đồng bộ, theo thời gian thực mỗi lần
// trong DeliverNetworkPacket, còn inbound() là goroutine duy nhất gọi InjectInbound (đơn goroutine, đồng bộ),
// nên đặt skipRXChecksum trước khi inject không có vấn đề đồng thời; nó và CapabilityTXChecksumOffload dùng cho
// chiều gửi là các bit độc lập, không ảnh hưởng việc tính checksum chiều ra.
type rxCapEndpoint struct {
	*channel.Endpoint
	skipRXChecksum atomic.Bool
}

func (e *rxCapEndpoint) Capabilities() stack.LinkEndpointCapabilities {
	caps := e.Endpoint.Capabilities()
	if e.skipRXChecksum.Load() {
		caps |= stack.CapabilityRXChecksumOffload
	}
	return caps
}

// New tạo raw socket (cần root hoặc CAP_NET_RAW) và khởi động tunnel.
func New(cfg Config) (*Tunnel, error) {
	if !cfg.ServerIPv4.Is4() {
		return nil, errors.New("tunnel: ServerIPv4 phải là địa chỉ IPv4")
	}
	local := "0.0.0.0"
	if cfg.LocalIPv4.IsValid() {
		if !cfg.LocalIPv4.Is4() {
			return nil, errors.New("tunnel: LocalIPv4 phải là địa chỉ IPv4")
		}
		local = cfg.LocalIPv4.String()
	}
	pc, err := net.ListenPacket("ip4:41", local)
	if err != nil {
		return nil, fmt.Errorf("tunnel: tạo raw socket thất bại (cần root hoặc CAP_NET_RAW): %w", err)
	}
	t, err := NewWithConn(cfg, pc, &net.IPAddr{IP: cfg.ServerIPv4.AsSlice()})
	if err != nil {
		pc.Close()
		return nil, err
	}
	return t, nil
}

// NewWithConn chạy tunnel trên bất kỳ PacketConn nào: mỗi datagram gửi nhận trên pc là một gói IPv6 thô,
// chỉ chấp nhận gói từ peer, mọi gói gửi ra đều gửi cho peer. New dùng nó trên raw socket, test dùng nó trên UDP.
// Sau khi thành công, pc thuộc quyền sở hữu của Tunnel và được Close đóng.
func NewWithConn(cfg Config, pc net.PacketConn, peer net.Addr) (*Tunnel, error) {
	if len(cfg.Prefixes) == 0 {
		return nil, errors.New("tunnel: cần ít nhất một tiền tố IPv6")
	}
	p := make(pool, len(cfg.Prefixes))
	for i, pfx := range cfg.Prefixes {
		if !pfx.IsValid() || !pfx.Addr().Is6() || pfx.Addr().Is4In6() {
			return nil, fmt.Errorf("tunnel: %s không phải tiền tố IPv6", pfx)
		}
		p[i] = pfx.Masked()
	}
	mtu := cfg.MTU
	if mtu == 0 {
		mtu = 1480
	}
	if mtu < header.IPv6MinimumMTU || mtu > 65535 {
		return nil, fmt.Errorf("tunnel: MTU %d vượt ngoài phạm vi [1280, 65535]", mtu)
	}
	// addrPort trả về AddrPort zero với các kiểu net.Addr không nhận biết được (gồm nil); nếu không chặn
	// ở đây, điều kiện "nguồn == peer" trong inbound() sẽ suy biến thành zero bằng zero, tức là cho phép
	// mọi người gửi, hoàn toàn mất lọc nguồn.
	if !addrPort(peer).Addr().IsValid() {
		return nil, errors.New("tunnel: peer phải là *net.IPAddr hoặc *net.UDPAddr")
	}

	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol, icmp.NewProtocol6},
	})
	ep := channel.New(1024, uint32(mtu), "")
	rxEP := &rxCapEndpoint{Endpoint: ep}
	if err := s.CreateNIC(nicID, rxEP); err != nil {
		s.Destroy()
		return nil, fmt.Errorf("tunnel: CreateNIC: %s", err)
	}
	// Spoofing: cho phép bind và gửi bất kỳ địa chỉ nguồn nào; Promiscuous: nhận gói gửi tới bất kỳ địa chỉ nào.
	s.SetSpoofing(nicID, true)
	s.SetPromiscuousMode(nicID, true)
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv6EmptySubnet, NIC: nicID}})
	sack := tcpip.TCPSACKEnabled(true)
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &sack)

	ctx, cancel := context.WithCancel(context.Background())
	t := &Tunnel{pool: p, stack: s, ep: ep, rxCap: rxEP, pc: pc, peer: peer, peerAP: addrPort(peer), mtu: mtu, cancel: cancel}
	t.spreadSeq.Store(rand.Uint64())
	t.wg.Add(2)
	go t.inbound()
	go t.outbound(ctx)
	return t, nil
}

// inbound đưa các gói IPv6 từ peer vào netstack.
func (t *Tunnel) inbound() {
	defer t.wg.Done()
	buf := make([]byte, 65535)
	for {
		n, from, err := t.pc.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			slog.Debug("tunnel: đọc thất bại", "err", err)
			continue
		}
		if addrPort(from) != t.peerAP {
			continue // chỉ chấp nhận gói từ máy chủ HE, tránh bị chèn gói giả
		}
		pkt := buf[:n]
		if len(pkt) < header.IPv6MinimumSize || header.IPVersion(pkt) != header.IPv6Version {
			continue
		}
		ip := header.IPv6(pkt)
		end := header.IPv6MinimumSize + int(ip.PayloadLength())
		if end > len(pkt) {
			continue // gói bị cắt cụt
		}
		if !t.pool.contains(netip.AddrFrom16(ip.DestinationAddress().As16())) {
			continue
		}
		pb := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(pkt[:end])})
		// Gói vượt quá MTU của tunnel thường là siêu gói do kernel GRO gộp: kernel đã kiểm tra checksum
		// TCP/UDP của từng phân đoạn trước khi gộp, sau khi gộp không tính lại checksum bên trong (trường
		// vẫn là giá trị cũ của một phân đoạn nào đó), nếu netstack kiểm tra lại theo quy trình thông thường
		// sẽ nhầm là lỗi checksum và bỏ gói, nên phải cho qua. Tạm báo CapabilityRXChecksumOffload qua rxCap
		// (xem chú thích của nó) để nic.DeliverNetworkPacket ghi quyết định này vào PacketBuffer.RXChecksumValidated.
		// ponytail: gói do GRO gộp nếu ≤ MTU (MSS bên gửi ≲ 720) vẫn bị bỏ vì checksum cũ; khi MTU đặt nhỏ hơn
		// HE, gói lớn do tái hợp phân mảnh IPv4 sẽ bỏ qua kiểm tra; phán đoán này phụ thuộc việc nic.go của gvisor
		// dùng năng lực liên kết ghi đè RXChecksumValidated, khi nâng cấp gVisor có TestOversizedPacketSkipsChecksum
		// làm lưới an toàn.
		t.rxCap.skipRXChecksum.Store(end > t.mtu)
		t.ep.InjectInbound(ipv6.ProtocolNumber, pb)
		pb.DecRef()
	}
}

// outbound giao các gói IPv6 do netstack gửi ra cho peer (raw socket sẽ được kernel bổ sung header IPv4).
func (t *Tunnel) outbound(ctx context.Context) {
	defer t.wg.Done()
	for {
		pkt := t.ep.ReadContext(ctx)
		if pkt == nil {
			return // ctx đã bị hủy
		}
		v := pkt.ToView()
		_, err := t.pc.WriteTo(v.AsSlice(), t.peer)
		v.Release()
		pkt.DecRef()
		if err != nil {
			slog.Debug("tunnel: gửi thất bại", "err", err)
		}
	}
}

// addrPort thống nhất địa chỉ của raw socket (*net.IPAddr) và UDP (*net.UDPAddr), thuận tiện so sánh nguồn.
func addrPort(a net.Addr) netip.AddrPort {
	switch a := a.(type) {
	case *net.IPAddr:
		ip, _ := netip.AddrFromSlice(a.IP)
		return netip.AddrPortFrom(ip.Unmap(), 0)
	case *net.UDPAddr:
		ap := a.AddrPort()
		return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	}
	return netip.AddrPort{}
}

func fullAddr(ap netip.AddrPort) tcpip.FullAddress {
	return tcpip.FullAddress{NIC: nicID, Addr: tcpip.AddrFrom16(ap.Addr().As16()), Port: ap.Port()}
}

// Resolve phân giải host:port thành địa chỉ IPv6: tên miền chỉ truy vấn AAAA, đích IPv4 trả về ErrNoIPv6.
func Resolve(ctx context.Context, address string) (netip.AddrPort, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return netip.AddrPort{}, err
	}
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("tunnel: cổng không hợp lệ %q", port)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip6", host)
		if err != nil {
			return netip.AddrPort{}, fmt.Errorf("%w: %s: %w", ErrNoIPv6, host, err)
		}
		if len(ips) == 0 {
			return netip.AddrPort{}, fmt.Errorf("%w: %s", ErrNoIPv6, host)
		}
		ip = ips[0]
	}
	if !ip.Is6() || ip.Is4In6() {
		return netip.AddrPort{}, fmt.Errorf("%w: %s", ErrNoIPv6, host)
	}
	return netip.AddrPortFrom(ip.WithZone(""), uint16(p)), nil
}

// Dial kết nối tới address (host:port) với src làm địa chỉ nguồn, network là tcp/tcp6/udp/udp6.
// Khi src là zero thì chọn ngẫu nhiên địa chỉ trong pool.
func (t *Tunnel) Dial(ctx context.Context, src netip.Addr, network, address string) (net.Conn, error) {
	if !src.IsValid() {
		src = t.pool.random()
	} else if !t.pool.contains(src) {
		return nil, ErrNotInPool
	}
	dst, err := Resolve(ctx, address)
	if err != nil {
		return nil, err
	}
	local, remote := fullAddr(netip.AddrPortFrom(src, 0)), fullAddr(dst)
	switch network {
	case "tcp", "tcp6":
		c, err := gonet.DialTCPWithBind(ctx, t.stack, local, remote, ipv6.ProtocolNumber)
		if err != nil {
			return nil, err // không được trả c trực tiếp: *TCPConn nil khi đóng gói vào interface không bằng nil
		}
		return c, nil
	case "udp", "udp6":
		c, err := gonet.DialUDP(t.stack, &local, &remote, ipv6.ProtocolNumber)
		if err != nil {
			return nil, err
		}
		return c, nil
	}
	return nil, fmt.Errorf("tunnel: không hỗ trợ network %q", network)
}

// ListenUDP mở một UDP socket không connected trên src, cổng 0 thì tự động cấp.
// conn trả về là implementation UDP của gonet, WriteTo chỉ chấp nhận *net.UDPAddr.
func (t *Tunnel) ListenUDP(src netip.AddrPort) (net.PacketConn, error) {
	if !t.pool.contains(src.Addr()) {
		return nil, ErrNotInPool
	}
	local := fullAddr(src)
	c, err := gonet.DialUDP(t.stack, &local, nil, ipv6.ProtocolNumber)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ListenTCP lắng nghe TCP trên địa chỉ trong pool, dùng để nhận kết nối vào.
func (t *Tunnel) ListenTCP(addr netip.AddrPort) (net.Listener, error) {
	if !t.pool.contains(addr.Addr()) {
		return nil, ErrNotInPool
	}
	l, err := gonet.ListenTCP(t.stack, fullAddr(addr), ipv6.ProtocolNumber)
	if err != nil {
		return nil, err
	}
	return l, nil
}

// PrefixCheck là kết quả Check kiểm tra một tiền tố.
type PrefixCheck struct {
	Prefix netip.Prefix
	Src    netip.Addr    // địa chỉ nguồn dùng để kiểm tra
	RTT    time.Duration // thời gian từ khi bắt đầu kết nối đến khi nhận phản hồi, bằng 0 khi Err khác nil
	Err    error         // nil nghĩa là cả chiều đi và chiều về đều thông
}

// Check lấy một địa chỉ ngẫu nhiên từ mỗi tiền tố, đồng thời mở kết nối TCP tới targets: chỉ cần một kết nối
// thành công hoặc bị từ chối (nhận RST cũng chứng tỏ gói phản hồi đã tới) thì coi tiền tố đó khả dụng; nếu đến
// khi ctx kết thúc vẫn không có phản hồi, thường là HE chưa định tuyến tiền tố này tới tunnel, hoặc tường lửa
// chặn giao thức 41. Kết quả được sắp xếp theo thứ tự cấu hình của tiền tố.
func (t *Tunnel) Check(ctx context.Context, targets []netip.AddrPort) []PrefixCheck {
	res := make([]PrefixCheck, len(t.pool))
	var wg sync.WaitGroup
	for i, pfx := range t.pool {
		res[i] = PrefixCheck{Prefix: pfx, Src: randomIn(pfx)}
		wg.Add(1)
		go func(r *PrefixCheck) {
			defer wg.Done()
			r.RTT, r.Err = t.probe(ctx, r.Src, targets)
		}(&res[i])
	}
	wg.Wait()
	return res
}

// probe đồng thời kết nối tới mọi targets với src làm địa chỉ nguồn, trả về thời gian của phản hồi đầu tiên.
func (t *Tunnel) probe(ctx context.Context, src netip.Addr, targets []netip.AddrPort) (time.Duration, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	start := time.Now()
	errc := make(chan error, len(targets))
	for _, dst := range targets {
		go func() {
			c, err := t.Dial(ctx, src, "tcp", dst.String())
			if err == nil {
				c.Close()
			} else if strings.Contains(err.Error(), "refused") { // gonet chuyển lỗi tcpip thành văn bản thuần
				err = nil
			}
			errc <- err
		}()
	}
	err := errors.New("tunnel: không có mục tiêu kiểm tra")
	for range targets {
		if err = <-errc; err == nil {
			return time.Since(start), nil
		}
	}
	return 0, err
}

// RandomAddr trả về địa chỉ ngẫu nhiên trong pool.
func (t *Tunnel) RandomAddr() netip.Addr { return t.pool.random() }

// SpreadAddr trả về địa chỉ trong pool, luân chuyển theo subnet /cidr: mỗi lần gọi rơi vào subnet khác nhau, chỉ lặp lại sau khi dùng hết mọi subnet.
func (t *Tunnel) SpreadAddr(cidr int) netip.Addr { return t.pool.spread(cidr, t.spreadSeq.Add(1)) }

// StickyAddr trả về địa chỉ cố định tương ứng với key.
func (t *Tunnel) StickyAddr(key string) netip.Addr { return t.pool.sticky(key) }

// Contains cho biết a có nằm trong pool không.
func (t *Tunnel) Contains(a netip.Addr) bool { return t.pool.contains(a) }

// Close đóng tunnel và mọi kết nối trên tunnel.
func (t *Tunnel) Close() error {
	t.cancel()
	err := t.pc.Close()
	t.stack.Destroy()
	t.wg.Wait()
	return err
}
