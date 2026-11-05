package main

import (
	"github.com/hydra/mcp-hub/internal/config"
	"github.com/hydra/mcp-hub/internal/store"
)

// openStore 打开数据库，后端按配置项 MCPHUB_DB_DRIVER / 连接串形态推断。
//
// add / servers / probe --db 都走它，与 serve 读同一份配置，保证读写的库一致。
// dsn 为空时回落到 MCPHUB_DB；SQLite 下再为空则用当前目录的默认库文件。
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
