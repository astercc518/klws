-- 0024: warmup — 养号子系统(worker+admin 专用)。照 0023 平台表范式:不启 RLS,
-- 仅 SystemPool(BYPASSRLS 的 app_system)访问,REVOKE app_tenant/app_customer。
-- tenant_id 仅作展示/筛选列。养号发送计 warmup_sent_today,与业务 sent_today 分离。

CREATE TABLE IF NOT EXISTS warmup_profiles (
    account_jid          TEXT PRIMARY KEY,
    tenant_id            BIGINT NOT NULL,
    lane                 TEXT NOT NULL DEFAULT 'STANDARD',   -- FAST / STANDARD
    stage                TEXT NOT NULL DEFAULT 'NEW',        -- NEW / WARMING / MATURE
    warmup_messages_sent INT NOT NULL DEFAULT 0,
    replies_received     INT NOT NULL DEFAULT 0,
    online_since         TIMESTAMPTZ,
    matured_at           TIMESTAMPTZ,
    warmup_sent_today    INT NOT NULL DEFAULT 0,
    warmup_sent_date     DATE,
    paused               BOOLEAN NOT NULL DEFAULT false,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_warmup_profiles_stage ON warmup_profiles (stage);
CREATE INDEX IF NOT EXISTS ix_warmup_profiles_tenant_stage ON warmup_profiles (tenant_id, stage);

CREATE TABLE IF NOT EXISTS warmup_policies (
    lane                 TEXT PRIMARY KEY,
    min_warmup_messages  INT NOT NULL,
    min_replies          INT NOT NULL,
    min_online_hours     INT NOT NULL,
    warming_cap          INT NOT NULL,
    mature_base_cap      INT NOT NULL,
    mature_max_cap       INT NOT NULL,
    mature_ramp_step     INT NOT NULL,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 初始策略(照 TS LANE_POLICIES)。ON CONFLICT DO NOTHING 保持幂等 + 不覆盖后台已改值。
INSERT INTO warmup_policies (lane, min_warmup_messages, min_replies, min_online_hours,
                             warming_cap, mature_base_cap, mature_max_cap, mature_ramp_step)
VALUES ('FAST', 2, 0, 2, 5, 15, 20, 5),
       ('STANDARD', 20, 5, 36, 8, 20, 40, 5)
ON CONFLICT (lane) DO NOTHING;

CREATE TABLE IF NOT EXISTS warmup_scripts (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    lang       TEXT NOT NULL,           -- zh / pt / en …,与配对号国家/代理国匹配
    turns      JSONB NOT NULL,          -- [{"from":"A","text":"oi {name}, tudo bem?"},{"from":"B","text":"tudo 👍"}]
    enabled    BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_warmup_scripts_lang ON warmup_scripts (lang) WHERE enabled;

-- 初始植入(每语种至少一套多轮脚本;pt/zh/en)。幂等:仅当该 lang 无脚本时插入。
INSERT INTO warmup_scripts (lang, turns)
SELECT v.lang, v.turns::jsonb FROM (VALUES
  ('pt', '[{"from":"A","text":"oi {name}, tudo bem?"},{"from":"B","text":"tudo sim {emoji} e vc?"},{"from":"A","text":"tudo tranquilo por aqui {emoji}"},{"from":"B","text":"que bom! bom dia"}]'),
  ('en', '[{"from":"A","text":"hey {name}, how are you?"},{"from":"B","text":"good {emoji} you?"},{"from":"A","text":"all good here {emoji}"},{"from":"B","text":"nice, take care"}]'),
  ('zh', '[{"from":"A","text":"在吗{name}?"},{"from":"B","text":"在的{emoji}"},{"from":"A","text":"最近咋样"},{"from":"B","text":"挺好的 你呢{emoji}"}]')
) AS v(lang, turns)
WHERE NOT EXISTS (SELECT 1 FROM warmup_scripts w WHERE w.lang = v.lang);

REVOKE ALL ON warmup_profiles, warmup_policies, warmup_scripts FROM app_tenant;
DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_customer') THEN
    EXECUTE 'REVOKE ALL ON warmup_profiles, warmup_policies, warmup_scripts FROM app_customer';
  END IF;
END $$;
