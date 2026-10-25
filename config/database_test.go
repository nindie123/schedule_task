package config

import (
	"net"
	"testing"
	"time"
)

// TestInitDB_Success 验证启动成功路径：MySQL 可用时 InitDB 返回 nil 并设置 DB。
func TestInitDB_Success(t *testing.T) {
	// 本机没有 MySQL 时跳过，避免把环境问题误报成代码问题。
	conn, err := net.DialTimeout("tcp", "127.0.0.1:3306", time.Second)
	if err != nil {
		t.Skipf("本机 MySQL 未启动，跳过成功路径验证: %v", err)
	}
	_ = conn.Close()

	old := DB
	t.Cleanup(func() {
		if DB != nil && DB != old {
			_ = DB.Close()
		}
		DB = old
	})

	if err := InitDB(); err != nil {
		t.Fatalf("InitDB 应成功，实际返回: %v", err)
	}
	if DB == nil {
		t.Fatal("InitDB 返回 nil，但 DB 未设置")
	}
}

// TestInitDB_Failure 验证启动失败路径：连不上 MySQL 时返回错误，且不替换全局 DB。
func TestInitDB_Failure(t *testing.T) {
	old := DB
	t.Cleanup(func() { DB = old })

	// 端口 1 上没有 MySQL，连接会被立即拒绝。
	err := initDB("zhllj:123456@tcp(127.0.0.1:1)/task_scheduler?timeout=1s")
	if err == nil {
		t.Fatal("连接失败时 initDB 应返回错误，实际为 nil")
	}
	if DB != old {
		t.Error("初始化失败时不应替换全局 DB")
	}
}
