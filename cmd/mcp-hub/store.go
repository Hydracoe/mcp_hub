package main

import (
	"github.com/hydra/mcp-hub/internal/config"
	"github.com/hydra/mcp-hub/internal/store"
)

// openStore 打开数据库，后端按配置项 MCPHUB_DB_DRIVER / 连接串形态推断。
//
// add / servers / probe --db 这三个子命令都走它：它们和 serve 读的是
// 同一份配置，所以"serve 连的是哪个库"与"add 写进哪个库"不可能不一致。
//
// dsn 为空时回落到配置项 MCPHUB_DB；SQLite 下再为空则用当前目录的默认库文件。
func openStore(dsn string) (*store.Store, error) {
	if dsn == "" {
		dsn = config.String(config.EnvDB)
	}
	return store.OpenWith(store.Options{
		Driver:  config.String(config.EnvDBDriver),
		DSN:     dsn,
		DataDir: dataDir(),
	})
}

// dbFlagUsage 是 --db 的帮助文案，三个子命令共用一份。
const dbFlagUsage = "数据库：postgres:// 连接串，或 SQLite 文件路径；缺省读配置项 MCPHUB_DB（.env 或进程环境变量）"
