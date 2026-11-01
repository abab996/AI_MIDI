//go:build windows

package config

// settings.json 中敏感值（顶层 api_key 与 providers[].api_key）的
// Windows DPAPI 加密：密文以 "dpapi:"+base64 形式落盘，作用域为当前
// 用户——拿到文件的其他账户/其他机器解不开。明文旧值读取时兼容，
// 下次 SaveSettings 落盘自动迁移为密文。

import (
	"encoding/base64"
	"encoding/binary"
	"log/slog"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SecretPrefix 加密值前缀（导出给掩码/校验逻辑判断用）
const SecretPrefix = "dpapi:"

// ProtectSecret 加密敏感值；空值或已是密文的值原样返回（幂等，防二次加密）。
func ProtectSecret(plain string) (string, error) {
	if plain == "" || strings.HasPrefix(plain, SecretPrefix) {
		return plain, nil
	}
	enc := utf16.Encode([]rune(plain))
	buf := make([]byte, len(enc)*2)
	for i, v := range enc {
		binary.LittleEndian.PutUint16(buf[i*2:], v)
	}
	var in, out windows.DataBlob
	in.Size = uint32(len(buf))
	if len(buf) > 0 {
		in.Data = &buf[0]
	}
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return "", err
	}
	data := unsafe.Slice(out.Data, out.Size)
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) }()
	return SecretPrefix + base64.StdEncoding.EncodeToString(data), nil
}

// UnprotectSecret 解密 "dpapi:" 前缀的值；明文旧值原样返回（兼容迁移）。
// 解密失败（文件被复制到其他机器/用户等）返回空串并记日志——比崩溃或
// 把密文当密钥用都安全。
func UnprotectSecret(stored string) string {
	if !strings.HasPrefix(stored, SecretPrefix) {
		return stored
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, SecretPrefix))
	if err != nil || len(raw) == 0 {
		slog.Warn("api_key 密文解码失败，按空值处理")
		return ""
	}
	var in, out windows.DataBlob
	in.Size = uint32(len(raw))
	in.Data = &raw[0]
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		slog.Warn("api_key DPAPI 解密失败（文件是否被复制到其他机器/用户？），按空值处理", "err", err)
		return ""
	}
	data := unsafe.Slice(out.Data, out.Size)
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) }()
	u := make([]uint16, len(data)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(data[i*2:])
	}
	return string(utf16.Decode(u))
}
