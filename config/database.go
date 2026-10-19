package config

import (
	"fmt"

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
	DB = db
	fmt.Println("MySQL connect success")
	return nil
}
