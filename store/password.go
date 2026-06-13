package store

import "deeproxy/pwhash"

// password.go 保留 store.HashPassword / VerifyPassword 历史 API（供 api/cmd 写入与展示用），
// 实现委托给零依赖叶子包 pwhash（AC-43：让 auth 等转发链包改引 pwhash 而非 store）。
// 这样既不破坏既有调用方，又把 bcrypt 的唯一实现集中在 pwhash（DRY）。

// HashPassword 生成密码 bcrypt 哈希（委托 pwhash.Hash）。
func HashPassword(plain string) (string, error) { return pwhash.Hash(plain) }

// VerifyPassword 校验明文与 bcrypt 哈希（委托 pwhash.Verify）。
func VerifyPassword(hash, plain string) bool { return pwhash.Verify(hash, plain) }
