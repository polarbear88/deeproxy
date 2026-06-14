// 本文件提供【权威功能状态表】（T6.3 / AC-6.3/6.4）。
//
// 背景：部分功能首版未实现（如 Top 域名排行依赖 domain 维度埋点），过去靠分散的
// 响应头/魔法字符串临时标注，前端难以集中判断。这里建立单一权威来源：一张
// 编译期常量表声明每个功能的实现状态，前端启动时拉一次据此决定显示「占位/暂不支持」。
//
// 维护约定：新功能从「未实现」改为「已实现」时，在此把状态改为 FeatureImplemented，
// 即对全前端生效（DRY，避免多处魔法字符串漂移）。
package api

import (
	"github.com/gin-gonic/gin"
)

// featureState 是功能实现状态枚举。
type featureState string

const (
	// FeatureImplemented 已实现可用。
	FeatureImplemented featureState = "implemented"
	// FeatureNotImplemented 首版未实现（前端显示占位/暂不支持）。
	FeatureNotImplemented featureState = "not-implemented"
)

// featureStatusTable 是权威功能状态表（单一真源）。
// key 为前端约定的功能标识；value 为当前实现状态。
var featureStatusTable = map[string]featureState{
	// Top 域名排行：依赖 CONNECT 目标域名按连接维度埋点（traffic_stat 仅 group/user 桶，
	// 无 domain 维度），首版未实现。对应 handleTop 的 kind=domain 返回空数组占位。
	"dashboard.top.domain": FeatureNotImplemented,
}

// handleFeatureStatus 返回权威功能状态表（T6.3，路由 GET /feature-status）。
// 前端据此集中判断哪些功能显示「占位/暂不支持」，避免散落的魔法字符串。
func (a *App) handleFeatureStatus(c *gin.Context) {
	// 拷贝为可序列化 map（值转字符串），保持响应稳定。
	out := make(map[string]string, len(featureStatusTable))
	for k, v := range featureStatusTable {
		out[k] = string(v)
	}
	respondOK(c, out)
}
