# 格位合同服务（寄存机构 · 仅 API）

面向办理窗口的格位（寄存位）合同服务：查询空位、核实联系人、登记迁入迁出、
内部迁位、缴费核销。后端 Go + Gin + PostgreSQL，**不建设前端**，全部能力经
HTTP API 提供（见 `openapi.yaml`）。

## 办理规则（示例规则）

1. **复核生效**：合同登记后为 `pending_review`，复核岗 `review` 通过才生效，
   同一事务内建立初始占位区间并生成首年账单；复核未完成不得迁出、迁位。
2. **占位不重叠**：同一格位的有效占用区间（`occupancy_intervals`，`tstzrange`）
   由数据库排他约束保证互不重叠——两个窗口并发复核/并发迁位，只有一个能提交。
3. **缴费**：账单开立后只能由本地模拟回执（`POST /payments/{id}/mock-receipt`，
   财务角色）置为已付；请求体携带 `paid`/`status`/`receipt_no`/`paid_at`
   任意字段一律 400。回执幂等：并发/重复回执返回同一回执号，账单只支付一次。
4. **费用清偿与联系人授权**：迁出、迁位提交前，合同名下不得有未付账单；
   经办联系人须已核实（`verified`）且授权级别为 `handle`/`full`。
5. **迁位**：先建草案（同时开立迁位手续费 200.00 元账单），再提交。
   提交在单事务内结束原占位、建立新占位、更新合同格位：
   - 欠费 → 409 `outstanding_fees`，草案保留为 `draft`，不产生半完成迁出；
   - 目标格位冲突 → 事务回滚，草案置 `failed`，原合同与原占位仍然有效。
6. **敏感信息**：证件号/手机号按角色脱敏（复核岗、管理员可见完整号码用于核对）；
   访问日志只记录方法、路径、状态码、工号，且对证件号样式串强制打码。

## 角色

| 角色 | 能力 |
|---|---|
| `clerk`（窗口） | 空位查询、登记合同/联系人、开账单、迁出、迁位 |
| `reviewer`（复核） | 查看、核实联系人、复核合同 |
| `finance`（财务） | 查看账单、模拟回执核销 |
| `admin` | 全部 |

认证：`Authorization: Bearer <token>`。token 映射由环境变量
`STAFF_TOKENS="token:staffId:role,..."` 配置；未配置时使用开发默认值：
`dev-clerk-1` / `dev-clerk-2`（两个窗口）、`dev-reviewer`、`dev-finance`、`dev-admin`。

## 运行

```bash
# 1. 启动 PostgreSQL（任选其一）
docker compose up -d db          # 或自备实例，导出 DATABASE_URL

# 2. 启动服务（自动迁移；SEED=1 时装入种子数据）
SEED=1 go run ./cmd/server
```

配置项：`DATABASE_URL`、`HTTP_ADDR`（默认 `:8080`）、`RUN_MIGRATIONS`（默认开）、
`SEED`、`STAFF_TOKENS`。

## 测试

```bash
go test ./... -count=1          # 单元 + 端到端并发测试（自动拉起内嵌 PostgreSQL）
go test ./test/ -race -count=3  # 并发场景压测 + 竞态检测
```

并发测试覆盖（`test/concurrency_test.go`，使用 embedded-postgres 真实数据库）：

- **两个窗口争用同一格位**：并发复核通过两份重叠合同 → 恰一个 200、一个 409，
  格位上只有一条有效占位；
- **重复回执**：8 路并发模拟回执 → 全部幂等成功，回执号一致，账单只支付一次；
- **迁位事务失败**：目标格位被占用 / 两合同并发迁往同一空位 → 失败方草案
  `failed`，其原合同与原占位仍然有效；
- **欠费迁位**：409 且草案保留 `draft`，原占位不动；缴清后同一草案可提交成功；
- 另有已付款布尔值拒绝、复核前置、迁出规则、角色限制、脱敏与日志打码、
  种子数据幂等性等用例。

## 目录

```
cmd/server            服务入口
internal/config       环境配置与角色 token
internal/db           连接池与 SQL 文件执行
internal/service      业务规则（合同/缴费/迁位，全部走事务）
internal/httpapi      Gin 路由、鉴权、脱敏、访问日志
internal/assets       内嵌迁移与种子 SQL（migrations/、seed/）
openapi.yaml          OpenAPI 3.0 规格
test/                 端到端并发测试
```

> 仓库根目录的 `migrations/`、`seed/` 是指向 `internal/assets/` 的软链，便于查阅。
