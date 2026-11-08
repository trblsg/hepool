package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"
)

func TestMgmtConfig(t *testing.T) {
	f := mgmtFlags{server: "216.66.80.26", prefixes: "2001:db8:1::/64, 2001:db8:abcd::/48"}
	var cfg config
	if err := f.apply(&cfg, map[string]bool{"server": true, "prefix": true}); err != nil {
		t.Fatal(err)
	}
	if cfg.ServerIPv4 != netip.MustParseAddr("216.66.80.26") || len(cfg.Prefixes) != 2 || cfg.Prefixes[1] != netip.MustParsePrefix("2001:db8:abcd::/48") {
		t.Fatalf("Kết quả apply không đúng: %+v", cfg)
	}

	// Giá trị mặc định: cổng cao ngẫu nhiên, tên người dùng và mật khẩu ngẫu nhiên, và phải vượt qua kiểm tra
	if err := fillDefaults(&cfg); err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := net.LookupPort("tcp", port); err != nil || host != "" || n < 20000 || n >= 60000 {
		t.Fatalf("Cổng ngẫu nhiên không đúng: %q", cfg.Listen)
	}
	user, pass := firstUser(cfg.Users)
	if len(cfg.Users) != 1 || len(user) < 8 || len(pass) < 20 || strings.ContainsAny(user+pass, ":@/-") {
		t.Fatalf("Thông tin xác thực ngẫu nhiên không đúng: %q %q", user, pass)
	}
	if cfg.MTU != 1480 {
		t.Fatalf("MTU mặc định = %d", cfg.MTU)
	}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}

	// Chỉ đổi mật khẩu thì giữ tên người dùng; chỉ đổi cổng thì thông tin xác thực không đổi
	if err := (&mgmtFlags{pass: "NewPassw0rd"}).apply(&cfg, map[string]bool{"pass": true}); err != nil {
		t.Fatal(err)
	}
	if u, p := firstUser(cfg.Users); u != user || p != "NewPassw0rd" {
		t.Fatalf("Sau khi chỉ đổi mật khẩu: %q %q", u, p)
	}
	if err := (&mgmtFlags{listen: ":31234"}).apply(&cfg, map[string]bool{"listen": true}); err != nil {
		t.Fatal(err)
	}
	if u, p := firstUser(cfg.Users); cfg.Listen != ":31234" || u != user || p != "NewPassw0rd" {
		t.Fatalf("Sau khi chỉ đổi cổng: %+v", cfg)
	}

	// Tham số không hợp lệ
	bad := []struct {
		name string
		f    mgmtFlags
		set  string
	}{
		{"IPv6 server", mgmtFlags{server: "2001:db8::1"}, "server"},
		{"IPv4 prefix", mgmtFlags{prefixes: "10.0.0.0/8"}, "prefix"},
		{"IPv6 local", mgmtFlags{local: "2001:db8::1"}, "local"},
	}
	for _, c := range bad {
		var empty config
		if err := c.f.apply(&empty, map[string]bool{c.set: true}); err == nil {
			t.Errorf("%s phải báo lỗi", c.name)
		}
	}
	// Tên người dùng có thể phá vỡ URL hoặc bộ chọn phải bị từ chối
	for _, u := range []string{"a:b", "a@b", "x-ip-y", "x-session-y", "x-cidr-y", "x-ttl-y"} {
		c := cfg
		c.Users = map[string]string{u: "pw"}
		if c.validate() == nil {
			t.Errorf("Tên người dùng %q phải bị từ chối", u)
		}
	}
	if (config{Listen: ":1", Users: map[string]string{"u": "p"}}).validate() == nil {
		t.Error("Thiếu server và prefix phải báo lỗi")
	}
}

func TestFirewallRules(t *testing.T) {
	cfg := config{ServerIPv4: netip.MustParseAddr("216.66.80.26"), Listen: ":24601"}
	want := [][]string{
		{"-p", "41", "-s", "216.66.80.26", "-j", "ACCEPT"},
		{"-p", "tcp", "-m", "tcp", "--dport", "24601", "-j", "ACCEPT"},
	}
	if got := firewallRules(cfg); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("firewallRules = %v", got)
	}
	cfg.Listen = "127.0.0.1:1080"
	if got := firewallRules(cfg); len(got) != 1 {
		t.Fatalf("Khi lắng nghe địa chỉ loopback không được mở cổng proxy: %v", got)
	}
}

// Biến môi trường ghi đè mục cùng tên trong tệp cấu hình; biến không được đặt giữ nguyên giá trị cũ.
func TestEnvConfig(t *testing.T) {
	t.Setenv("HE_SERVER", "216.66.80.26")
	t.Setenv("HE_PREFIXES", "2001:db8:1::/64,2001:db8:abcd::/48")
	t.Setenv("HE_MTU", "1472")
	t.Setenv("PROXY_LISTEN", ":24601")
	t.Setenv("PROXY_PASS", "envpass")
	cfg := config{LocalIPv4: netip.MustParseAddr("10.0.0.2"), Users: map[string]string{"alice": "filepass"}}
	used, err := applyEnv(&cfg)
	if err != nil || !used {
		t.Fatalf("applyEnv: used=%v err=%v", used, err)
	}
	if cfg.ServerIPv4 != netip.MustParseAddr("216.66.80.26") || len(cfg.Prefixes) != 2 || cfg.MTU != 1472 || cfg.Listen != ":24601" {
		t.Fatalf("Biến môi trường không có hiệu lực: %+v", cfg)
	}
	if cfg.LocalIPv4 != netip.MustParseAddr("10.0.0.2") {
		t.Fatalf("HE_LOCAL không được đặt không nên thay đổi giá trị cũ: %s", cfg.LocalIPv4)
	}
	if u, p := firstUser(cfg.Users); u != "alice" || p != "envpass" {
		t.Fatalf("Khi chỉ đặt PROXY_PASS phải giữ tên người dùng: %q %q", u, p)
	}

	for name, bad := range map[string]string{"HE_SERVER": "2001:db8::1", "HE_PREFIXES": "10.0.0.0/8", "HE_MTU": "abc"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, bad)
			if _, err := applyEnv(&config{}); err == nil || !strings.Contains(err.Error(), name) {
				t.Errorf("%s=%q phải báo lỗi và chỉ ra tên biến, nhận được %v", name, bad, err)
			}
		})
	}
}

func TestEnvConfigUnset(t *testing.T) {
	for _, k := range []string{"HE_SERVER", "HE_PREFIXES", "HE_LOCAL", "HE_MTU", "PROXY_LISTEN", "PROXY_USER", "PROXY_PASS"} {
		os.Unsetenv(k)
	}
	if used, err := applyEnv(&config{}); used || err != nil {
		t.Fatalf("Khi không có biến môi trường: used=%v err=%v", used, err)
	}
}

func TestSetupLog(t *testing.T) {
	t.Cleanup(func() { slog.SetLogLoggerLevel(slog.LevelInfo) })
	debugOn := func() bool { return slog.Default().Enabled(context.Background(), slog.LevelDebug) }

	t.Setenv("LOG_LEVEL", "debug")
	if err := setupLog(false); err != nil || !debugOn() {
		t.Fatalf("LOG_LEVEL=debug phải bật debug: err=%v", err)
	}
	t.Setenv("LOG_LEVEL", "warn")
	if err := setupLog(true); err != nil || !debugOn() {
		t.Fatalf("-v phải được ưu tiên: err=%v", err)
	}
	if err := setupLog(false); err != nil || debugOn() || slog.Default().Enabled(context.Background(), slog.LevelInfo) {
		t.Fatalf("LOG_LEVEL=warn không được xuất info: err=%v", err)
	}
	t.Setenv("LOG_LEVEL", "verbose")
	if err := setupLog(false); err == nil || !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Fatalf("LOG_LEVEL không hợp lệ phải báo lỗi: %v", err)
	}
}
