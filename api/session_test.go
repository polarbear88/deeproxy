package api

import "testing"

// session_test.go 覆盖 WP-6 T6.2：会话令牌生成（crypto/rand 成功路径、唯一性）。

// TestRandomTokenUniqueAndError 验证 randomToken 成功返回 64 hex 字符且多次调用互不相同。
func TestRandomTokenUniqueAndError(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		tok, err := randomToken()
		if err != nil {
			t.Fatalf("randomToken 不应失败: %v", err)
		}
		if len(tok) != 64 { // 32 字节 → 64 hex
			t.Fatalf("令牌应为 64 hex 字符，得到 %d", len(tok))
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("令牌重复（随机性不足）: %s", tok)
		}
		seen[tok] = struct{}{}
	}
}

// TestSessionCreateValidate 验证 Create 成功签发、Validate 命中、Delete/Clear 生效。
func TestSessionCreateValidate(t *testing.T) {
	s := newSessionStore()
	id, err := s.Create()
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	if !s.Validate(id) {
		t.Fatal("新建会话应有效")
	}
	s.Delete(id)
	if s.Validate(id) {
		t.Fatal("删除后会话应失效")
	}

	id2, _ := s.Create()
	s.Clear()
	if s.Validate(id2) {
		t.Fatal("Clear 后所有会话应失效")
	}
}
