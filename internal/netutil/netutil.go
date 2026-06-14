// Package netutil 提供网络相关的公共小工具（本机 IP 探测等），供管理面复用（DRY）。
//
// 为什么独立建包而非内联到 handler：本机 IP 探测是可复用的纯逻辑（无 HTTP 依赖），
// 抽到 internal 公共包便于单测与多处复用；且无现成 util 可复用（Critic 修订）。
package netutil

import "net"

// DetectLocalIP 探测本机第一个非回环 IPv4 地址，用于「服务器域名/IP」字段的首次默认值。
//
// 多网卡/容器消歧：遍历所有接口地址，取第一个【非回环、非链路本地、IPv4】地址；
// 探测失败或无合适地址时返回空串，由用户在后台手填覆盖（此值仅作连接示例文案、非绑定地址）。
//
// 为什么用 net.InterfaceAddrs 而非 Dial 一个外网地址：InterfaceAddrs 不产生任何网络流量、
// 不依赖外网可达性，纯本地内省，适合启动/设置时调用；缺点是多网卡时取「第一个」，
// 故消歧规则明确为「第一个非回环 IPv4」，并允许用户手填覆盖。
func DetectLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		// InterfaceAddrs 返回 *net.IPNet（含掩码）或 *net.IPAddr，取其中的 IP。
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		default:
			continue
		}
		// 跳过回环（127.0.0.1/::1）与链路本地（169.254.x / fe80::），只取可对外展示的地址。
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		// 只取 IPv4（To4 非 nil 即 IPv4）。IPv6 展示/连接示例较复杂，首版用 IPv4。
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}
