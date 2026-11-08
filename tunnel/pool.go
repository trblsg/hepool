package tunnel

import (
	"crypto/sha256"
	"encoding/binary"
	"math/bits"
	"math/rand/v2"
	"net/netip"
)

// pool là tập hợp các tiền tố IPv6 có thể dùng làm địa chỉ nguồn, mọi phần tử đều đã Masked.
type pool []netip.Prefix

// random trước tiên chọn đều một tiền tố, rồi điền ngẫu nhiên phần host bit.
func (p pool) random() netip.Addr { return randomIn(p[rand.IntN(len(p))]) }

// randomIn trả về địa chỉ trong pfx với phần host bit ngẫu nhiên.
func randomIn(pfx netip.Prefix) netip.Addr {
	var host [16]byte
	binary.BigEndian.PutUint64(host[:8], rand.Uint64())
	binary.BigEndian.PutUint64(host[8:], rand.Uint64())
	return fill(pfx, host)
}

// sticky băm key thành địa chỉ cố định: cùng một key luôn cho cùng kết quả, không cần lưu trạng thái.
func (p pool) sticky(key string) netip.Addr {
	h := sha256.Sum256([]byte(key))
	pfx := p[binary.BigEndian.Uint64(h[:8])%uint64(len(p))]
	return fill(pfx, [16]byte(h[16:]))
}

// spread trả về địa chỉ luân chuyển thứ i: chia pool thành các subnet /cidr và đánh số, lần thứ i lấy subnet
// số i*m mod n, phần host bit ngẫu nhiên. n là tổng số subnet, m nguyên tố cùng nhau với n, nên phép nhân lấy
// modulo là một hoán vị trên [0,n): n giá trị i liên tiếp rơi vào n subnet khác nhau, không lặp lại trước khi
// dùng hết mọi subnet; m lấy hằng số tỉ lệ vàng, hai lần liên tiếp rơi vào các subnet rất xa nhau, chứ không
// phải ::1, ::2 liền kề. Tiền tố dài hơn (nhỏ hơn) cidr được tính là một subnet.
func (p pool) spread(cidr int, i uint64) netip.Addr {
	var n uint64
	for _, pfx := range p {
		n += subnets(pfx, cidr)
	}
	m := 0x9E3779B97F4A7C15 % n
	for gcd(m, n) != 1 {
		m++
	}
	hi, lo := bits.Mul64(i%n, m)
	idx := bits.Rem64(hi, lo, n)
	for _, pfx := range p {
		if k := subnets(pfx, cidr); idx >= k {
			idx -= k
			continue
		}
		// Đổi cidr bit đầu của địa chỉ thành số hiệu subnet (căn phải), các host bit còn lại ngẫu nhiên; các bit tiền tố sau đó được fill ghi đè
		h, l := rand.Uint64(), rand.Uint64()
		if s := uint(128 - min(max(cidr, pfx.Bits()), 128)); s >= 64 {
			h = h&(1<<(s-64)-1) | idx<<(s-64)
		} else {
			h, l = idx>>(64-s), l&(1<<s-1)|idx<<s
		}
		var host [16]byte
		binary.BigEndian.PutUint64(host[:8], h)
		binary.BigEndian.PutUint64(host[8:], l)
		return fill(pfx, host)
	}
	panic("unreachable")
}

// subnets trả về số subnet /cidr mà pfx chứa.
// ponytail: giới hạn 2^40 subnet, các subnet cao vượt quá không tham gia luân chuyển; HE cấp tối đa /48, tính theo /64 chỉ có 2^16 subnet
func subnets(pfx netip.Prefix, cidr int) uint64 {
	return 1 << min(max(cidr-pfx.Bits(), 0), 40)
}

func gcd(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func (p pool) contains(a netip.Addr) bool {
	for _, pfx := range p {
		if pfx.Contains(a) {
			return true
		}
	}
	return false
}

// fill giữ nguyên các bit tiền tố của pfx, các bit host lấy từ host.
func fill(pfx netip.Prefix, host [16]byte) netip.Addr {
	b := pfx.Addr().As16()
	for i := range b {
		n := pfx.Bits() - i*8 // số bit thuộc tiền tố trong byte này
		var mask byte
		switch {
		case n >= 8:
			mask = 0xff
		case n > 0:
			mask = ^byte(0xff >> n)
		}
		b[i] = b[i]&mask | host[i]&^mask
	}
	return netip.AddrFrom16(b)
}
