// Package db 负责连接池与 SQL 文件（迁移/种子）的执行。
package db

import (
	"context"
	"embed"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

// ApplySQLDir 按文件名顺序执行 embed.FS 中某目录下的全部 .sql 文件。
// SQL 均写成幂等形式（IF NOT EXISTS / ON CONFLICT DO NOTHING），可重复执行。
func ApplySQLDir(ctx context.Context, pool *pgxpool.Pool, fsys embed.FS, dir string) error {
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read dir %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		b, err := fsys.ReadFile(dir + "/" + name)
		if err != nil {
			return fmt.Errorf("read %s/%s: %w", dir, name, err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			return fmt.Errorf("apply %s/%s: %w", dir, name, err)
		}
	}
	return nil
}
