-- seed.sql — 演示用种子数据（可重复执行，全部幂等）
-- 演示账号见 README：dev-clerk-1 / dev-clerk-2（两个窗口）、dev-reviewer、dev-finance、dev-admin

INSERT INTO niches (id, code, zone, level, price_per_year_cents, status) VALUES
    ( 1, 'A-01-001', 'A', 1, 360000, 'available'),
    ( 2, 'A-01-002', 'A', 1, 360000, 'available'),
    ( 3, 'A-01-003', 'A', 1, 360000, 'available'),
    ( 4, 'A-02-001', 'A', 2, 300000, 'available'),
    ( 5, 'A-02-002', 'A', 2, 300000, 'available'),
    ( 6, 'B-01-001', 'B', 1, 480000, 'available'),
    ( 7, 'B-01-002', 'B', 1, 480000, 'available'),
    ( 8, 'B-02-001', 'B', 2, 420000, 'available'),
    ( 9, 'B-02-002', 'B', 2, 420000, 'available'),
    (10, 'C-01-001', 'C', 1, 240000, 'maintenance')
ON CONFLICT (id) DO NOTHING;

INSERT INTO contacts (id, name, id_number, phone, relation) VALUES
    (1, '张四', '110101196002021234', '13800001234', '子'),
    (2, '王五', '110101196503054321', '13900005678', '配偶')
ON CONFLICT (id) DO NOTHING;

-- 已生效合同：占用 A-01-001，首年费用已缴清
INSERT INTO contracts (id, contract_no, niche_id, deceased_name, deceased_id_number,
                       status, start_at, end_at, annual_fee_cents,
                       reviewed_by, reviewed_at, created_by) VALUES
    (100, 'C-2026-000100', 1, '张三', '110101194901011234',
     'active', '2026-01-01T00:00:00Z', '2036-01-01T00:00:00Z', 360000,
     'review-1', '2026-01-01T08:00:00Z', 'window-1')
ON CONFLICT (id) DO NOTHING;

INSERT INTO contract_contacts (id, contract_id, contact_id, auth_level,
                               verified, verified_by, verified_at) VALUES
    (100, 100, 1, 'full', true, 'review-1', '2026-01-01T07:00:00Z')
ON CONFLICT (id) DO NOTHING;

INSERT INTO occupancy_intervals (id, contract_id, niche_id, period, kind, status) VALUES
    (100, 100, 1, tstzrange('2026-01-01T00:00:00Z', '2036-01-01T00:00:00Z', '[)'), 'initial', 'active')
ON CONFLICT (id) DO NOTHING;

INSERT INTO payments (id, contract_id, kind, amount_cents, status, receipt_no, paid_at) VALUES
    (100, 100, 'annual_fee', 360000, 'paid', 'RCPT-SEED-000100', '2026-01-02T00:00:00Z')
ON CONFLICT (id) DO NOTHING;

-- 待复核合同：已登记、已关联联系人（未核实），复核通过前不产生任何占位
INSERT INTO contracts (id, contract_no, niche_id, deceased_name, deceased_id_number,
                       status, start_at, end_at, annual_fee_cents, created_by) VALUES
    (101, 'C-2026-000101', 5, '李四', '110101195203032345',
     'pending_review', '2026-09-01T00:00:00Z', '2036-09-01T00:00:00Z', 300000, 'window-2')
ON CONFLICT (id) DO NOTHING;

INSERT INTO contract_contacts (id, contract_id, contact_id, auth_level) VALUES
    (101, 101, 2, 'handle')
ON CONFLICT (id) DO NOTHING;

-- 序列推进到种子数据之后，避免与显式 id 冲突
SELECT setval('niches_id_seq', 1000);
SELECT setval('contacts_id_seq', 1000);
SELECT setval('contracts_id_seq', 1000);
SELECT setval('contract_contacts_id_seq', 1000);
SELECT setval('occupancy_intervals_id_seq', 1000);
SELECT setval('payments_id_seq', 1000);
SELECT setval('relocations_id_seq', 1000);
