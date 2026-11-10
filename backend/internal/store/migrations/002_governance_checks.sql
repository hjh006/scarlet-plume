CREATE TABLE plume.requirements (
    code text PRIMARY KEY,
    title text NOT NULL,
    citation text NOT NULL,
    source_url text NOT NULL,
    summary text NOT NULL
);

ALTER TABLE plume.assets ADD UNIQUE (workspace_id, id);

CREATE TABLE plume.checks (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    asset_id bigint NOT NULL,
    requirement_code text NOT NULL REFERENCES plume.requirements(code),
    owner_team text NOT NULL CHECK (char_length(owner_team) BETWEEN 1 AND 80),
    note text NOT NULL CHECK (char_length(note) BETWEEN 1 AND 1000),
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'in_review', 'needs_work', 'verified')),
    created_by bigint NOT NULL REFERENCES plume.users(id),
    submitted_by bigint REFERENCES plume.users(id),
    feishu_target_digest text CHECK (feishu_target_digest ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (workspace_id, asset_id) REFERENCES plume.assets(workspace_id, id)
);

CREATE INDEX checks_workspace_page_idx ON plume.checks (workspace_id, id DESC);

CREATE TABLE plume.check_evidence (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    check_id bigint NOT NULL REFERENCES plume.checks(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('observation', 'remediation', 'retest')),
    source_system text NOT NULL CHECK (char_length(source_system) BETWEEN 1 AND 100),
    source_record text NOT NULL CHECK (char_length(source_record) BETWEEN 1 AND 160),
    summary text NOT NULL CHECK (char_length(summary) BETWEEN 1 AND 600),
    observed_at timestamptz NOT NULL,
    created_by bigint NOT NULL REFERENCES plume.users(id),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    feishu_snapshot_digest text,
    feishu_member_present boolean,
    feishu_permission text,
    CHECK (
        (feishu_snapshot_digest IS NULL AND feishu_member_present IS NULL AND feishu_permission IS NULL)
        OR (feishu_snapshot_digest IS NOT NULL AND feishu_snapshot_digest ~ '^[0-9a-f]{64}$'
            AND feishu_member_present IS NOT NULL AND kind IN ('observation', 'retest')
            AND ((feishu_member_present AND feishu_permission IS NOT NULL
                    AND feishu_permission IN ('view', 'edit', 'full_access'))
                OR (NOT feishu_member_present AND feishu_permission IS NULL)))
    ),
    UNIQUE (check_id, feishu_snapshot_digest)
);

CREATE INDEX check_evidence_check_idx ON plume.check_evidence (check_id, id DESC);
CREATE INDEX check_evidence_actor_idx ON plume.check_evidence (check_id, created_by);

CREATE TABLE plume.check_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    check_id bigint NOT NULL REFERENCES plume.checks(id) ON DELETE CASCADE,
    action text NOT NULL CHECK (action IN ('created', 'evidence', 'submitted', 'changes', 'verified')),
    actor_id bigint NOT NULL REFERENCES plume.users(id),
    note text NOT NULL DEFAULT '' CHECK (char_length(note) <= 600),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX check_events_check_idx ON plume.check_events (check_id, id DESC);
CREATE INDEX check_events_check_action_idx ON plume.check_events (check_id, action, id DESC);

-- Port the existing Django reference catalog. Its original source URLs are retained.
INSERT INTO plume.requirements (code, title, citation, source_url, summary) VALUES
    ('G01', '数据安全制度与责任', '《中华人民共和国数据安全法》第27条', 'https://www.cac.gov.cn/2021-06/11/c_1624994566919140.htm', '建立覆盖数据处理全过程的安全管理制度，开展安全培训并落实保护措施。处理重要数据的组织还应明确安全负责人和管理机构。'),
    ('G02', '数据盘点与分类分级', '《网络数据安全管理条例》第5、29条', 'https://www.cac.gov.cn/2024-09/30/c_1729384452307680.htm', '根据数据的重要性和安全事件可能造成的危害开展分类分级。识别和申报重要数据时，核对适用的地区、行业目录与规则。'),
    ('G03', '访问控制、加密与备份', '《网络数据安全管理条例》第9条', 'https://www.cac.gov.cn/2024-09/30/c_1729384452307680.htm', '网络数据处理者在等保基础上，按适用要求落实访问控制、安全认证、加密、备份等保护措施。'),
    ('G04', '网络安全等级保护与日志', '《中华人民共和国网络安全法》第23条', 'https://www.cac.gov.cn/2025-12/29/c_1768735112911946.htm', '网络运营者按等级保护要求实施防护，监测网络运行及安全事件，并按规定留存相关网络日志至少六个月。'),
    ('G05', '网络安全事件研判与报告', '《国家网络安全事件报告管理办法》第4、7、8条', 'https://www.cac.gov.cn/2025-09/15/c_1759583017717009.htm', '发现或获知网络安全事件后开展研判。较大以上事件按主体和行业适用渠道及时报告，处置结束后三十日内按原渠道报送总结。'),
    ('I01', '工信领域重要与核心数据识别', '《工业和信息化领域数据安全管理办法（试行）》第7至12条', 'https://www.miit.gov.cn/gyhxxhb/jgsj/wlaqglj/sjaq/art/2022/art_8aefad495b3940f8a3236eef1d83096c.html', '适用的工信领域数据处理者定期梳理数据，依相关标准识别重要数据和核心数据，并将目录向属地行业监管部门备案。'),
    ('I03', '重要数据年度与变更风险评估', '《网络数据安全风险评估办法》第5、6、15、16条', 'https://www.cac.gov.cn/2026-06/18/c_1783525609815499.htm', '重要数据处理者每年评估，出现可能不利于安全的重大变化时及时补评。年度报告至少保存三年，年度评估完成后二十个工作日内按要求报送。行业另有规定的，从其规定。'),
    ('I05', '工信领域数据风险评估与报送', '《工业和信息化领域数据安全风险评估实施细则（试行）》第6、10条', 'https://www.miit.gov.cn/cms_files/filemanager/1226211233/attach/20244/1e8f671034ea40a796ca155145c86081.pdf', '工信领域重要数据和核心数据处理者至少每年评估一次，规定情形触发补评。评估完成后十个工作日内向属地行业监管部门报送报告。'),
    ('P01', '个人信息处理依据、告知与留存', '《中华人民共和国个人信息保护法》第6、13、17至19条', 'https://www.cac.gov.cn/2021-08/20/c_1631050028355286.htm', '明确处理目的和适用依据，按规定履行告知义务，将处理范围与保存期限控制在实现目的所需范围内。法律、行政法规另有留存规定的，从其规定。'),
    ('P04', '事前个人信息保护影响评估', '《中华人民共和国个人信息保护法》第55、56条', 'https://www.cac.gov.cn/2021-08/20/c_1631050028355286.htm', '处理敏感个人信息、利用个人信息自动化决策、委托处理、向其他处理者提供、公开或出境等适用情形应事前评估。评估报告及处理记录至少保存三年。'),
    ('P05', '个人信息保护合规审计', '《个人信息保护合规审计管理办法》第3、4、12条', 'https://www.cac.gov.cn/2025-02/14/c_1741233507681519.htm', '个人信息处理者按适用规则定期审计。处理超过一千万人个人信息的至少每两年审计一次。处理一百万人以上个人信息的应指定个人信息保护负责人。'),
    ('T01', '向其他处理者提供或委托处理', '《网络数据安全管理条例》第12条', 'https://www.cac.gov.cn/2024-09/30/c_1729384452307680.htm', '向其他网络数据处理者提供或委托处理个人信息、重要数据时，约定处理范围与安全义务，监督接收方履约，并将相关处理情况记录至少保存三年。'),
    ('T03', '数据出境识别与合规路径', '《促进和规范数据跨境流动规定》第2至10条', 'https://www.cac.gov.cn/2024-03/22/c_1712776611775634.htm', '按实际数据流向、关基身份、重要数据情况、个人信息规模与敏感程度及适用例外，判断出境评估、标准合同或认证路径及相关义务。'),
    ('X01', '企业内部生成式人工智能应用', '《生成式人工智能服务管理暂行办法》第2条', 'https://www.cac.gov.cn/2023-07/13/c_1690898327029107.htm', '暂行办法适用于向境内公众提供生成式人工智能服务。企业仅在内部研发或使用时，按实际数据流向与处理角色核查适用的数据安全和个人信息保护义务。');
