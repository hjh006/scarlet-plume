export interface ScenarioTask {
  id: string
  canonicalPath?: string
  label: string
  summary: string
  requirements: string[]
  deliverables: string[]
  entries: { label: string; page: string; description?: string }[]
  gap?: string
}

export interface ScenarioViewEntry {
  label: string
  page: string
  description: string
}

export interface ScenarioDefinition {
  id: string
  label: string
  icon: string
  description: string
  trigger: string
  output: string
  views: ScenarioViewEntry[]
  tasks: ScenarioTask[]
  sources: { label: string; url: string }[]
}

const dataRegulation = {
  label: '网络数据安全管理条例（2024年公布）',
  url: 'https://www.cac.gov.cn/2024-09/30/c_1729384452307680.htm',
}
const personalInformationLaw = {
  label: '个人信息保护法（2021年公布）',
  url: 'https://www.cac.gov.cn/2021-08/20/c_1631050028355286.htm',
}
const personalAuditRegulation = {
  label: '个人信息保护合规审计管理办法（2025年公布）',
  url: 'https://www.cac.gov.cn/2025-02/14/c_1741233507681519.htm',
}
const dataRiskAssessmentRegulation = {
  label: '网络数据安全风险评估办法（2026年公布）',
  url: 'https://policy.mofcom.gov.cn/claw/clawContent.shtml?id=106660',
}

const personalAuditTask: ScenarioTask = {
  id: 'audit',
  canonicalPath: 'scenarios/personal/audit',
  label: '个人信息合规检查',
  summary: '核查个人信息处理范围与保护证据，记录发现、整改建议及待核实的审计条件。',
  requirements: [
    '核实审计触发依据、组织方式、时间范围及采用要求。',
    '核查实际个人信息处理活动与履责证据，按适用要求安排内部机构或专业机构。',
    '记录审计发现、整改措施与复核结果，按适用情形准备留存或报送材料。',
  ],
  deliverables: ['审计范围与组织安排', '审计底稿、发现及整改证据', '经确认的审计报告及适用报送材料'],
  entries: [
    { label: '处理活动', page: 'activities', description: '核对本次审计涉及的业务活动与数据范围。' },
    { label: '检查模板', page: 'templates', description: '核对或配置本次实际采用的检查要求。' },
    { label: '检查与证据', page: 'checks', description: '登记单项检查、审计发现及整改复核依据。' },
  ],
  gap: '个人信息保护合规审计与个人信息保护影响评估是不同任务。现有检查与证据可复用，完整审计组织、正式审计报告及报送仍需另行办理。',
}

export const scenarios: ScenarioDefinition[] = [
  {
    id: 'governance',
    label: '数据治理与防护',
    icon: 'assets',
    description: '通过授权采集检查资产分类、访问权限与数据流向，保留证据并追踪整改复测。',
    trigger: '新接入系统、开展数据盘点、保护要求变化或定期复查。',
    output: '检查结果、技术证据、整改建议与复测记录',
    views: [
      { label: '数据资产', page: 'assets', description: '查看资产台账与来源，选择本次治理涉及的实际资产。' },
      { label: '业务资产目录', page: 'asset-catalog', description: '查看业务归属、责任信息和来源记录，核对本次目录范围。' },
      { label: '分类分级', page: 'classification', description: '查看分类结果、采用标准和判定依据，选择需要核实的资产。' },
      { label: '浏览器字段标注', page: 'capabilities?tab=browser', description: '核对授权当前页的标签、人工资产关联及真实契约同名候选。' },
      { label: '文件内容结果', page: 'classification?kind=file', description: '按真实文件资产筛选识别结果、候选规则和来源覆盖。' },
      { label: '数据流转', page: 'data-flows', description: '按实际服务、来源或引用查看已观测关系与历史快照。' },
    ],
    tasks: [
      {
        id: 'inventory',
        label: '资产发现与范围检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '明确本次系统、业务和数据范围，记录尚未接入或尚未确认的部分。',
          '将来源观察与业务负责人确认分别记录，核对资产身份与责任团队。',
          '需要接入来源时，由有权限的技术人员完成配置，再核对实际采集覆盖。',
        ],
        deliverables: ['本次资产与业务范围', '来源、用途、责任团队及待核实清单'],
        entries: [
          { label: '数据资产', page: 'assets', description: '查看已登记资产及来源，必要时补充登记。' },
          { label: '业务资产目录', page: 'asset-catalog', description: '核对业务归属、责任团队、来源与人工确认。' },
          { label: '多源自动发现', page: 'discovery', description: '由技术人员接入来源并核对采集范围。' },
          { label: '集成与导入', page: 'integrations', description: '查看接入能力，校验并导入已有采集清单。' },
        ],
        gap: '连接成功与采集覆盖分别核对；未接入或未观察到的数据仍需保留在盘点缺口中。',
      },
      {
        id: 'classification',
        label: '分类分级一致性检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '选择与业务和行业相符的标准，保留采用版本与条款依据。',
          '分别确认个人信息属性、内部业务保护等级和法定数据类别。',
          '核对识别样本、观察范围及缺失条件，由有权限的人员确认并留痕。',
        ],
        deliverables: ['采用标准及判定依据', '分类分级确认记录与待补事实'],
        entries: [
          { label: '分类分级', page: 'classification', description: '查看候选、采用依据、确认值及修订历史。' },
          { label: '分类分级标准库', page: 'classification/standards', description: '核对标准版本、规则覆盖与公开来源。' },
        ],
        gap: '分类标签和有限样本不能直接代替重要数据、核心数据的识别或认定依据。',
      },
      {
        id: 'access',
        label: '数据访问权限检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '明确要核对的资产、账号或角色、批准用途与访问路径。',
          '核对来源实际可见的授权与访问记录，区分直接授权、继承及未覆盖路径。',
          '存在差异时登记整改，取得同一范围的新证据后提交独立复核。',
        ],
        deliverables: ['批准范围与权限观察记录', '差异、整改与来源复测依据'],
        entries: [
          { label: '资产与权限依据', page: 'assets', description: '选择已采集的 PostgreSQL 资产查看字段与权限。' },
          { label: '自动采集', page: 'automation', description: '查看采集任务、运行结果及覆盖限制。' },
          { label: '检查与证据', page: 'checks', description: '记录权限差异、整改和独立复核。' },
        ],
        gap: '来源可见授权不等于全部有效访问权限；元数据目录中的所有者也不等于源数据库授权。',
      },
      {
        id: 'lifecycle',
        label: '生命周期配置检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '对照业务登记与已观测关系，核实流向、处理地域和接收方。',
          '核对接口变更、保存期限与退出安排，记录缺失来源和未证实关系。',
          '业务或来源变化时，重新检查受影响的范围和既有评估。',
        ],
        deliverables: ['已观测流向与业务说明', '保存安排、变更及待核实关系'],
        entries: [
          { label: '数据流转', page: 'data-flows', description: '查看来源依据、历史快照与请求调查。' },
          { label: '接口生命周期', page: 'api-lifecycle', description: '核对契约、观测和接口变更。' },
          { label: '处理活动', page: 'activities', description: '核对用途、接收方、地域及保留安排。' },
        ],
        gap: '图谱仅覆盖已接入来源；当前平台不自动完成数据保存到期删除。',
      },
    ],
    sources: [dataRegulation],
  },
  {
    id: 'domestic',
    label: '国内合规与迎检',
    icon: 'checks',
    description: '按适用标准核查分类、权限与安全配置，输出缺口、整改建议和检查报告。',
    trigger: '监管通知、适用的定期评估或审计、组织自查。',
    output: '检查结果、技术证据、整改建议与复测记录',
    views: [
      { label: '监管检查', page: 'checks', description: '查看检查、证据和整改状态，选择本次适用的实际检查范围。' },
      { label: '合规评估', page: 'assessments', description: '查看已有专项评估，核对本次适用类型与关联处理活动。' },
      { label: '合规报告', page: 'reports', description: '选择实际工作区或处理活动范围，查看报告与采用依据。' },
      { label: '审计中心', page: 'audit', description: '按实际来源和时间核对请求审计，按需查看平台操作记录。' },
      { label: '运营报告证据', page: 'capabilities?tab=reports', description: '按明确来源与时间生成运营技术报告，核对证据及保留窗口缺口。' },
    ],
    tasks: [
      {
        id: 'applicability',
        label: '监管要求适用性核对',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '确认组织、行业、地域、处理者身份及本次任务的触发依据。',
          '收到通知时记录原始通知、要求范围及真实截止时点。',
          '逐项核对采用条款、版本、适用条件和负责核实的人员。',
        ],
        deliverables: ['任务触发依据与适用范围', '采用要求及待核实条件清单'],
        entries: [
          { label: '检查模板', page: 'templates', description: '核对已登记条款、适用条件与取证要求。' },
          { label: '处理活动', page: 'activities', description: '核实本次涉及的业务与处理范围。' },
          { label: '检查台账', page: 'checks', description: '查看已有检查，按实际范围补充所需项目。' },
        ],
        gap: '平台不根据行业名称自动给出完整适用结论；未覆盖的行业专项要求需由专业人员补充。',
      },
      {
        id: 'self-check',
        label: '数据安全技术自查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '固定本次对象、检查标准、责任团队及采用的要求版本。',
          '登记实际证据来源、时点和缺口，不用材料齐全代替措施有效。',
          '完成整改后取得新的同范围复测证据，再由独立人员核验。',
        ],
        deliverables: ['自查记录与证据引用', '整改、复测与独立复核记录'],
        entries: [
          { label: '检查与整改', page: 'checks', description: '查看或建立实际范围的单项检查。' },
          { label: '检查模板', page: 'templates', description: '核对采用条件、证据与验收标准。' },
          { label: '合规报告', page: 'reports', description: '查看已有检查范围内的内部报告。' },
        ],
      },
      {
        id: 'important-data',
        label: '重要数据保护检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '核实重要数据识别或认定依据、处理者身份及本次处理范围。',
          '按适用要求组织风险识别、分析与评价，核查保护措施有效性。',
          '编制实际风险评估报告，按适用要求办理报送并保留真实回执。',
          '数据范围、处理活动或风险变化时，核对后续评估与跟踪要求。',
        ],
        deliverables: ['重要数据识别及范围底稿', '风险评估、措施验证及整改材料', '适用报送材料与真实回执'],
        entries: [
          { label: '分类依据', page: 'classification', description: '核对已登记法定类别、采用依据与缺失条件。' },
          { label: '检查与证据', page: 'checks', description: '留存各项核查、措施验证与整改依据。' },
          { label: '现有内部报告', page: 'reports', description: '汇集已创建检查的状态和证据，供编制正式材料时核对。' },
        ],
        gap: '重要数据风险评估不同于个人信息影响评估和内部数据出境评估。现有检查报告不替代正式风险评估报告，平台也不代为报送或生成主管部门回执。',
      },
      { ...personalAuditTask, id: 'personal-audit' },
      {
        id: 'inspection',
        label: '迎检证据核对',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '逐项对照通知或已确认要求，核实材料覆盖与责任人。',
          '区分当前预览、历史归档、来源回执及独立复核结论。',
          '补充尚未建立检查的适用项目，保留未解决问题及外部提交凭证。',
        ],
        deliverables: ['检查要求与材料对照清单', '实际采用的报告、证据与未完成说明'],
        entries: [
          { label: '检查台账', page: 'checks', description: '核对检查、证据与当前复核状态。' },
          { label: '合规报告', page: 'reports', description: '查看或导出实际范围的内部报告。' },
          { label: '历史报告归档', page: 'reports/archives', description: '核对仍保留的历史快照与采用时点。' },
        ],
        gap: '现有报告仅覆盖已创建的检查，不表示通知要求已全部覆盖，也不表示材料已向监管部门提交。',
      },
    ],
    sources: [dataRegulation, personalAuditRegulation, dataRiskAssessmentRegulation],
  },
  {
    id: 'personal',
    label: '个人信息保护',
    icon: 'user',
    description: '核查个人信息识别、处理范围、保存与保护配置，按证据追踪问题整改。',
    trigger: '新业务上线、敏感信息处理、自动化决策或处理方式变更。',
    output: '检查结果、技术证据、整改建议与复测记录',
    views: [
      { label: '处理活动', page: 'activities', description: '查看已有活动，选择本次涉及个人信息的实际业务范围。' },
      { label: '分类分级', page: 'classification', description: '核对所选资产的个人信息属性、识别线索与确认依据。' },
      { label: '合规评估', page: 'assessments', description: '选择个人信息影响评估或其他实际适用的评估记录。' },
      { label: '监管检查', page: 'checks', description: '查看与本次处理范围相关的检查、证据与整改复核。' },
    ],
    tasks: [
      {
        id: 'inventory',
        label: '个人信息范围检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '核对个人信息类别、涉及人员、实际用途与必要性。',
          '核实处理依据、接收方、地域、保存安排和保护措施。',
          '记录来源覆盖与未确认事实，按条件关联共享、出境或 AI 使用要求。',
        ],
        deliverables: ['个人信息处理活动与范围', '处理依据、保护措施及待核实事实'],
        entries: [
          { label: '分类分级', page: 'classification', description: '核对个人信息候选及人工确认依据。' },
          { label: '处理活动', page: 'activities', description: '登记或核对用途、接收方、地域和保留安排。' },
          { label: '数据流转', page: 'data-flows', description: '对照实际已观测关系与来源范围。' },
        ],
      },
      {
        id: 'pia',
        label: '个人信息影响与保护检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '核实本次处理是否触发影响评估，保留判断事实与依据。',
          '核对处理目的、必要性、数据范围、权益影响及保护措施有效性。',
          '引用同一活动范围的证据，补齐缺口后提交独立人员复核。',
        ],
        deliverables: ['影响评估适用性依据', '采用范围、答复、措施证据与复核记录'],
        entries: [
          { label: '合规评估', page: 'assessments', description: '核对处理活动后，选择个人信息影响评估的已发布模板。' },
          { label: '处理活动', page: 'activities', description: '确认评估采用的业务范围与当前修订。' },
          { label: '检查与整改', page: 'checks', description: '查看相关措施核验及尚未完成的整改。' },
        ],
        gap: '个人信息保护影响评估不能替代个人信息保护合规审计；未知处理依据和业务条件仍须有据核实。',
      },
      personalAuditTask,
      {
        id: 'rights-retention',
        label: '告知与保存删除检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '按适用条件核对告知、同意或其他处理依据及其记录。',
          '核实个人权利请求的受理、身份核验、处理结果与业务记录。',
          '对照保存安排检查删除、返还或继续保留的实际依据与执行结果。',
        ],
        deliverables: ['告知与权利响应核查材料', '保存、删除及未完成问题的实际依据'],
        entries: [
          { label: '处理活动', page: 'activities', description: '核对登记的用途、保留安排与保护措施。' },
          { label: '检查与证据', page: 'checks', description: '引用源系统记录，登记差异及整改复核。' },
        ],
        gap: '当前平台不受理个人权利请求、不维护同意台账，也不执行源系统删除；相关操作与原始材料仍在业务系统完成。',
      },
    ],
    sources: [personalInformationLaw, personalAuditRegulation],
  },
  {
    id: 'crossborder',
    label: '数据跨境合规',
    icon: 'data-flows',
    description: '核查跨境链路、数据范围与保护配置，区分技术发现和待核实业务条件。',
    trigger: '境外接收方、境外服务或境外人员可能接触境内数据。',
    output: '检查结果、技术证据、整改建议与复测记录',
    views: [
      { label: '处理活动', page: 'activities', description: '查看已有活动，核对实际出境接收方、地域与处理范围。' },
      { label: '数据流转', page: 'data-flows', description: '选择相关服务与来源，对照已观测关系及缺失范围。' },
      { label: '合规评估', page: 'assessments', description: '选择数据出境或其他实际适用的评估，核对事实与独立复核状态。' },
      { label: '合规报告', page: 'reports', description: '选择实际处理活动范围，查看已有内部报告与历史材料。' },
    ],
    tasks: [
      {
        id: 'scope',
        label: '出境链路与数据范围检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '确认实际数据、用途、接收方、处理地域、远程访问与备份安排。',
          '核实数据类型、去重主体规模、关基身份及拟适用例外的真实依据。',
          '按实际条件核对适用路径，未知条件保留为待核实。',
        ],
        deliverables: ['出境范围与接收方底稿', '路径判断所需事实与待核实条件'],
        entries: [
          { label: '处理活动', page: 'activities', description: '核对数据出境活动、接收方和地域。' },
          { label: '分类依据', page: 'classification', description: '核对个人信息、重要数据等已登记类别的依据。' },
          { label: '数据流转', page: 'data-flows', description: '查看实际观测范围，不足部分请业务与技术人员补充。' },
        ],
        gap: '有限样本行数不等于去重自然人数，IP 或地域标签也不能单独证明完整出境范围。',
      },
      {
        id: 'assessment',
        label: '出境数据与保护检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '固定本次出境活动、接收方、数据范围和采用的模板版本。',
          '核对路径条件、权益与安全影响、措施和证据；涉及个人信息时核实影响评估要求。',
          '补齐业务事实与关联整改，由独立人员核对采用范围及结论。',
        ],
        deliverables: ['内部数据出境评估及引用证据', '适用影响评估材料与整改复核记录'],
        entries: [
          { label: '合规评估', page: 'assessments', description: '按实际条件选择数据出境或个人信息影响评估模板。' },
          { label: '检查与证据', page: 'checks', description: '核对出境链路、措施和整改后的新证据。' },
        ],
        gap: '平台中的数据出境评估是内部核实与复核记录，不代表主管部门数据出境安全评估已经通过。',
      },
      {
        id: 'procedures',
        label: '协议与报送证据检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '核实本次需办理的安全评估申报、标准合同备案、认证或适用例外。',
          '对照实际路径要求核对报告、协议、接收方与其他办理材料。',
          '分别保存内部审查、正式提交、补充材料和外部结果的真实凭证。',
        ],
        deliverables: ['适用路径与办理材料清单', '真实提交凭证、正式结果及适用范围'],
        entries: [
          { label: '处理活动', page: 'activities', description: '核对登记接收方、传输路径与保护安排。' },
          { label: '检查与证据', page: 'checks', description: '引用实际材料、办理凭证与核查结果。' },
          { label: '现有内部报告', page: 'reports', description: '核对内部检查与证据，供准备正式材料时参考。' },
        ],
        gap: '平台不代办申报、备案或认证；内部报告和独立复核不等于外部提交成功、备案完成或正式批准。',
      },
      {
        id: 'review',
        label: '变更与持续复查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '核实变更前后的数据、目的、方式、接收方、地域和措施。',
          '按实际变化复查适用路径、协议、既有评估及正式结果的范围。',
          '保存新证据及重评结果，原报告与旧采用事实继续按原时点留存。',
        ],
        deliverables: ['变更范围与影响说明', '复查、重评及后续办理依据'],
        entries: [
          { label: '处理活动与历史', page: 'activities', description: '进入活动详情，核对修订与变更历史。' },
          { label: '合规评估', page: 'assessments', description: '核对过期标记，按实际条件开展业务变更重评。' },
          { label: '历史报告归档', page: 'reports/archives', description: '核对仍保留的历史报告及采用范围。' },
        ],
        gap: '正式办理结果的有效范围、期限与后续义务须按真实凭证核实，平台不会自动续办。',
      },
    ],
    sources: [
      { label: '促进和规范数据跨境流动规定（2024年公布）', url: 'https://www.cac.gov.cn/2024-03/22/c_1712776611775634.htm' },
      { label: '数据出境政策法规问答（2026年9月）', url: 'https://www.cac.gov.cn/2026-09/11/c_1790876549989064.htm' },
    ],
  },
  {
    id: 'ai',
    label: 'AI 数据安全',
    icon: 'api-security',
    description: '检查 AI 输入、知识库、模型及工具的配置与数据风险，记录来源覆盖和复测结果。',
    trigger: '采购外部 AI、接入知识库或 Agent、上线 AI 应用、对外发布内容。',
    output: '检查结果、技术证据、整改建议与复测记录',
    views: [
      { label: '数据外发与事件调查', page: 'capabilities?tab=data-security', description: '按实际来源核对外发通道、AI目的地、事件证据及处理记录。' },
      { label: '处理活动', page: 'activities', description: '查看已有活动，选择实际 AI 使用业务并核对供应商与数据范围。' },
      { label: '合规评估', page: 'assessments', description: '选择 AI 使用或其他实际适用的评估，核对采集配置与答复。' },
      { label: '数据流转', page: 'data-flows', description: '按实际服务与来源查看关系，核对 AI 使用涉及的已观测路径。' },
      { label: '运行时观测', page: 'api-security/runtime', description: '选择实际接入的服务与来源，查看接口观测、发现和覆盖限制。' },
    ],
    tasks: [
      {
        id: 'assessment',
        label: 'AI 使用安全检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '确认实际服务对象、自身角色、业务用途与使用范围。',
          '核实输入数据、供应商、处理地域、留存训练安排及工具权限。',
          '按条件关联个人信息、数据出境和对外提供要求，保留未知条件与证据缺口。',
        ],
        deliverables: ['AI 使用范围与适用条件', 'AI 使用评估、证据与复核记录'],
        entries: [
          { label: '处理活动', page: 'activities', description: '登记或核对 AI 使用活动与供应商安排。' },
          { label: '合规评估', page: 'assessments', description: '选择 AI 使用评估模板，核对事实、答复与缺口。' },
          { label: '检查与证据', page: 'checks', description: '核对保护措施及相关整改结果。' },
        ],
        gap: '内部使用 AI 不会自动等同于面向公众提供生成式 AI 服务；适用条件仍由实际角色与业务事实确定。',
      },
      {
        id: 'data',
        label: 'AI 输入与知识库检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '区分输入、检索知识库、训练或微调的数据来源、范围与用途。',
          '核实使用依据、个人信息与保密要求、供应商是否留存或用于训练。',
          '引用实际配置及授权测试记录，保留缺失来源与未覆盖的数据路径。',
        ],
        deliverables: ['数据来源、用途与使用依据', '配置、访问边界及措施核查记录'],
        entries: [
          { label: '业务资产目录', page: 'asset-catalog', description: '核对已有数据对象、来源与责任团队。' },
          { label: '分类分级', page: 'classification', description: '核对数据属性和保护要求的采用依据。' },
          { label: '检查与证据', page: 'checks', description: '记录数据来源、配置和措施验证的实际引用。' },
        ],
        gap: '当前平台不自动盘点全部知识库、训练集或 AI 对话正文，也不自动证明训练数据使用依据充分。',
      },
      {
        id: 'suppliers-tools',
        label: '模型服务与工具权限检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '核对模型服务、处理地域、条款版本、留存和训练设置。',
          '明确工具、账号、数据与授权范围，使用批准的合成用例验证边界。',
          '发现差异时保存配置、整改与新测试记录，按条件复查共享或出境要求。',
        ],
        deliverables: ['供应商条款与实际配置依据', '工具权限范围、授权测试及整改记录'],
        entries: [
          { label: '处理活动', page: 'activities', description: '核对模型服务、接收地域、留存与保护措施。' },
          { label: 'API 安全与运行时', page: 'api-security/runtime', description: '核对已接入服务与接口的实际观测及覆盖。' },
          { label: '检查与证据', page: 'checks', description: '引用实际权限配置与授权测试结果。' },
        ],
        gap: '平台现有 API 观测不等于完整 Agent 工具权限控制；模型、知识库和工具清单仍需按实际配置核实。',
      },
      {
        id: 'public-service',
        label: '公众服务配置检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '确认是内部使用、调用外部服务，还是向外部用户提供相关服务。',
          '按实际角色核实内容发布、显式与隐式标识及其他适用要求。',
          '留存业务说明、实际配置、检测材料及所需正式手续的真实凭证。',
        ],
        deliverables: ['服务角色与适用要求说明', '内容标识、配置及相关办理材料'],
        entries: [
          { label: '处理活动', page: 'activities', description: '核对 AI 服务对象、用途与处理范围。' },
          { label: '检查模板', page: 'templates', description: '由专业人员核对或补充实际适用的检查要求。' },
          { label: '检查与证据', page: 'checks', description: '引用外部实际检测、配置或手续凭证。' },
        ],
        gap: '平台目前不检测或添加生成合成内容标识，也不代办算法备案、安全评估等正式手续。',
      },
    ],
    sources: [
      { label: '生成式人工智能服务管理暂行办法（2023年公布）', url: 'https://www.cac.gov.cn/2023-07/13/c_1690898327029107.htm' },
      { label: '人工智能生成合成内容标识办法（2025年公布）', url: 'https://www.cac.gov.cn/2025-03/14/c_1743654684782215.htm' },
    ],
  },
  {
    id: 'sharing',
    label: '数据共享与委托',
    icon: 'integrations',
    description: '核查共享与受托链路、接收方、访问权限及退出配置，关联真实整改与复测。',
    trigger: '向合作方提供数据、委托供应商处理、服务范围或合作关系变化。',
    output: '检查结果、技术证据、整改建议与复测记录',
    views: [
      { label: '数据外发与事件调查', page: 'capabilities?tab=data-security', description: '按实际来源核对外发通道、AI目的地、事件证据及处理记录。' },
      { label: '处理活动', page: 'activities', description: '查看已有活动，选择实际共享或委托关系与处理范围。' },
      { label: '业务资产目录', page: 'asset-catalog', description: '核对相关数据对象、责任团队、共享对象与来源确认。' },
      { label: '合规评估', page: 'assessments', description: '查看个人信息影响评估或变更重评，核对本次实际适用范围。' },
      { label: '监管检查', page: 'checks', description: '选择相关检查，核对协议、权限、监督及退出的证据与整改。' },
    ],
    tasks: [
      {
        id: 'providing',
        label: '共享数据与接收方检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '区分接收方自行决定用途与按我方要求处理的实际关系。',
          '核实提供目的、数据范围、处理依据、接收方及地域。',
          '按条件核实影响评估、告知等要求，保存批准范围与实际提供依据。',
        ],
        deliverables: ['提供关系与接收方范围', '适用依据、批准及实际提供记录'],
        entries: [
          { label: '处理活动', page: 'activities', description: '核对对外共享或委托活动及接收方安排。' },
          { label: '数据流转', page: 'data-flows', description: '对照已有观测与实际提供范围。' },
          { label: '检查与证据', page: 'checks', description: '核对共享范围、接收方和权限依据。' },
        ],
      },
      {
        id: 'outsourcing',
        label: '受托处理范围检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '明确委托目的、期限、方式、数据类别、保护措施与双方责任。',
          '核对实际受托方、转委托安排、地域和约定处理边界。',
          '涉及个人信息时按条件组织影响评估，并记录供应商核查及监督依据。',
        ],
        deliverables: ['委托处理关系与范围底稿', '协议、保护能力及适用影响评估材料'],
        entries: [
          { label: '处理活动', page: 'activities', description: '记录委托目的、接收方、地域与保护安排。' },
          { label: '合规评估', page: 'assessments', description: '按适用条件开展个人信息影响评估或业务变更重评。' },
          { label: '检查与证据', page: 'checks', description: '记录委托范围、协议和监督核查依据。' },
        ],
        gap: '当前没有独立的供应商或委托专项评估题库；可复用适用的影响评估和单项检查，不能直接视为供应商准入已通过。',
      },
      {
        id: 'permissions',
        label: '共享访问权限检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '核对协议版本、批准范围、用途限制与双方安全责任。',
          '核对账号、链接、接口或数据库的实际访问边界与使用记录。',
          '保留监督发现、整改、变更与复测依据，不用保密协议代替全部核查。',
        ],
        deliverables: ['协议、批准范围与权限对照', '监督发现、整改与新复测记录'],
        entries: [
          { label: '资产与权限依据', page: 'assets', description: '核对已采集对象的来源权限与覆盖。' },
          { label: '访问审计', page: 'audit', description: '核对已接入来源保留范围内的请求元数据。' },
          { label: '检查与证据', page: 'checks', description: '记录协议、权限与监督检查结果。' },
        ],
        gap: '平台不维护完整合同台账；未接入路径和接收方实际使用情况仍需相应业务证据。',
      },
      {
        id: 'exit',
        label: '退出与删除检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '确认终止范围、涉及数据、账号、接收方及约定退出要求。',
          '取得返还、删除、撤权或继续保留的实际依据与执行回执。',
          '按真实访问路径复测，不以平台动作完成或来源沉默代替退出完成。',
        ],
        deliverables: ['退出范围与执行依据', '返还、删除、撤权及复测结果'],
        entries: [
          { label: '运营与处置', page: 'operations', description: '仅对已接入且授权的执行来源申请受控处置。' },
          { label: '检查与证据', page: 'checks', description: '登记退出执行依据、缺口与新复测。' },
          { label: '处理活动', page: 'activities', description: '核对业务范围变更与原约定。' },
        ],
        gap: '现有撤权仅覆盖受支持的来源和权限范围，不等于所有访问路径已撤销；平台不自动删除受托方数据。',
      },
    ],
    sources: [personalInformationLaw, dataRegulation],
  },
  {
    id: 'incident',
    label: '风险监测与处置',
    icon: 'shield',
    description: '汇总实际风险发现与异常信号，定位影响范围并核验处置后的来源事实。',
    trigger: '安全告警、异常访问、疑似泄露或其他事件线索。',
    output: '检查结果、技术证据、整改建议与复测记录',
    views: [
      { label: '运营与处置', page: 'operations', description: '查看实际来源事件、风险与处置记录，选择需要调查的范围。' },
      { label: 'API 安全', page: 'api-security', description: '选择实际契约扫描或运行时来源，核对接口发现及依据。' },
      { label: 'API 主动验证', page: 'validation', description: '查看已批准范围的验证计划、运行结果与关联检查。' },
      { label: '接口生命周期', page: 'api-lifecycle', description: '按实际服务与来源对照契约、观测及接口变化。' },
      { label: '监管检查', page: 'checks', description: '核对相关发现的取证、整改、新复测与独立复核记录。' },
      { label: '具体值指纹溯源', page: 'capabilities?tab=value-search', description: '以客户侧生成的匿名指纹查询文件、运行时记录与显式转换关系。' },
      { label: '数据外发与事件调查', page: 'capabilities?tab=data-security', description: '核对终端、数据库和AI事件的脱敏来源、关联证据及人工研判。' },
      { label: '运营调查与周期报告', page: 'capabilities?tab=reports', description: '按服务、用户引用、IP和时间核对冻结报告与证据缺口。' },
    ],
    tasks: [
      {
        id: 'investigation',
        label: '风险发现与定位',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '核对来源、服务、接口、时间窗与实际信号，区分契约提示和运行时观测。',
          '关联请求审计、关系依据和已批准测试，记录覆盖限制与未知项。',
          '登记风险研判、责任与后续检查，不直接把单个信号认定为已证实漏洞。',
        ],
        deliverables: ['来源与调查范围', '风险研判、授权测试及待核实事实'],
        entries: [
          { label: '运行时观测', page: 'api-security/runtime', description: '核对已接入来源、发现及风险研判。' },
          { label: '请求审计', page: 'audit', description: '按来源与时间查看保留范围内的请求记录。' },
          { label: 'API 主动验证', page: 'validation', description: '仅在已明确授权的计划与客户侧范围内验证。' },
        ],
        gap: '主动验证目前仅覆盖受限计划与已登记测试范围；普通成功响应、无新告警或未检出不能单独证明风险消除。',
      },
      {
        id: 'response',
        label: '整改与处置检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '核实事件来源、受影响业务与数据，记录已知事实和调查缺口。',
          '确定责任分工、措施范围及审批，保留源端原始材料引用。',
          '分别记录审批、实际执行、失败或回滚状态，并继续核对业务影响。',
        ],
        deliverables: ['事件事实与应急分工记录', '处置审批、执行及回滚依据'],
        entries: [
          { label: '运营与处置', page: 'operations', description: '核对来源事件及受控处置申请、回执和回滚。' },
          { label: '数据流转', page: 'data-flows', description: '调查已有观测范围内的关系与请求依据。' },
          { label: '检查与证据', page: 'checks', description: '留存调查、整改及后续复测依据。' },
        ],
        gap: '平台事件类别是来源线索，疑似泄露仍须核实。受控处置仅作用于明确接入并授权的来源，不覆盖全部业务路径。',
      },
      {
        id: 'reporting',
        label: '报告与通知证据检查',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '核实主体身份、事件经过、影响范围、已有措施及待调查事项。',
          '按适用要求确认事件等级、报告对象、方式、真实时点及后续补报安排。',
          '分别保留内部调查、正式提交、接收凭证及后续更新材料。',
        ],
        deliverables: ['事件分级依据与报告要求', '实际报告、补充材料与提交凭证'],
        entries: [
          { label: '运营与处置', page: 'operations', description: '核对事件、措施与来源回执。' },
          { label: '检查与证据', page: 'checks', description: '记录影响事实、调查材料与实际凭证引用。' },
          { label: '现有内部报告', page: 'reports', description: '核对已登记检查和证据，供准备正式材料时参考。' },
        ],
        gap: '当前平台不自动确定完整事件等级、法定报告时限或代为报送；内部报告不表示已向有关部门报告。',
      },
      {
        id: 'retest',
        label: '整改复测',
        summary: '读取所选范围的最新来源事实，对照已登记要求核查问题、证据缺口与整改建议。',
        requirements: [
          '核对原始发现、采用规则、责任范围及实际整改记录。',
          '取得同来源、同目标范围且晚于整改的新复测，失败或未知结果继续保留缺口。',
          '由符合回避要求的独立人员核验；后续新发现与旧结论分别留存。',
        ],
        deliverables: ['整改记录与新来源复测', '独立复核结论及持续跟踪依据'],
        entries: [
          { label: '检查与独立复核', page: 'checks', description: '核对证据时序、提交条件和当前复核结果。' },
          { label: '运行时检测策略', page: 'runtime-policies', description: '核对检测窗口、采用规则与策略历史。' },
          { label: '操作审计', page: 'audit/actions', description: '追溯实际操作与工作流记录。' },
        ],
        gap: '执行成功、来源未再发现和独立通过分别核验；缺少新来源证据时不能把风险显示为已经消除。',
      },
    ],
    sources: [
      { label: '国家网络安全事件报告管理办法（2025年公布）', url: 'https://www.cac.gov.cn/2025-09/15/c_1759583017717009.htm' },
    ],
  },
]

export function scenarioEntryPath(sceneID: string, page: string): string {
  const [path='',rawQuery='']=page.split('?')
  const query=new URLSearchParams(rawQuery)
  query.set('scene',sceneID)
  return path+'?'+query
}

export function scenarioOwnsPage(scene: ScenarioDefinition | undefined, page: string): boolean {
  const rootPage = page.split(/[/?]/)[0]
  return scene?.views.some(view => view.page.split(/[/?]/)[0] === rootPage) ?? false
}

export function primaryScenarioForPage(page: string): ScenarioDefinition | undefined {
  const owners: Record<string, string> = {
    assets: 'governance',
    'asset-catalog': 'governance',
    classification: 'governance',
    'data-flows': 'governance',
    activities: 'personal',
    assessments: 'personal',
    checks: 'domestic',
    reports: 'domestic',
    audit: 'domestic',
    operations: 'incident',
    'api-security': 'incident',
    validation: 'incident',
    'api-lifecycle': 'incident',
  }
  const owner = owners[page.split(/[/?]/)[0]]
  return scenarios.find(scene => scene.id === owner)
}
