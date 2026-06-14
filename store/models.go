// Package store 是 deeproxy v2 的存储层，封装 SQLite 持久化。
//
// 设计要点（与转发热路径完全解耦）：
//   - SQLite 是分组/池/规则/用户/授权/系统设置/统计的【权威数据源】；
//     运行期转发只读由本层物化出的内存快照（见 config 包），绝不在转发路径同步访问 DB。
//   - 开启 WAL 模式并用【单写协程】串行化所有写操作（见 db.go），避免写锁竞争。
//   - 统计只存【分钟级聚合时间桶】，按保留期自动清理，控制表增长（见 traffic_stat_repo.go）。
//
// 本文件集中定义各实体的 Go 结构体（与表结构一一对应），全部中文注释。
package store

import (
	"time"

	"deeproxy/domain"
)

// 枚举类型与常量统一来自零依赖叶子包 domain（AC-43：让转发链脱离 store→database/sql 依赖）。
// store 这里用类型别名 + 常量再导出，使本包既复用 domain 的唯一定义，又保持
// store.GroupType / store.TypeA 等历史 API 不变（调用方零改动，DRY）。
type (
	// GroupType 是代理分组类型（A/B），定义见 domain 包。
	GroupType = domain.GroupType
	// HealthMode 是健康检查探测方式（ping/url），定义见 domain 包。
	HealthMode = domain.HealthMode
	// RuleScope 是规则组作用域（global/group），定义见 domain 包。
	RuleScope = domain.RuleScope
)

const (
	// TypeA 动态上游组。
	TypeA = domain.TypeA
	// TypeB 代理池组。
	TypeB = domain.TypeB

	// HealthPing ping 探测。
	HealthPing = domain.HealthPing
	// HealthURL URL 探测（默认）。
	HealthURL = domain.HealthURL

	// ScopeGlobal 全局规则组。
	ScopeGlobal = domain.ScopeGlobal
	// ScopeGroup 分组规则组。
	ScopeGroup = domain.ScopeGroup
)

// SystemSetting 是单行系统配置（含全局唯一管理员凭据与各项默认值）。
// 之所以与管理员合并在一行：后台仅单一管理员，无需独立 admin 表，简化存取。
type SystemSetting struct {
	ID                int    // 固定为 1（单行配置）
	AdminUser         string // 后台管理员用户名（仅用于登录 Web 后台）
	AdminPwdHash      string // 管理员密码 bcrypt 哈希（未设置时为空 → 触发首次设置引导）
	StatRetentionDays int    // 统计聚合桶保留天数（默认 30）
	// 健康检查默认值：新建 Type B 分组未显式配置时的兜底。
	HCDefaultMode      HealthMode // 默认探测方式（url）
	HCDefaultURL       string     // 默认探测 URL
	HCDefaultInterval  int        // 默认探测间隔（秒，默认 600）
	HCDefaultFailThld  int        // 默认连续失败阈值（默认 3）
	HCDefaultRecvThld  int        // 默认连续成功恢复阈值（默认 2）

	// —— v2 运行期动态设置（取消配置文件后，原 YAML 引导项迁入此表，可在后台热改）——
	// 这 5 项均不在字节中继热循环里：DefaultAction 在规则匹配时用、IdleTimeoutSec/Sniff* 在
	// 每连接建连时从快照读一次（纳秒级）、LogLevel 经 slog.LevelVar 原子生效。故动态化对转发零影响。
	DefaultAction  string // 规则全不命中时的全局默认动作 forward/direct/reject（默认 forward）
	LogLevel       string // 日志级别 debug/info/warn/error（默认 info，后台改后经 LevelVar 热生效）
	IdleTimeoutSec int    // 连接双向空闲超时（秒，默认 300，新连接生效）
	SniffDomain    bool   // 是否启用域名嗅探（IP 未命中 ip-cidr 时嗅探 SNI/Host，默认开）
	SniffTimeoutMs int    // 嗅探首包等待超时（毫秒，默认 300）

	// —— v2 批量增强新增 ——
	// ServerAddr 是后台展示用的服务器域名/IP（仅用于生成「复制代理地址」与连接示例文案，
	// 并非 SOCKS5 监听绑定地址）；首次为空时由后端探测本机非回环 IP 兜底，用户可手填覆盖。
	ServerAddr string
	// ProbePoolSize 是健康检查全局协程池大小（所有分组所有探测共用一个池，DEC-C1，默认 150）。
	// 每轮 scanOnce 开头重新读取生效（不做在途 resize）。
	ProbePoolSize int

	UpdatedAt          time.Time  // 最后更新时间
}

// IsAdminConfigured 返回管理员是否已配置（用于首次启动引导判断 AC-19）。
func (s *SystemSetting) IsAdminConfigured() bool {
	return s.AdminUser != "" && s.AdminPwdHash != ""
}

// ProxyUser 是代理用户（只能连 SOCKS5 代理，不能登录后台）。
// 鉴权 = 明文密码比对 + 验分组授权（与后台管理员两套凭据完全独立）。
//
// 密码存储说明（用户决策）：ProxyUser 的 SOCKS5 连接密码改为【明文存储】，不再 bcrypt。
// 原因：bcrypt 每连接 ~49ms 是转发建连延迟瓶颈（AC-43），而代理密码仅用于 SOCKS5 连接
// 鉴权、明文比对为微秒级；故取消哈希。⚠️ 仅 ProxyUser 如此；后台管理员密码
// （SystemSetting.AdminPwdHash）仍用 bcrypt。
type ProxyUser struct {
	ID        int64
	Username  string // 代理用户名（连接用户名 user-group 的首段）
	Pwd       string // 连接密码（明文存储，鉴权时直接比对）
	Remark    string // 备注
	// AllGroups 是「授权全部分组」通配标志（DEC-B1）：为 true 时该用户可访问所有分组，
	// 且与逐组授权（group_user）【并存】——它是独立布尔标志，切换它【永不清空】精细授权行；
	// IsAuthorized = all_groups 命中 OR 精细行命中。
	AllGroups bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Group 是 SOCKS5 代理分组（前端称“代理组 / Proxy Group”）。
// Type A 不含上游池；Type B 含多条 UpstreamProxy 并内嵌健康检查配置。
type Group struct {
	ID        int64
	Name      string    // 分组名称
	Remark    string    // 备注
	Type      GroupType // A / B
	// 健康检查配置（仅 Type B 有意义；Type A 忽略，不参与健康检查 G2）。
	HCEnabled   bool       // 是否开启健康检查
	HCMode      HealthMode // ping / url
	HCURL       string     // url 模式的探测地址
	HCInterval  int        // 探测间隔（秒）
	HCFailThld  int        // 连续失败阈值（标记不可用）
	HCRecvThld  int        // 连续成功阈值（恢复可用）
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// UpstreamProxy 是 Type B 分组下的一条固定上游 SOCKS5 代理。
//   - UsernameTemplate 是上游认证用户名模板，可含任意命名占位 {var}（如 acct-{region}-{session}）；
//     运行期由客户端尾段变量映射替换占位（缺值补空、多余忽略、顺序无关）。
//   - HealthState 是探测出的健康状态，由健康检查 worker 维护（持久化便于前端展示与重启展示初值）。
type UpstreamProxy struct {
	ID               int64
	GroupID          int64  // 所属 Type B 分组
	Host             string // 上游主机（域名或 IP）
	Port             int    // 上游端口
	User             string // 上游认证用户名（不含模板占位时即定值）
	UsernameTemplate string // 上游认证用户名模板（含 {var} 占位；为空则用 User）
	Pwd              string // 上游认证密码
	Weight           int    // 加权轮训权重（>=1）
	Enabled          bool   // 是否启用（手动启停，false 时不参与选择 AC-18）
	HealthState      bool   // 最近一次探测健康状态（true=健康；初值 true）
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// RuleGroup 是规则组（多对多关联分组，scope=global 时对所有连接生效）。
type RuleGroup struct {
	ID        int64
	Name      string
	Scope     RuleScope // global / group
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Rule 是规则组下的一条规则。
//   - Match 形如 "domain-suffix:google.com"（沿用 v1 语法，复用 rule 包匹配核心）。
//   - OrderIdx 决定组内书写顺序（顺序首匹配）。
type Rule struct {
	ID          int64
	RuleGroupID int64
	Match       string // 匹配表达式：domain:/domain-suffix:/ip-cidr:
	Action      string // forward / direct / reject
	OrderIdx    int    // 组内顺序（升序即书写顺序）
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// GroupUser 是分组↔代理用户的授权关系（多对多）。
type GroupUser struct {
	GroupID int64
	UserID  int64
}

// GroupRuleGroup 是分组↔规则组的关联关系（多对多）。
// scope=global 的规则组无需在此关联（对所有分组隐式生效）。
type GroupRuleGroup struct {
	GroupID     int64
	RuleGroupID int64
}

// TrafficStat 是分钟级流量聚合桶（唯一存储粒度）。
//   - 维度：GroupID + UserID + BucketTime（截断到分钟）。
//   - 7d 等长窗口在查询期用 strftime 降采样到小时，避免双写两套桶。
//   - 由 stats flush worker 每 5~10s 批量 upsert（累加），由清理 worker 按保留期删除过期行。
type TrafficStat struct {
	GroupID    int64
	UserID     int64
	BucketTime time.Time // 截断到分钟的桶时间（UTC 存储）
	UpBytes    int64     // 该桶上行字节累计
	DownBytes  int64     // 该桶下行字节累计
	ReqCount   int64     // 该桶请求数累计
}
