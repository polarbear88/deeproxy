package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	socks5 "github.com/things-go/go-socks5"

	"deeproxy/config"
	"deeproxy/connreg"
	"deeproxy/internal/logging"
	"deeproxy/pool"
	"deeproxy/snapbuild"
	"deeproxy/snapshot"
	"deeproxy/stats"
	"deeproxy/store"
	"deeproxy/syslog"
)

// harness_test.go 是 v2 server 集成测试的公共脚手架（DRY）。
//
// 与 v1 的根本差异：v2 用户名 = user-group[-尾段]，鉴权需 SQLite 中存在 ProxyUser（bcrypt）
// 且被授权访问 group；路由由 group 的预编译规则引擎决定。故脚手架负责：建库 → 播种
// 用户/分组/规则/授权 → 物化快照 Holder + 注册表 + 计数器 + 审计 → 用新签名装配 server。

// deeproxyEnv 持有一套被测 deeproxy 运行期组件，便于测试断言统计/审计。
type deeproxyEnv struct {
	addr    string
	store   *store.Store
	holder  *snapshot.Holder
	counter *stats.Counter
	audit   *syslog.AuditBuffer
	conns   *connreg.Registry // 活跃连接登记表（实时连接功能）：供 never-reject 等测试观察
	cfg     *config.Config
}

// seedSpec 描述一次测试播种：一个分组（A/B）+ 其规则 + 一个授权用户 + （Type B）上游池。
type seedSpec struct {
	groupName string
	groupType store.GroupType
	rules     []store.Rule // match/action/orderIdx（rule_group 由脚手架建分组作用域组）
	upstreams []store.UpstreamProxy
	defAction string // 全局默认动作（config.DefaultAction）
	user      string // 代理用户名
	pwd       string // 代理用户明文密码
}

// startDeeproxyV2 按 seedSpec 建库播种并启动被测 server，返回环境与监听地址。
func startDeeproxyV2(t *testing.T, spec seedSpec) *deeproxyEnv {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "srv.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// 播种用户（明文密码：ProxyUser 不再 bcrypt）。
	u := &store.ProxyUser{Username: spec.user, Pwd: spec.pwd}
	if err := st.CreateProxyUser(u); err != nil {
		t.Fatalf("播种用户失败: %v", err)
	}

	// 播种分组。
	g := &store.Group{Name: spec.groupName, Type: spec.groupType}
	if err := st.CreateGroup(g); err != nil {
		t.Fatalf("播种分组失败: %v", err)
	}
	// 授权 user → group。
	if err := st.AddGroupUser(g.ID, u.ID); err != nil {
		t.Fatalf("播种授权失败: %v", err)
	}

	// 播种上游池（Type B）。
	for i := range spec.upstreams {
		up := spec.upstreams[i]
		up.GroupID = g.ID
		if up.Weight == 0 {
			up.Weight = 1
		}
		up.Enabled = true
		up.HealthState = true
		if err := st.CreateUpstream(&up); err != nil {
			t.Fatalf("播种上游失败: %v", err)
		}
	}

	// 播种规则（建一个 scope=group 的规则组并关联）。
	if len(spec.rules) > 0 {
		rg := &store.RuleGroup{Name: spec.groupName + "-rg", Scope: store.ScopeGroup}
		if err := st.CreateRuleGroup(rg); err != nil {
			t.Fatalf("播种规则组失败: %v", err)
		}
		for i, r := range spec.rules {
			r.RuleGroupID = rg.ID
			if r.OrderIdx == 0 {
				r.OrderIdx = i + 1
			}
			if err := st.CreateRule(&r); err != nil {
				t.Fatalf("播种规则失败: %v", err)
			}
		}
		if err := st.AddGroupRuleGroup(g.ID, rg.ID); err != nil {
			t.Fatalf("关联规则组失败: %v", err)
		}
	}

	def := spec.defAction
	if def == "" {
		def = "reject"
	}
	// 取消配置文件后，默认动作 / 空闲 / 嗅探 等迁入 system_setting，由 Rebuild 从库物化进快照。
	// 故测试需把期望的默认动作写入 system_setting（idle=300/sniff=on/300 用列默认即可），
	// 再 Rebuild；cfg 仅留监听地址等引导项。
	ss, err := st.GetSystemSetting()
	if err != nil {
		t.Fatalf("读取系统设置失败: %v", err)
	}
	ss.DefaultAction = def
	ss.LogLevel = "error"
	ss.IdleTimeoutSec = 300
	ss.SniffDomain = true
	ss.SniffTimeoutMs = 300
	if err := st.UpdateSystemSetting(ss); err != nil {
		t.Fatalf("写入系统设置失败: %v", err)
	}
	cfg := &config.Config{Listen: "127.0.0.1:0"}

	snap, err := snapbuild.Rebuild(st, cfg)
	if err != nil {
		t.Fatalf("物化快照失败: %v", err)
	}
	holder := snapshot.NewHolder(snap)
	registry := pool.NewRegistry()
	counter := stats.NewCounter()
	audit := syslog.NewAuditBuffer(100)
	logger := logging.New("error")

	conns := connreg.New()
	srv := New(holder, registry, counter, audit, conns, logger)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("deeproxy 监听失败: %v", err)
	}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = l.Close() })

	return &deeproxyEnv{
		addr:    l.Addr().String(),
		store:   st,
		holder:  holder,
		counter: counter,
		audit:   audit,
		conns:   conns,
		cfg:     cfg,
	}
}

// ---------- 复用自 v1 的测试基础设施 ----------

// fakeUpstream 是测试用“假上游 SOCKS5 代理”，记录目标 FQDN 与调用次数，
// 把所有 CONNECT 一律拨到 realTarget。
type fakeUpstream struct {
	count      atomic.Int64
	lastFQDN   atomic.Value
	realTarget string
}

func (f *fakeUpstream) start(t *testing.T, requireAuth map[string]string) string {
	t.Helper()
	opts := []socks5.Option{
		socks5.WithResolver(nopResolver{}),
		socks5.WithDialAndRequest(func(ctx context.Context, _, _ string, req *socks5.Request) (net.Conn, error) {
			f.count.Add(1)
			if req.DestAddr != nil {
				f.lastFQDN.Store(req.DestAddr.FQDN)
			}
			return net.Dial("tcp", f.realTarget)
		}),
	}
	if requireAuth != nil {
		opts = append(opts, socks5.WithCredential(socks5.StaticCredentials(requireAuth)))
	}
	srv := socks5.NewServer(opts...)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("假上游监听失败: %v", err)
	}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().String()
}

// encBase64 生成 base64("user:pwd@addr")，供 Type A 尾段使用。
func encBase64(upUser, upPwd, upAddr string) string {
	return base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%s:%s@%s", upUser, upPwd, upAddr))
}

const (
	atypIPv4   = 0x01
	atypDomain = 0x03
)

// socks5Connect 用原始字节做一次 SOCKS5 用户名/密码认证 + 指定命令请求，返回 REP 码与连接。
// password 参数为真实代理用户密码（v2 鉴权校验）。
func socks5Connect(t *testing.T, proxyAddr, username, password string, cmd, atyp byte, host string, port int) (byte, net.Conn) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 3*time.Second)
	if err != nil {
		t.Fatalf("连接 deeproxy 失败: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	if _, err := conn.Write([]byte{0x05, 0x01, 0x02}); err != nil {
		t.Fatalf("写方法协商失败: %v", err)
	}
	rep := make([]byte, 2)
	if _, err := io.ReadFull(conn, rep); err != nil {
		t.Fatalf("读方法回复失败: %v", err)
	}
	if rep[0] != 0x05 || rep[1] != 0x02 {
		t.Fatalf("方法协商未选用户名/密码认证: %v", rep)
	}

	authMsg := []byte{0x01, byte(len(username))}
	authMsg = append(authMsg, username...)
	authMsg = append(authMsg, byte(len(password)))
	authMsg = append(authMsg, password...)
	if _, err := conn.Write(authMsg); err != nil {
		t.Fatalf("写认证失败: %v", err)
	}
	authRep := make([]byte, 2)
	if _, err := io.ReadFull(conn, authRep); err != nil {
		t.Fatalf("读认证回复失败: %v", err)
	}
	if authRep[1] != 0x00 {
		_ = conn.Close()
		return 0xFF, nil // 认证失败标记
	}

	msg := []byte{0x05, cmd, 0x00, atyp}
	switch atyp {
	case atypIPv4:
		ip := net.ParseIP(host).To4()
		msg = append(msg, ip...)
	case atypDomain:
		msg = append(msg, byte(len(host)))
		msg = append(msg, host...)
	}
	msg = append(msg, byte(port>>8), byte(port&0xff))
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("写请求失败: %v", err)
	}

	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		t.Fatalf("读回复头失败: %v", err)
	}
	switch head[3] {
	case atypIPv4:
		io.ReadFull(conn, make([]byte, 4+2))
	case atypDomain:
		l := make([]byte, 1)
		io.ReadFull(conn, l)
		io.ReadFull(conn, make([]byte, int(l[0])+2))
	default:
		io.ReadFull(conn, make([]byte, 16+2))
	}
	_ = conn.SetDeadline(time.Time{})
	return head[1], conn
}

// httpGetOver 在已建立的连接上发起一次 HTTP GET，返回响应体。
// 写完请求后半关闭写方向：请求用 "Connection: close" 后无更多客户端数据，
// 半关闭可让代理上行中继（io.Copy 客户端→目标）干净结束，从而连接收尾、埋点/审计落地。
func httpGetOver(t *testing.T, conn net.Conn, host string) string {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	req := fmt.Sprintf("GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("写 HTTP 请求失败: %v", err)
	}
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	data, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("读 HTTP 响应失败: %v", err)
	}
	return string(data)
}

// newTarget 启动返回固定 body 的 httptest 目标。
func newTarget(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "HELLO-DEEPROXY")
	}))
	t.Cleanup(ts.Close)
	return ts, ts.Listener.Addr().String()
}

// startEcho 启动回显服务器（用于嗅探回放验证）。
func startEcho(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("回显服务器监听失败: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { defer c.Close(); _, _ = io.Copy(c, c) }(c)
		}
	}()
	return l.Addr().String()
}

// splitHostPort 把 "host:port" 拆成 host 与 int port（测试便捷）。
func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("拆分地址失败: %v", err)
	}
	port, _ := strconv.Atoi(portStr)
	return host, port
}

// contains 是不依赖 strings 包的简单子串判断。
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
