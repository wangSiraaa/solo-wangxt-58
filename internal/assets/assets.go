// Package assets 内嵌迁移与种子 SQL，保证单二进制即可初始化数据库。
package assets

import "embed"

//go:embed migrations
var Migrations embed.FS

//go:embed seed
var Seed embed.FS
