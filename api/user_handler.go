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

// userResp 是代理用户视图（不含密码哈希；含被授权的分组 ID 列表）。
type userResp struct {
	ID       int64   `json:"id"`
	Username string  `json:"username"`
	Remark   string  `json:"remark"`
	GroupIDs []int64 `json:"groupIds"`
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
	return userResp{ID: u.ID, Username: u.Username, Remark: u.Remark, GroupIDs: groupIDs}
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

// setUserGroupsReq 是设置用户授权分组的请求体（用户维度覆盖式）。
type setUserGroupsReq struct {
	GroupIDs []int64 `json:"groupIds"`
}

// handleSetUserGroups 覆盖式设置某代理用户被授权的分组（AC-30，路由 /proxy-users/:id/groups）。
func (a *App) handleSetUserGroups(c *gin.Context) {
	uid, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req setUserGroupsReq
	if !bindJSON(c, &req) {
		return
	}
	if err := a.store.SetUserGroups(uid, req.GroupIDs); err != nil {
		respondError(c, http.StatusInternalServerError, "设置用户授权失败: "+err.Error())
		return
	}
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("设置用户授权分组", "userId", uid, "groupIds", req.GroupIDs)
	respondOK(c, nil)
}
