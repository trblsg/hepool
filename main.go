// he-ipv6-pool: không tạo card mạng, kết nối tunnel HE 6in4 trong không gian người dùng,
// và cho phép ra ngoài bằng bất kỳ IPv6 nào trong pool thông qua proxy SOCKS5/HTTP.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"

	"github.com/wsyqhh/he-ipv6-pool/proxy"
	"github.com/wsyqhh/he-ipv6-pool/tunnel"
)

// version được quy trình phát hành chèn qua -ldflags "-X main.version=<tag>"; khi để trống thì dùng
// thông tin build của Go, tức phiên bản module của go install …@v0.1.0, hoặc mã commit khi build từ mã nguồn
// (ví dụ v0.0.0-20261022…-abcdef+dirty).
var version string

func versionString() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

type config struct {
	ServerIPv4 netip.Addr        `json:"server_ipv4"`
	LocalIPv4  netip.Addr        `json:"local_ipv4"`
	Prefixes   []netip.Prefix    `json:"prefixes"`
	MTU        int               `json:"mtu"`
	Listen     string            `json:"listen"` // địa chỉ lắng nghe dùng chung cho SOCKS5 và HTTP
	Users      map[string]string `json:"users"`
}

func main() {
	if len(os.Args) > 1 {
		cmds := map[string]func([]string) error{"install": cmdInstall, "config": cmdConfig, "uninstall": cmdUninstall}
		if cmd, ok := cmds[os.Args[1]]; ok {
			if err := cmd(os.Args[2:]); err != nil && !errors.Is(err, flag.ErrHelp) {
				fmt.Fprintln(os.Stderr, "Lỗi:", err)
				os.Exit(1)
			}
			return
		}
	}
	path := flag.String("c", "config.json", "Đường dẫn tệp cấu hình")
	verbose := flag.Bool("v", false, "Xuất log debug (mỗi yêu cầu một dòng), tương đương biến môi trường LOG_LEVEL=debug")
	showVersion := flag.Bool("version", false, "Hiển thị phiên bản")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), `Cách dùng:
  he-ipv6-pool [-c config.json] [-v]                                  Chạy trực tiếp
  he-ipv6-pool -version                                               Hiển thị phiên bản
  he-ipv6-pool install -server <HE IPv4> -prefix <tiền tố[,tiền tố]>  Cài đặt dưới dạng dịch vụ systemd
  he-ipv6-pool config [tùy chọn]                                      Xem hoặc sửa cấu hình dịch vụ đã cài đặt
  he-ipv6-pool uninstall                                              Gỡ cài đặt
Thêm -h vào lệnh con để xem tùy chọn.

`)
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println(versionString())
		return
	}
	if err := setupLog(*verbose); err != nil {
		fmt.Fprintln(os.Stderr, "Lỗi:", err)
		os.Exit(1)
	}
	if err := run(*path); err != nil {
		slog.Error("Thoát", "err", err)
		os.Exit(1)
	}
}

// setupLog đặt mức log theo -v hoặc biến môi trường LOG_LEVEL (debug, info, warn, error), -v được ưu tiên;
// khi chạy dưới systemd (journald đặt JOURNAL_STREAM) thì không xuất thời gian, journald tự ghi lại.
func setupLog(verbose bool) error {
	if os.Getenv("JOURNAL_STREAM") != "" {
		log.SetFlags(0)
	}
	level := slog.LevelInfo
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		if err := level.UnmarshalText([]byte(v)); err != nil {
			return fmt.Errorf("biến môi trường LOG_LEVEL=%q không hợp lệ, có thể chọn debug, info, warn, error", v)
		}
	}
	if verbose {
		level = slog.LevelDebug
	}
	slog.SetLogLoggerLevel(level)
	return nil
}

func loadConfig(path string) (config, error) {
	var cfg config
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("phân tích cấu hình %s: %w", path, err)
	}
	return cfg, nil
}

// envVars là quan hệ giữa biến môi trường và tùy chọn install; biến môi trường ghi đè mục cùng tên trong tệp cấu hình.
var envVars = []struct{ name, flag string }{
	{"HE_SERVER", "server"}, {"HE_PREFIXES", "prefix"}, {"HE_LOCAL", "local"}, {"HE_MTU", "mtu"},
	{"PROXY_LISTEN", "listen"}, {"PROXY_USER", "user"}, {"PROXY_PASS", "pass"},
}

// applyEnv dùng biến môi trường ghi đè cfg, cách phân tích và kiểm tra giống tùy chọn cùng tên của install; trả về có đặt biến nào không.
func applyEnv(cfg *config) (bool, error) {
	used := false
	for _, e := range envVars {
		v, ok := os.LookupEnv(e.name)
		if !ok {
			continue
		}
		used = true
		// apply chỉ đọc trường tương ứng với mục trong set, nên các trường chuỗi đều điền v
		f := mgmtFlags{server: v, prefixes: v, local: v, listen: v, user: v, pass: v}
		if e.flag == "mtu" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return used, fmt.Errorf("biến môi trường %s=%q không phải số nguyên", e.name, v)
			}
			f.mtu = n
		}
		if err := f.apply(cfg, map[string]bool{e.flag: true}); err != nil {
			return used, fmt.Errorf("biến môi trường %s: %w", e.name, err)
		}
	}
	return used, nil
}

// run đọc tệp cấu hình và ghi đè bằng biến môi trường; khi tệp cấu hình không tồn tại nhưng có đặt biến môi trường
// thì chỉ dùng biến môi trường (phù hợp triển khai bằng container).
func run(path string) error {
	cfg, err := loadConfig(path)
	noFile := errors.Is(err, os.ErrNotExist)
	if err != nil && !noFile {
		return err
	}
	used, envErr := applyEnv(&cfg)
	switch {
	case envErr != nil:
		return envErr
	case noFile && !used:
		return fmt.Errorf("%w (cũng có thể cấu hình bằng biến môi trường HE_SERVER, HE_PREFIXES, PROXY_LISTEN, v.v.)", err)
	case !cfg.ServerIPv4.Is4():
		return errors.New("thiếu server_ipv4 (biến môi trường HE_SERVER)")
	case len(cfg.Prefixes) == 0:
		return errors.New("thiếu prefixes (biến môi trường HE_PREFIXES)")
	case cfg.Listen == "":
		return errors.New("thiếu listen (biến môi trường PROXY_LISTEN)")
	}
	for u, p := range cfg.Users {
		if u == "" || p == "" {
			return errors.New("tên người dùng và mật khẩu proxy đều không được để trống (biến môi trường PROXY_USER, PROXY_PASS)")
		}
	}
	t, err := tunnel.New(tunnel.Config{ServerIPv4: cfg.ServerIPv4, LocalIPv4: cfg.LocalIPv4, Prefixes: cfg.Prefixes, MTU: cfg.MTU})
	if err != nil {
		return err
	}
	defer t.Close()
	l, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	defer l.Close() // chạy trước t.Close (defer vào sau ra trước)
	if len(cfg.Users) == 0 && !l.Addr().(*net.TCPAddr).IP.IsLoopback() {
		slog.Warn("Đang lắng nghe trên địa chỉ không phải loopback và chưa cấu hình users, bất kỳ ai cũng có thể dùng proxy này", "listen", l.Addr())
	}
	slog.Info("Đã khởi động", startupAttrs(cfg, l.Addr())...)

	p := &proxy.Proxy{Tunnel: t, Users: cfg.Users}
	errc := make(chan error, 1)
	go func() { errc <- p.Serve(l) }()
	go checkTunnel(t, cfg.ServerIPv4)
	go logStats(p, statsInterval)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case s := <-sig:
		slog.Info("Đã nhận tín hiệu, thoát", "signal", s)
		return nil
	case err := <-errc:
		return err
	}
}

func startupAttrs(cfg config, listen net.Addr) []any {
	local, mtu, auth := "Tự động", cfg.MTU, "Tắt"
	if cfg.LocalIPv4.IsValid() {
		local = cfg.LocalIPv4.String()
	}
	if mtu == 0 {
		mtu = 1480
	}
	if n := len(cfg.Users); n > 0 {
		auth = fmt.Sprintf("%d người dùng", n)
	}
	return []any{"version", versionString(), "server", cfg.ServerIPv4, "local", local, "mtu", mtu,
		"prefixes", cfg.Prefixes, "listen", listen, "protocols", "socks5+http", "auth", auth}
}

// checkTargets là các đích kết nối kiểm tra khi khởi động: địa chỉ anycast IPv6 của DNS công cộng Cloudflare và Google, ổn định lâu dài.
var checkTargets = []netip.AddrPort{
	netip.MustParseAddrPort("[2606:4700:4700::1111]:443"),
	netip.MustParseAddrPort("[2001:4860:4860::8888]:443"),
}

// checkTunnel kiểm tra xem mỗi tiền tố có nhận được gói phản hồi không. Kết quả chỉ ghi vào log, không ảnh hưởng
// việc dịch vụ chạy: thoát khi HE gặp sự cố tạm thời sẽ khiến systemd khởi động lại liên tục; cũng không loại bỏ
// tiền tố không thông, nếu không IP cố định của mọi phiên sẽ thay đổi.
func checkTunnel(t *tunnel.Tunnel, server netip.Addr) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ok := 0
	for _, r := range t.Check(ctx, checkTargets) {
		if r.Err != nil {
			slog.Warn("Kiểm tra tunnel thất bại: tiền tố này không nhận được gói phản hồi, có thể HE chưa định tuyến nó tới tunnel này", "prefix", r.Prefix, "src", r.Src, "err", r.Err)
			continue
		}
		ok++
		slog.Info("Kiểm tra tunnel đạt", "prefix", r.Prefix, "src", r.Src, "rtt", r.RTT.Round(time.Millisecond))
	}
	if ok == 0 {
		slog.Error("Tất cả tiền tố đều không thông: hãy kiểm tra tường lửa và security group của nhà cung cấp cloud đã cho phép giao thức 41 từ HE chưa, "+
			"endpoint IPv4 điền trong bảng điều khiển HE có phải IP công cộng của máy này không, và tunnel HE của kernel có đang được bật đồng thời hay không", "server", server)
	}
}

const statsInterval = 10 * time.Minute

// logStats mỗi interval xuất một lần thống kê yêu cầu trong khoảng thời gian đó; không xuất khi không có hoạt động nào.
func logStats(p *proxy.Proxy, interval time.Duration) {
	var prev proxy.Stats
	for range time.Tick(interval) {
		s := p.Stats()
		d := s.Sub(prev)
		prev = s
		if d.Requests() == 0 && s.Active == 0 {
			continue
		}
		slog.Info(fmt.Sprintf("Trong %d phút gần đây", int(interval.Minutes())), d.Attrs()...)
	}
}
