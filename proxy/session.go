package proxy

import (
	"net/netip"
	"sync"
	"time"
)

// sessionIdle: phiên có trạng thái không kèm -ttl- (có -cidr-) chỉ bị dọn sau khi nhàn rỗi ngần này.
const sessionIdle = 24 * time.Hour

// sessions ghi lại địa chỉ mà các phiên có -ttl- hoặc -cidr- đã chọn, giá trị zero dùng được; xóa sạch sau khi tiến trình khởi động lại.
type sessions struct {
	mu      sync.Mutex
	m       map[string]session
	sweepAt int // bảng tăng tới kích thước này thì dọn phiên hết hạn một lần, khấu hao O(1)
}

type session struct {
	addr    netip.Addr
	expires time.Time
	idle    bool // không có ttl: mỗi lần dùng đều gia hạn thêm sessionIdle
}

// get trả về địa chỉ của phiên key: dùng lại nếu chưa hết hạn, ngược lại chọn địa chỉ mới bằng pick. ttl tính từ lần dùng đầu tiên,
// việc sử dụng trong thời gian đó không kéo dài nó; ttl bằng 0 nghĩa là chỉ hết hạn sau khi nhàn rỗi sessionIdle.
func (s *sessions) get(key string, ttl time.Duration, pick func() netip.Addr) netip.Addr {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[key]; ok && now.Before(e.expires) {
		if e.idle {
			e.expires = now.Add(sessionIdle)
			s.m[key] = e
		}
		return e.addr
	}
	if s.m == nil {
		s.m = map[string]session{}
	}
	if len(s.m) >= s.sweepAt {
		for k, e := range s.m {
			if !now.Before(e.expires) {
				delete(s.m, k)
			}
		}
		s.sweepAt = max(2*len(s.m), 1024)
	}
	e := session{addr: pick(), expires: now.Add(ttl), idle: ttl == 0}
	if e.idle {
		e.expires = now.Add(sessionIdle)
	}
	s.m[key] = e
	return e.addr
}
