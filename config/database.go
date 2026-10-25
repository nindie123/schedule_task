package config

import (
	"context"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

var DB *sqlx.DB

func InitDB() error {
	db, err := sqlx.Open("mysql", "zhllj:123456@tcp(127.0.0.1:3306)/task_scheduler?charset=utf8mb4&parseTime=True&loc=Local")
	if err != nil {
		fmt.Println("database connect is error", err)
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
