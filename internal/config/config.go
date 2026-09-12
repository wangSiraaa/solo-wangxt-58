// Package config 从环境变量加载服务配置。
package config

import (
	"os"
	"strings"
)

// Identity 是一个办理人员身份（由 Bearer token 映射而来）。
type Identity struct {
	StaffID string
	Role    string // clerk / reviewer / finance / admin
}

type Config struct {
	DatabaseURL   string
	HTTPAddr      string
	RunMigrations bool
	Seed          bool
	Tokens        map[string]Identity
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func Load() Config {
	return Config{
		DatabaseURL:   env("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/niche?sslmode=disable"),
		HTTPAddr:      env("HTTP_ADDR", ":8080"),
		RunMigrations: env("RUN_MIGRATIONS", "1") != "0",
		Seed:          env("SEED", "0") == "1",
		Tokens:        parseTokens(env("STAFF_TOKENS", "")),
	}
}

// parseTokens 解析 "token:staffId:role,token2:staffId2:role2" 格式；
// 未配置时使用一组开发默认值（两个窗口 + 复核 + 财务 + 管理员）。
func parseTokens(s string) map[string]Identity {
	if strings.TrimSpace(s) == "" {
		return map[string]Identity{
			"dev-clerk-1":  {StaffID: "window-1", Role: "clerk"},
			"dev-clerk-2":  {StaffID: "window-2", Role: "clerk"},
			"dev-reviewer": {StaffID: "review-1", Role: "reviewer"},
			"dev-finance":  {StaffID: "finance-1", Role: "finance"},
			"dev-admin":    {StaffID: "admin-1", Role: "admin"},
		}
	}
	m := make(map[string]Identity)
	for _, part := range strings.Split(s, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), ":", 3)
		if len(kv) == 3 && kv[0] != "" {
			m[kv[0]] = Identity{StaffID: kv[1], Role: kv[2]}
		}
	}
	return m
}
