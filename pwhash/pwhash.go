// Package pwhash 是密码哈希与校验的零依赖（仅 x/crypto/bcrypt）叶子包。
//
// 存在意义（AC-43 静态依赖约束）：bcrypt 哈希/校验被【转发热路径】的 auth 包（鉴权验密码）
// 与【存储层】store 包（CRUD 写入哈希）共同使用。若把它放在 store，auth 就会因 import store
// 而把 database/sql + SQLite 驱动拉进转发链。抽到本叶子包后，auth 与 store 都引本包，
// auth 不再 import store，转发链脱离存储依赖。
//
// 采用 bcrypt（行业标准、自带盐、可调 cost），cost 用库默认（约 10，G5）。
package pwhash

import "golang.org/x/crypto/bcrypt"

// Hash 用 bcrypt 默认 cost 生成密码哈希。
func Hash(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// Verify 校验明文密码与 bcrypt 哈希是否匹配。
// 返回 true 表示匹配；其余情况（不匹配/哈希非法）返回 false。
func Verify(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
