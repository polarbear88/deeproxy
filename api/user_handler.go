// 本文件实现代理用户（ProxyUser）的 CRUD 与分组授权（AC-23/30）。
//
// 端点对齐前端契约：路径 /proxy-users；授权为用户维度 POST /proxy-users/:id/groups。
// 代理用户与后台管理员完全独立：代理用户只能连 SOCKS5 代理、不能登录后台。
// 密码明文存储（用户决策，仅 ProxyUser；管理员仍 bcrypt），列表/详情不回显密码。
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"deeproxy/store"
)

// userReq 是代理用户创建/更新请求体。更新时 Password 为空表示不改密码。
type userReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Remark   string `json:"remark"`
}

// userResp 是代理用户视图（admin-only 端点，requireAuth）。
//
// 含 pwd（明文连接密码）：D3 决策——「复制代理地址」按钮需产出可直接使用的
// socks5://<user>-<group>:<pwd>@<server>:<port> URL，故管理端列表回传明文密码。
// 安全前提：本端点经 requireAuth 保护、仅管理员可访问；ProxyUser 密码本就明文存储
// （AC-43，连接鉴权微秒级明文比对，仅后台管理员密码用 bcrypt），故此处回传可接受。
type userResp struct {
	ID        int64   `json:"id"`
	Username  string  `json:"username"`
	Pwd       string  `json:"pwd"` // 明文连接密码（D3：供「复制代理地址」拼可用 URL）
	Remark    string  `json:"remark"`
	AllGroups bool    `json:"allGroups"` // DEC-B1：是否授权全部分组（前端回显授权状态）
	GroupIDs  []int64 `json:"groupIds"`
}

// buildUserGroupIndex 反查每个用户被授权的分组 ID（一次性读全量关联，避免 N+1）。
func (a *App) buildUserGroupIndex() (map[int64][]int64, error) {
	gus, err := a.store.ListGroupUsers()
	if err != nil {
		return nil, err
	}
	idx := make(map[int64][]int64)
	for _, gu := range gus {
		idx[gu.UserID] = append(idx[gu.UserID], gu.GroupID)
	}
	return idx, nil
}

func toUserResp(u store.ProxyUser, groupIDs []int64) userResp {
	if groupIDs == nil {
		groupIDs = []int64{}
	}
	return userResp{ID: u.ID, Username: u.Username, Pwd: u.Pwd, Remark: u.Remark, AllGroups: u.AllGroups, GroupIDs: groupIDs}
}

func (a *App) handleListUsers(c *gin.Context) {
	users, err := a.store.ListProxyUsers()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取用户失败: "+err.Error())
		return
	}
	idx, err := a.buildUserGroupIndex()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取授权关系失败: "+err.Error())
		return
	}
	out := make([]userResp, 0, len(users))
	for _, u := range users {
		out = append(out, toUserResp(u, idx[u.ID]))
	}
	respondOK(c, out)
}

func (a *App) handleCreateUser(c *gin.Context) {
	var req userReq
	if !bindJSON(c, &req) {
		return
	}
	if req.Username == "" || req.Password == "" {
		respondError(c, http.StatusBadRequest, "用户名与密码不能为空")
		return
	}
	// ProxyUser 密码明文存储（用户决策：避免每连接 bcrypt ~49ms 拖慢建连，AC-43）。
	// 仅 ProxyUser 如此；后台管理员密码仍 bcrypt。
	u := store.ProxyUser{Username: req.Username, Pwd: req.Password, Remark: req.Remark}
	if err := a.store.CreateProxyUser(&u); err != nil {
		respondError(c, http.StatusInternalServerError, "新增用户失败: "+err.Error())
		return
	}
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("创建代理用户", "id", u.ID, "username", u.Username)
	respondOK(c, toUserResp(u, nil))
}

func (a *App) handleUpdateUser(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	old, err := a.store.GetProxyUser(id)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取用户失败: "+err.Error())
		return
	}
	if old == nil {
		respondError(c, http.StatusNotFound, "用户不存在")
		return
	}
	var req userReq
	if !bindJSON(c, &req) {
		return
	}
	old.Username = req.Username
	old.Remark = req.Remark
	// 密码非空才更新（空 = 保留原密码）；ProxyUser 明文存储。
	if req.Password != "" {
		old.Pwd = req.Password
	}
	if err := a.store.UpdateProxyUser(old); err != nil {
		respondError(c, http.StatusInternalServerError, "更新用户失败: "+err.Error())
		return
	}
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("更新代理用户", "id", old.ID, "username", old.Username)
	idx, _ := a.buildUserGroupIndex()
	respondOK(c, toUserResp(*old, idx[old.ID]))
}

func (a *App) handleDeleteUser(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if err := a.store.DeleteProxyUser(id); err != nil {
		respondError(c, http.StatusInternalServerError, "删除用户失败: "+err.Error())
		return
	}
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("删除代理用户", "id", id)
	respondOK(c, nil)
}

// setUserGroupsReq 是设置用户授权分组的请求体。
//
// 「并存」语义（DEC-B1，Critic 钉死）：allGroups 与 groupIds 是两个【独立维度】，
// 互不清除。两字段均为指针 = 可选：
//   - allGroups 非 nil 才更新「授权全部分组」通配标志；切换它【绝不】动 group_user 精细行。
//   - groupIds  非 nil 才覆盖式设置逐组精细授权；为 nil 时保留原有精细授权不变。
// 故「开 all_groups → 关 all_groups」不会丢失用户原有的逐组授权。
type setUserGroupsReq struct {
	AllGroups *bool   `json:"allGroups"`
	GroupIDs  []int64 `json:"groupIds"`
}

// handleSetUserGroups 设置某代理用户的授权（AC-1.1/1.2，路由 /proxy-users/:id/groups）。
func (a *App) handleSetUserGroups(c *gin.Context) {
	uid, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req setUserGroupsReq
	if !bindJSON(c, &req) {
		return
	}

	// 1) 通配标志（独立维度，永不清精细行）。仅在显式传了 allGroups 时更新。
	if req.AllGroups != nil {
		if err := a.store.SetUserAllGroups(uid, *req.AllGroups); err != nil {
			respondError(c, http.StatusInternalServerError, "设置授权全部分组失败: "+err.Error())
			return
		}
	}
	// 2) 逐组精细授权（独立维度）。仅在显式传了 groupIds 时覆盖设置；为 nil 则保留原有不动。
	if req.GroupIDs != nil {
		if err := a.store.SetUserGroups(uid, req.GroupIDs); err != nil {
			respondError(c, http.StatusInternalServerError, "设置用户授权失败: "+err.Error())
			return
		}
	}

	if !a.rebuildAndSwap(c) {
		return
	}
	allGroups := false
	if req.AllGroups != nil {
		allGroups = *req.AllGroups
	}
	a.logger.Info("设置用户授权分组", "userId", uid, "allGroups", allGroups, "groupIds", req.GroupIDs)
	respondOK(c, nil)
}
