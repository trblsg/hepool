package main

import (
	"bytes"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"os/exec"
	osuser "os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	installDir  = "/opt/he-ipv6-pool"
	serviceName = "he-ipv6-pool"
	serviceUser = "hepool"
)

var (
	installBin  = installDir + "/he-ipv6-pool"
	installConf = installDir + "/config.json"
	unitPath    = "/etc/systemd/system/" + serviceName + ".service"
)

// mgmtFlags là các tham số dùng chung cho install và config.
type mgmtFlags struct {
	server, prefixes, local, listen, user, pass string
	mtu                                         int
}

func (f *mgmtFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.server, "server", "", "Server IPv4 Address trên trang chi tiết tunnel HE")
	fs.StringVar(&f.prefixes, "prefix", "", "Tiền tố Routed, nhiều tiền tố phân tách bằng dấu phẩy, ví dụ 2001:db8:1::/64,2001:db8:abcd::/48")
	fs.StringVar(&f.local, "local", "", "IPv4 ràng buộc cục bộ, máy chủ cloud dùng 1:1 NAT để trống")
	fs.IntVar(&f.mtu, "mtu", 1480, "MTU tunnel, khớp với bảng điều khiển HE")
	fs.StringVar(&f.listen, "listen", "", "Địa chỉ lắng nghe proxy (SOCKS5 và HTTP dùng chung), mặc định :cổng cao ngẫu nhiên")
	fs.StringVar(&f.user, "user", "", "Tên người dùng proxy, mặc định tạo ngẫu nhiên")
	fs.StringVar(&f.pass, "pass", "", "Mật khẩu proxy, mặc định tạo ngẫu nhiên")
}

// setFlags trả về tên các tham số được chỉ định rõ ràng trên dòng lệnh.
func setFlags(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

// apply ghi các tham số được chỉ định rõ ràng trên dòng lệnh vào cfg, tham số không được chỉ định giữ nguyên giá trị cũ.
// Lệnh quản trị chỉ duy trì một người dùng: khi chỉ đổi tên người dùng hoặc chỉ đổi mật khẩu, mục còn lại giữ nguyên giá trị cũ.
func (f *mgmtFlags) apply(cfg *config, set map[string]bool) error {
	if set["server"] {
		a, err := netip.ParseAddr(f.server)
		if err != nil || !a.Is4() {
			return fmt.Errorf("-server phải là địa chỉ IPv4, nhận được %q", f.server)
		}
		cfg.ServerIPv4 = a
	}
	if set["prefix"] {
		cfg.Prefixes = nil
		for _, s := range strings.Split(f.prefixes, ",") {
			p, err := netip.ParsePrefix(strings.TrimSpace(s))
			if err != nil || !p.Addr().Is6() || p.Addr().Is4In6() {
				return fmt.Errorf("-prefix không phải tiền tố IPv6: %q", s)
			}
			cfg.Prefixes = append(cfg.Prefixes, p.Masked())
		}
	}
	if set["local"] {
		cfg.LocalIPv4 = netip.Addr{}
		if f.local != "" {
			a, err := netip.ParseAddr(f.local)
			if err != nil || !a.Is4() {
				return fmt.Errorf("-local phải là địa chỉ IPv4, nhận được %q", f.local)
			}
			cfg.LocalIPv4 = a
		}
	}
	if set["mtu"] {
		cfg.MTU = f.mtu
	}
	if set["listen"] {
		cfg.Listen = f.listen
	}
	if set["user"] || set["pass"] {
		user, pass := firstUser(cfg.Users)
		if set["user"] {
			user = f.user
		}
		if set["pass"] {
			pass = f.pass
		}
		cfg.Users = map[string]string{user: pass}
	}
	return nil
}

// fillDefaults bổ sung các mục chưa cấu hình: MTU 1480, cổng cao ngẫu nhiên, tên người dùng và mật khẩu ngẫu nhiên.
func fillDefaults(cfg *config) error {
	if cfg.MTU == 0 {
		cfg.MTU = 1480
	}
	if cfg.Listen == "" {
		port, err := randomPort()
		if err != nil {
			return err
		}
		cfg.Listen = ":" + strconv.Itoa(port)
	}
	if user, pass := firstUser(cfg.Users); len(cfg.Users) <= 1 && (user == "" || pass == "") {
		if user == "" {
			user = "u" + strings.ToLower(crand.Text()[:9]) // bảng chữ cái base32, chỉ gồm chữ cái và chữ số
		}
		if pass == "" {
			pass = crand.Text() // 26 ký tự base32, khoảng 130 bit
		}
		cfg.Users = map[string]string{user: pass}
	}
	return nil
}

// firstUser trả về một người dùng trong cấu hình, trả về chuỗi rỗng khi không có người dùng.
func firstUser(users map[string]string) (string, string) {
	for u, p := range users {
		return u, p
	}
	return "", ""
}

// randomPort chọn một cổng TCP trống trên máy trong khoảng 20000~59999, bỏ qua các cổng số tròn như x000, x001.
func randomPort() (int, error) {
	for range 100 {
		p := 20000 + rand.IntN(40000)
		if p%1000 <= 1 {
			continue
		}
		l, err := net.Listen("tcp", ":"+strconv.Itoa(p))
		if err != nil {
			continue
		}
		l.Close()
		return p, nil
	}
	return 0, errors.New("không tìm được cổng trống, hãy chỉ định bằng -listen")
}

// validate kiểm tra cấu hình được lệnh quản trị tạo hoặc sửa.
func (c config) validate() error {
	switch {
	case !c.ServerIPv4.Is4():
		return errors.New("thiếu -server (Server IPv4 Address trên trang chi tiết tunnel HE)")
	case len(c.Prefixes) == 0:
		return errors.New("thiếu -prefix (Routed /64, /48)")
	case c.MTU < 1280 || c.MTU > 65535:
		return fmt.Errorf("MTU %d vượt ngoài phạm vi [1280, 65535]", c.MTU)
	}
	if _, port, err := net.SplitHostPort(c.Listen); err != nil || port == "" {
		return fmt.Errorf("định dạng -listen phải là [địa chỉ]:cổng, nhận được %q", c.Listen)
	}
	for u, p := range c.Users {
		bad := u == "" || p == "" || strings.ContainsAny(u+p, ":@/ ")
		for _, k := range []string{"-session-", "-ip-", "-ttl-", "-cidr-"} {
			bad = bad || strings.Contains(u, k)
		}
		if bad {
			return fmt.Errorf("tên người dùng %q hoặc mật khẩu không hợp lệ: không được để trống, không được chứa : @ / khoảng trắng; tên người dùng không được chứa -session-, -ip-, -ttl-, -cidr-", u)
		}
	}
	return nil
}

// firewallRules trả về các quy tắc INPUT của iptables cần cho phép: giao thức 41 từ máy chủ HE,
// và cổng proxy (không cần khi lắng nghe địa chỉ loopback).
func firewallRules(cfg config) [][]string {
	rules := [][]string{{"-p", "41", "-s", cfg.ServerIPv4.String(), "-j", "ACCEPT"}}
	host, port, err := net.SplitHostPort(cfg.Listen)
	if ip := net.ParseIP(host); err == nil && (ip == nil || !ip.IsLoopback()) {
		rules = append(rules, []string{"-p", "tcp", "-m", "tcp", "--dport", port, "-j", "ACCEPT"})
	}
	return rules
}

// unitFile tạo dịch vụ systemd: chạy bằng người dùng chuyên dụng, chỉ cấp CAP_NET_RAW, và bật các tùy chọn sandbox thông dụng.
func unitFile() string {
	return fmt.Sprintf(`[Unit]
Description=he-ipv6-pool: userspace HE 6in4 IPv6 proxy pool
After=network-online.target
Wants=network-online.target

[Service]
User=%[1]s
Group=%[1]s
ExecStart=%[2]s -c %[3]s
Restart=on-failure
RestartSec=5
AmbientCapabilities=CAP_NET_RAW
CapabilityBoundingSet=CAP_NET_RAW
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
LockPersonality=true
SystemCallArchitectures=native
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`, serviceUser, installBin, installConf)
}

// ---- Lệnh quản trị: install / config / uninstall (Linux + systemd, cần root) ----

func cmdInstall(args []string) error { return manage("install", args) }
func cmdConfig(args []string) error  { return manage("config", args) }

// manage triển khai install và config: đọc cấu hình đã cài, ghi đè bằng tham số dòng lệnh, bổ sung giá trị mặc định
// rồi ghi lại, đồng bộ tường lửa và khởi động lại dịch vụ. install còn cài binary, tạo người dùng và dịch vụ systemd;
// chạy lại install tương đương nâng cấp.
func manage(name string, args []string) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	var f mgmtFlags
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireRoot(); err != nil {
		return err
	}
	install := name == "install"
	old, err := loadConfig(installConf)
	if err != nil && (!install || !errors.Is(err, os.ErrNotExist)) {
		return fmt.Errorf("đọc %s: %w", installConf, err)
	}
	set := setFlags(fs)
	if !install && len(set) == 0 {
		printInfo(old)
		return nil
	}
	cfg := old
	if err := f.apply(&cfg, set); err != nil {
		return err
	}
	if err := fillDefaults(&cfg); err != nil {
		return err
	}
	if err := cfg.validate(); err != nil {
		return err
	}
	if install {
		if err := ensureUser(); err != nil {
			return err
		}
		if err := os.MkdirAll(installDir, 0o755); err != nil {
			return err
		}
		if err := installBinary(); err != nil {
			return err
		}
		if err := os.WriteFile(unitPath, []byte(unitFile()), 0o644); err != nil {
			return err
		}
	}
	if err := writeConfig(cfg); err != nil {
		return err
	}
	updateFirewall(old, cfg)
	if err := restartService(); err != nil {
		return err
	}
	printInfo(cfg)
	return nil
}

func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireRoot(); err != nil {
		return err
	}
	if err := sh("systemctl", "disable", "--now", serviceName); err != nil {
		fmt.Println("Cảnh báo:", err)
	}
	os.Remove(unitPath)
	sh("systemctl", "daemon-reload")
	if cfg, err := loadConfig(installConf); err == nil {
		updateFirewall(cfg, config{})
	}
	if err := os.RemoveAll(installDir); err != nil {
		return err
	}
	if _, err := osuser.Lookup(serviceUser); err == nil {
		if err := sh("userdel", serviceUser); err != nil {
			fmt.Println("Cảnh báo:", err)
		}
	}
	fmt.Printf("Đã gỡ cài đặt: dịch vụ %s, %s, người dùng %s và các quy tắc iptables tương ứng\n", serviceName, installDir, serviceUser)
	return nil
}

func requireRoot() error {
	if runtime.GOOS != "linux" {
		return errors.New("lệnh quản trị chỉ hỗ trợ Linux (systemd), trên hệ thống khác hãy chạy trực tiếp he-ipv6-pool -c config.json")
	}
	if os.Geteuid() != 0 {
		return errors.New("cần quyền root, hãy chạy bằng sudo")
	}
	return nil
}

// sh chạy lệnh bên ngoài, khi thất bại đưa đầu ra của lệnh vào thông tin lỗi.
func sh(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
}

func ensureUser() error {
	if _, err := osuser.Lookup(serviceUser); err == nil {
		return nil
	}
	shell := "/usr/sbin/nologin"
	if _, err := os.Stat(shell); err != nil {
		shell = "/sbin/nologin"
	}
	return sh("useradd", "--system", "--user-group", "--no-create-home", "--shell", shell, serviceUser)
}

// installBinary sao chép binary đang chạy vào thư mục cài đặt; ghi tệp tạm trước rồi rename, có thể thay thế khi dịch vụ đang chạy.
func installBinary() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	if self == installBin {
		return nil
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp := installBin + ".new"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, installBin)
}

// writeConfig ghi cấu hình: root:hepool 0640, người dùng dịch vụ chỉ được đọc, người dùng khác không thấy mật khẩu.
func writeConfig(cfg config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	g, err := osuser.LookupGroup(serviceUser)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return err
	}
	tmp := installConf + ".new"
	os.Remove(tmp)
	if err := os.WriteFile(tmp, append(b, '\n'), 0o640); err != nil {
		return err
	}
	if err := os.Chown(tmp, 0, gid); err != nil {
		return err
	}
	return os.Rename(tmp, installConf)
}

// updateFirewall xóa quy tắc iptables tương ứng với old, chèn quy tắc tương ứng với cfg và lưu lại;
// config zero nghĩa là không có quy tắc. Quy tắc thất bại chỉ in cảnh báo: tường lửa cũng có thể do công cụ khác quản lý.
func updateFirewall(old, cfg config) {
	if _, err := exec.LookPath("iptables"); err != nil {
		fmt.Println("Gợi ý: không có iptables, hãy tự cho phép giao thức 41 và cổng proxy")
		return
	}
	ipt := func(op string, rule []string) error { return sh("iptables", append([]string{op, "INPUT"}, rule...)...) }
	if old.ServerIPv4.IsValid() {
		for _, r := range firewallRules(old) {
			ipt("-D", r)
		}
	}
	if cfg.ServerIPv4.IsValid() {
		for _, r := range firewallRules(cfg) {
			if ipt("-C", r) == nil {
				continue
			}
			if err := ipt("-I", r); err != nil {
				fmt.Println("Cảnh báo:", err)
			}
		}
	}
	if _, err := exec.LookPath("netfilter-persistent"); err != nil {
		fmt.Println("Gợi ý: không có netfilter-persistent, quy tắc iptables sẽ mất sau khi khởi động lại, hãy tự lưu lại")
		return
	}
	if err := sh("netfilter-persistent", "save"); err != nil {
		fmt.Println("Cảnh báo:", err)
	}
}

// restartService khởi động lại dịch vụ và xác nhận nó thực sự chạy, khi thất bại kèm log gần đây.
func restartService() error {
	for _, args := range [][]string{{"daemon-reload"}, {"enable", serviceName}, {"restart", serviceName}} {
		if err := sh("systemctl", args...); err != nil {
			return err
		}
	}
	time.Sleep(2 * time.Second)
	if sh("systemctl", "is-active", "--quiet", serviceName) != nil {
		out, _ := exec.Command("journalctl", "-u", serviceName, "-n", "20", "--no-pager").CombinedOutput()
		return fmt.Errorf("dịch vụ không chạy bình thường, log gần đây:\n%s", out)
	}
	return nil
}

func printInfo(cfg config) {
	host, port, _ := net.SplitHostPort(cfg.Listen)
	if host == "" {
		host = "<IP máy chủ>"
	}
	addr := net.JoinHostPort(host, port)
	user, pass := firstUser(cfg.Users)
	fmt.Printf(`Phiên bản     %s
Tệp cấu hình  %s
Máy chủ HE    %s
Tiền tố       %v
Lắng nghe     %s (SOCKS5 và HTTP dùng chung)
Người dùng    %s
Mật khẩu      %s

  curl -x socks5h://%[6]s:%[7]s@%[8]s https://api64.ipify.org
  curl -x http://%[6]s:%[7]s@%[8]s https://api64.ipify.org

Hậu tố tên người dùng để chọn đầu ra (có thể kết hợp): -session-<id> cố định IP, -ttl-<phút> thời gian sống của IP cố định,
-cidr-64 luân chuyển theo /64; -ip-<địa chỉ> chỉ định IP (dấu : trong địa chỉ viết thành -).
Security group của nhà cung cấp cloud cũng cần cho phép giao thức 41 và TCP %[9]s.
Log: journalctl -u he-ipv6-pool -f (khi khởi động sẽ kiểm tra từng tiền tố xem có khả dụng không)
`, versionString(), installConf, cfg.ServerIPv4, cfg.Prefixes, cfg.Listen, user, pass, addr, port)
}
