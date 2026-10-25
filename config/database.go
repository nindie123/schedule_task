package config

import (
	"context"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

const defaultDSN = "zhllj:123456@tcp(127.0.0.1:3306)/task_scheduler?charset=utf8mb4&parseTime=True&loc=Local"

var DB *sqlx.DB

func InitDB() error {
	return initDB(defaultDSN)
}

// initDB 用给定 DSN 初始化连接。
// 抽出 dsn 形参是为了测试连接失败路径；生产入口 InitDB 始终使用 defaultDSN。
func initDB(dsn string) error {
	db, err := sqlx.Open("mysql", dsn)
	if err != nil {
		// sqlx.Open 只打开驱动并校验 DSN，不会真正建立连接；
		// 真正的连接检查在下面的 PingContext。
		fmt.Printf("open database failed: %v\n", err)
		return err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return fmt.Errorf("MySQL ping failed: %w", err)
	}
	DB = db
	fmt.Println("MySQL connect success")

	return nil
}
