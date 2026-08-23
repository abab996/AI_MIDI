package engine

import "sync"

// 全局守护器引用：HTTP 处理器经 Get() 访问，main 启动时 SetGlobal 注入。
// 采用与 config 包一致的包级状态风格，避免改动 Router 构造函数签名。
var (
	globalMu sync.RWMutex
	global   *Supervisor
)

// SetGlobal 注册全局守护器
func SetGlobal(s *Supervisor) {
	globalMu.Lock()
	global = s
	globalMu.Unlock()
}

// Get 获取全局守护器（未注册时返回 nil）
func Get() *Supervisor {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return global
}
