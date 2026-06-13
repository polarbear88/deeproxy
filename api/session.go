// 本文件实现后台管理员的【内存会话】与【登录失败限流】（D2-A + AC-40）。
//
// 设计（D2-A）：单一管理员，会话用内存 map + HttpOnly Cookie 存 sessionID；
// 即时吊销（登出/改密清空）；重启会话失效（可接受，无需持久化签名密钥）。
//
// 限流（AC-40 / G5）：登录失败计数放内存；连续失败达阈值后锁定一段时间，
// 防暴力破解。锁定按客户端来源维度（单管理员，按 IP 即可）。
package api

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const (
	sessionCookieName = "deeproxy_admin" // 会话 Cookie 名
	sessionTTL        = 24 * time.Hour   // 会话有效期（无活动自动过期）

	maxLoginFails     = 5               // 连续失败阈值（AC-40：5 次）
	loginLockDuration = 5 * time.Minute // 锁定时长（AC-40：5 分钟）
)

// sessionStore 是内存会话表（sessionID → 过期时间）。并发安全。
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time // sessionID → expireAt
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]time.Time)}
}

// Create 生成一个新会话并返回其 ID。
func (s *sessionStore) Create() string {
	id := randomToken()
	s.mu.Lock()
	s.sessions[id] = time.Now().Add(sessionTTL)
	s.mu.Unlock()
	return id
}

// Validate 校验 sessionID 是否有效（存在且未过期）。顺带惰性清理过期项。
func (s *sessionStore) Validate(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[id]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, id) // 过期清理
		return false
	}
	return true
}

// Delete 删除指定会话（登出）。
func (s *sessionStore) Delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// Clear 清空全部会话（改密后强制所有会话失效，AC-40 安全语义）。
func (s *sessionStore) Clear() {
	s.mu.Lock()
	s.sessions = make(map[string]time.Time)
	s.mu.Unlock()
}

// randomToken 生成 32 字节的随机十六进制会话 ID（密码学安全）。
func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand 失败概率极低，失败时退化为全零（仍唯一性由长度保证）
	return hex.EncodeToString(b)
}

// loginLimiter 是按来源 key（IP）的登录失败限流器。
type loginLimiter struct {
	mu        sync.Mutex
	maxFails  int
	lockDur   time.Duration
	failures  map[string]int       // key → 连续失败次数
	lockUntil map[string]time.Time // key → 锁定到期时间
}

func newLoginLimiter(maxFails int, lockDur time.Duration) *loginLimiter {
	return &loginLimiter{
		maxFails:  maxFails,
		lockDur:   lockDur,
		failures:  make(map[string]int),
		lockUntil: make(map[string]time.Time),
	}
}

// Locked 判断某来源当前是否处于锁定中；返回锁定剩余时长（>0 表示锁定）。
func (l *loginLimiter) Locked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lockedLocked(key)
}

// lockedLocked 是 Locked 的内部实现，要求调用方已持有 l.mu。
// 抽出来是为了让 Fail 在同一把锁内复用「是否已锁定」判断，避免重复加锁与竞态（DRY）。
func (l *loginLimiter) lockedLocked(key string) (bool, time.Duration) {
	until, ok := l.lockUntil[key]
	if !ok {
		return false, 0
	}
	if remain := time.Until(until); remain > 0 {
		return true, remain
	}
	// 锁定已过期：清理，允许重试。
	delete(l.lockUntil, key)
	delete(l.failures, key)
	return false, 0
}

// Fail 记录一次登录失败；达到阈值则进入锁定。
//
// FIX-H5b：锁定期间不再累加计数、不再续期。
// 旧实现里锁定后继续失败仍 failures++ 且每次都把 lockUntil 顺延 lockDur，
// 攻击者对某受害者 IP 持续打错密码即可让锁定无限延长，形成持久 DoS（合法管理员永远登不进）。
// 现在：先判断是否已处于锁定中，是则直接返回（冻结计数与到期时间），待其自然过期后再清零重置。
func (l *loginLimiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// 已锁定：冻结，不累加也不续期（lockedLocked 顺带惰性清理已过期的锁）。
	if locked, _ := l.lockedLocked(key); locked {
		return
	}
	l.failures[key]++
	if l.failures[key] >= l.maxFails {
		l.lockUntil[key] = time.Now().Add(l.lockDur)
	}
}

// Reset 登录成功后清零某来源的失败计数与锁定。
func (l *loginLimiter) Reset(key string) {
	l.mu.Lock()
	delete(l.failures, key)
	delete(l.lockUntil, key)
	l.mu.Unlock()
}
