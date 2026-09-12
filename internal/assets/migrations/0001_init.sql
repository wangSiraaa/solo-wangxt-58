-- 0001_init.sql — 格位合同服务初始 schema
-- 关键约束：occupancy_intervals 上的排他约束保证同一格位的有效占用区间互不重叠，
-- 即使两个办理窗口并发提交，数据库层面也只会放行一个。

CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE IF NOT EXISTS niches (
    id                   BIGSERIAL PRIMARY KEY,
    code                 TEXT NOT NULL UNIQUE,          -- 格位编号，如 A-01-001
    zone                 TEXT NOT NULL,                 -- 分区
    level                INT  NOT NULL DEFAULT 1,       -- 层位
    price_per_year_cents BIGINT NOT NULL CHECK (price_per_year_cents >= 0),
    -- available: 可办理；maintenance: 维护中不可办理。
    -- 是否被占用不冗余存储，由 occupancy_intervals 推导，避免状态不一致。
    status               TEXT NOT NULL DEFAULT 'available' CHECK (status IN ('available','maintenance'))
);

CREATE TABLE IF NOT EXISTS contacts (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    id_number  TEXT NOT NULL UNIQUE,   -- 证件号：仅存储与脱敏展示，绝不写入日志
    phone      TEXT NOT NULL,
    relation   TEXT NOT NULL DEFAULT '', -- 与逝者关系
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE SEQUENCE IF NOT EXISTS contract_no_seq;

CREATE TABLE IF NOT EXISTS contracts (
    id                 BIGSERIAL PRIMARY KEY,
    contract_no        TEXT NOT NULL UNIQUE,
    niche_id           BIGINT NOT NULL REFERENCES niches(id),
    deceased_name      TEXT NOT NULL,
    deceased_id_number TEXT NOT NULL,   -- 逝者证件号，展示时按角色脱敏
    status             TEXT NOT NULL DEFAULT 'pending_review'
                       CHECK (status IN ('pending_review','active','ended','cancelled')),
    start_at           TIMESTAMPTZ NOT NULL,
    end_at             TIMESTAMPTZ NOT NULL,
    annual_fee_cents   BIGINT NOT NULL CHECK (annual_fee_cents >= 0),
    review_note        TEXT,
    reviewed_by        TEXT,
    reviewed_at        TIMESTAMPTZ,
    created_by         TEXT NOT NULL,   -- 办理窗口工号
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (end_at > start_at)
);

-- 合同与联系人的关系：须复核确认（verified）后才具备办理权限
CREATE TABLE IF NOT EXISTS contract_contacts (
    id          BIGSERIAL PRIMARY KEY,
    contract_id BIGINT NOT NULL REFERENCES contracts(id) ON DELETE CASCADE,
    contact_id  BIGINT NOT NULL REFERENCES contacts(id),
    auth_level  TEXT NOT NULL CHECK (auth_level IN ('view','handle','full')),
    verified    BOOLEAN NOT NULL DEFAULT false,
    verified_by TEXT,
    verified_at TIMESTAMPTZ,
    UNIQUE (contract_id, contact_id)
);

-- 格位占用区间：迁位/迁出通过结束旧区间 + 新增区间实现，历史可追溯
CREATE TABLE IF NOT EXISTS occupancy_intervals (
    id          BIGSERIAL PRIMARY KEY,
    contract_id BIGINT NOT NULL REFERENCES contracts(id),
    niche_id    BIGINT NOT NULL REFERENCES niches(id),
    period      TSTZRANGE NOT NULL CHECK (NOT isempty(period)),
    kind        TEXT NOT NULL CHECK (kind IN ('initial','relocation')),
    status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','ended')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    EXCLUDE USING gist (niche_id WITH =, period WITH &&) WHERE (status = 'active')
);

CREATE INDEX IF NOT EXISTS occupancy_active_contract_idx
    ON occupancy_intervals (contract_id) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS occupancy_active_niche_idx
    ON occupancy_intervals (niche_id) WHERE status = 'active';

CREATE TABLE IF NOT EXISTS relocations (
    id            BIGSERIAL PRIMARY KEY,
    contract_id   BIGINT NOT NULL REFERENCES contracts(id),
    from_niche_id BIGINT NOT NULL REFERENCES niches(id),
    to_niche_id   BIGINT NOT NULL REFERENCES niches(id),
    contact_id    BIGINT NOT NULL REFERENCES contacts(id),
    -- 欠费时保持 draft；目标格位冲突时置 failed，原占位不受影响
    status        TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','committed','failed','cancelled')),
    fail_reason   TEXT,
    requested_by  TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    committed_at  TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS payments (
    id            BIGSERIAL PRIMARY KEY,
    contract_id   BIGINT NOT NULL REFERENCES contracts(id),
    relocation_id BIGINT REFERENCES relocations(id),
    kind          TEXT NOT NULL CHECK (kind IN ('annual_fee','relocation_fee','other')),
    amount_cents  BIGINT NOT NULL CHECK (amount_cents >= 0),
    -- 只能由本地模拟回执驱动变为 paid，请求体中的任何布尔值都不起作用
    status        TEXT NOT NULL DEFAULT 'unpaid' CHECK (status IN ('unpaid','paid','void')),
    receipt_no    TEXT UNIQUE,
    paid_at       TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS payments_contract_idx ON payments (contract_id);
