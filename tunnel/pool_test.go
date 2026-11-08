package tunnel

import (
	"net/netip"
	"testing"
)

func TestPool(t *testing.T) {
	p := pool{netip.MustParsePrefix("2001:db8:1::/48"), netip.MustParsePrefix("2001:db8:2:3::/64")}
	hits := map[netip.Prefix]int{}
	for range 1000 {
		a := p.random()
		if !p.contains(a) {
			t.Fatalf("random %s không nằm trong pool", a)
		}
		for _, pfx := range p {
			if pfx.Contains(a) {
				hits[pfx]++
			}
		}
	}
	if len(hits) != 2 {
		t.Fatalf("random không bao phủ mọi tiền tố: %v", hits)
	}
	if a, b := p.random(), p.random(); a == b {
		t.Fatal("random hai lần liên tiếp cho kết quả giống nhau")
	}

	a, b := p.sticky("alice-session-1"), p.sticky("alice-session-1")
	if a != b || !p.contains(a) {
		t.Fatalf("sticky không ổn định hoặc vượt ngoài phạm vi: %s %s", a, b)
	}
	if p.sticky("alice-session-2") == a {
		t.Fatal("Các key khác nhau nhận được cùng địa chỉ")
	}
	if p.contains(netip.MustParseAddr("2001:db8:2:4::1")) {
		t.Fatal("contains nhận nhầm địa chỉ ngoài pool")
	}

	// Tiền tố không thẳng hàng theo byte
	odd := pool{netip.MustParsePrefix("2001:db8:ab80::/41")}
	for range 1000 {
		if a := odd.random(); !odd.contains(a) {
			t.Fatalf("random %s không nằm trong /41", a)
		}
	}
}

func TestFill(t *testing.T) {
	var ones [16]byte
	for i := range ones {
		ones[i] = 0xff
	}
	got := fill(netip.MustParsePrefix("2001:db8:ab80::/41"), ones)
	want := netip.MustParseAddr("2001:db8:abff:ffff:ffff:ffff:ffff:ffff")
	if got != want {
		t.Fatalf("fill = %s, want %s", got, want)
	}
}

func TestSpread(t *testing.T) {
	p := pool{netip.MustParsePrefix("2001:db8:1::/48"), netip.MustParsePrefix("2001:db8:2:3::/64")}
	// Luân chuyển theo /64: trong pool có tổng cộng 65536+1 subnet /64, liên tiếp ngần ấy lần đều không trùng và đều nằm trong pool
	distinct := func(p pool, cidr int, n uint64) {
		t.Helper()
		seen := map[netip.Prefix]bool{}
		for i := range n {
			a := p.spread(cidr, 12345+i)
			s := netip.PrefixFrom(a, cidr).Masked()
			if !p.contains(a) || seen[s] {
				t.Fatalf("/%d lần thứ %d: %s (nằm trong pool %v, subnet trùng %v)", cidr, i, a, p.contains(a), seen[s])
			}
			seen[s] = true
		}
	}
	distinct(p, 64, 1<<16+1)
	distinct(pool{netip.MustParsePrefix("2001:db8:ab80::/41")}, 44, 8)  // không thẳng hàng theo byte
	distinct(pool{netip.MustParsePrefix("2001:db8:2:3::/64")}, 72, 256) // mịn hơn /64

	if a, b := p.spread(64, 7), p.spread(64, 7); a == b {
		t.Fatal("Các host bit trong cùng một subnet phải ngẫu nhiên")
	}
	// Khi độ chi tiết thô hơn cả tiền tố thì thoái hóa thành ngẫu nhiên trong tiền tố
	for i := range uint64(100) {
		if a := p.spread(32, i); !p.contains(a) {
			t.Fatalf("spread(32) = %s không nằm trong pool", a)
		}
	}
}
