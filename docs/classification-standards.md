# 分类分级标准库

公开来源核验日期为 2026-11-11。目录数据保存在 `backend/internal/classificationstandards/catalog/`，随应用版本发布。

## 选择与留痕

在「分类分级 → 标准库」按行业和类型筛选，核对适用范围、发布机关、发布日期、实施日期及官方来源。工作区负责人可以设置默认标准，经办人在每项资产确认时可以另选适用版本。

初始默认采用 GB/T 43697—2024。金融、证券期货、教育、气象、健康医疗等体系保留各自的等级名称。具有明确法定类别映射的等级会同步约束一般、重要或核心数据的选择。其他体系需要结合适用目录、主管部门认定和评估材料确认法定类别。

每次保存均记录标准号、标题、行业、来源链接、原生等级、所选条款、判定口径和取证要求，并与经办人、判定依据和分类版本一起保存。后续换标或改变默认标准时，可以从分类历史查看原来的快照。并发修改会返回版本冲突，刷新后重新核对。

已替代版本供历史查阅。已发布且列有未来实施日期的版本显示实施日期。仅有发布信息的资料会显示目录覆盖情况，客户可以结合正式文件确认依据。

## 自动化与确认边界

PostgreSQL 和 OpenAPI 采集继续自动生成疑似敏感字段线索及分类建议。本轮标准规则提供结构化条款、判定口径和取证指引，保存时校验等级、规则及已定义的法定类别映射。

人数、地理范围、数据精度、业务影响和主管部门认定等条件，需要相应的源数据或评估材料支持。行业条款中的自然语言条件仍需要继续转为可验证的数据项和自动判定程序。客户在本页确认的结果可追溯至所用标准版本与判定依据。

## 公开来源与覆盖缺口

本轮采用主管部门、国家标准公开平台及其他正式发布渠道的公开资料，核对版本标识后归纳条款。仓库分发标准元数据和原创概括的规则指引，原始正式文本由对应来源提供。

- YD/T 4981—2024 已取得政府网站公开的正式文本，补入工业领域的重要、核心数据识别及目录备案口径。[公开正式文本](https://oss.jiaozuo.gov.cn/jiaozuo_gxj/upfile/202506/2025061738898197.pdf)
- LY/T 2170—2026 已取得行业标准备案平台的正式文本，补入林草分类分级及分级脱敏相关条款。[正式文本入口](https://hbba.sacinfo.org.cn/portal/online/84104ba1dfe2508a0b00ca3aaac7508a53df73ecac1d9aacd07ec9737d57814a)
- YD/T 3867—2024 的指引依据湖北省通信管理局公开版本。该公开件包含新增章节，规则条款号按该件标注，使用时需要核对所采用的文本版本。[主管部门发布页](https://hubca.miit.gov.cn/zwgk/wlaqgl/art/2025/art_3ab1b8e5a3614e4893438fba11b9ee83.html)
- GA/T 2369—2026 的公安部页面目前提供发布记录，目录已经收录，正式条款待取得公开正文后补充。[公安部发布记录](https://ywtb.mps.gov.cn/gabzh/portal/stdDetail/320329)
- YD/T 4244—2023、YD/T 4251—2023 的已取得备案下载件带有报批稿和占位编号，正式版本规则仍待补充。其他目录条目的覆盖情况逐项列在下表。

应急、商务、住建、文旅、司法、海关、医保等领域的主管部门正式分类分级文件仍需持续核验。目录覆盖及条款规则的完整性还有待补齐，当前覆盖以本清单为准。

## 版本维护

新增或修订目录时核对正式版本、替代关系、实施日期、适用范围和公开来源，更新 `CatalogAsOf`，运行目录校验及选择、历史快照测试后发布。已有分类记录按保存时的快照追溯。

## 目录清单

<!-- catalog-summary -->

共收录 54 项，其中国家标准 16 项、行业标准 23 项、规则与指南 15 项。41 项含结构化条款指引，共 153 条。清单同时标注现行、已发布和已替代版本。

| 行业领域 | 标准号或文号 | 名称 | 状态 | 条款指引数 | 来源 |
| --- | --- | --- | --- | --- | --- |
| 互联网域名服务 | YD/T 7441—2026 | 互联网域名服务数据分类分级方法 | 现行 | 0 | [官方来源](https://std.samr.gov.cn/hb/search/stdHBDetailed?id=5A166B0E8C362BDAE06397BE0A0A0423) |
| 交通运输 | GB/T 42017—2022 | 信息安全技术 网络预约汽车服务数据安全要求 | 现行 | 4 | [官方来源](https://openstd.samr.gov.cn/bzgk/std/newGbInfo?hcno=182AA2DDF9F1EB9A69193B9CA528BBC4) |
| 交通运输 | 交科技规〔2026〕3号 | 交通运输数据安全管理办法 | 现行 | 3 | [官方来源](https://xxgk.mot.gov.cn/xzgfxwj/202607/t20260713_4209545.html) |
| 人民银行业务领域 | 中国人民银行令〔2025〕第3号 | 中国人民银行业务领域数据安全管理办法 | 现行 | 4 | [官方来源](https://www.pbc.gov.cn/zhengwugongkai/attachDir/2025/11/2025111915275027791.pdf) |
| 公安视频图像 | GA/T 2369—2026 | 公安视频图像信息分类分级指南 | 现行 | 0 | [官方来源](https://ywtb.mps.gov.cn/gabzh/portal/stdDetail/320329) |
| 农业 | NY/T 4261—2022 | 农业大数据安全管理指南 | 现行 | 3 | [官方来源](https://www.moa.gov.cn/ztzl/zhnyzxd/bzgf/xb/202411/P020241122655981957113.pdf) |
| 卫生健康 | 2023年试行 | 卫生健康行业数据分类分级指南（试行） | 已发布 | 0 | [官方来源](https://www.nhc.gov.cn/wjw/tia/202409/ec2aae0087844a5fa61383d434dc0c82.shtml) |
| 卫生健康 | GB/T 39725—2020 | 信息安全技术 健康医疗数据安全指南 | 现行 | 4 | [官方来源](https://openstd.samr.gov.cn/bzgk/std/newGbInfo?hcno=239351905E7B62A7DF537856738247CE) |
| 即时通信 | GB/T 42012—2022 | 信息安全技术 即时通信服务数据安全要求 | 现行 | 2 | [官方来源](https://openstd.samr.gov.cn/bzgk/std/newGbInfo?hcno=136B8F06A548FAE8ABB375E4C65C1E38) |
| 基础电信 | YD/T 3813—2020 | 基础电信企业数据分类分级方法 | 现行 | 2 | [官方来源](https://std.samr.gov.cn/hb/search/stdHBDetailed?id=B80DCDBCBAF0CE3AE05397BE0A0A06D8) |
| 基础电信 | YD/T 4251—2023 | 电信运营商大数据安全管控分类分级技术要求 | 现行 | 0 | [官方来源](https://std.samr.gov.cn/hb/search/stdHBDetailed?id=FE86C10A0D1C3123E05397BE0A0A2FC5) |
| 工业 | SJ/T 12043—2025 | 工业数据分类分级指南 | 现行 | 0 | [官方来源](https://std.samr.gov.cn/hb/search/stdHBDetailed?id=53F41DDFDC493089E06397BE0A0A51F6) |
| 工业 | YD/T 4981—2024 | 工业领域重要数据识别指南 | 现行 | 11 | [官方来源](https://oss.jiaozuo.gov.cn/jiaozuo_gxj/upfile/202506/2025061738898197.pdf) |
| 工业互联网 | GB/T 44462.4—2026 | 工业互联网企业网络安全 第4部分：数据防护要求 | 现行 | 2 | [官方来源](https://openstd.samr.gov.cn/bzgk/std/newGbInfo?hcno=7F3CAB698C68BE2407C0FD636C9B75AC) |
| 工业互联网 | YD/T 7344—2026 | 工业互联网数据安全分类分级要求 | 已发布 | 0 | [官方来源](https://wap.miit.gov.cn/zwgk/zcwj/wjfb/gg/art/2026/art_74c4c2a0052143fcacd282226eedba23.html) |
| 工业和信息化 | 工信部网安〔2022〕166号 | 工业和信息化领域数据安全管理办法（试行） | 现行 | 3 | [官方来源](https://www.miit.gov.cn/gyhxxhb/jgsj/wlaqglj/sjaq/art/2022/art_8aefad495b3940f8a3236eef1d83096c.html) |
| 广播电视 | GY/T 339.2—2020 | 有线电视网络大数据技术规范 第2部分：平台要求 | 现行 | 1 | [官方来源](https://www.nrta.gov.cn/module/download/downfile.jsp?classid=0&filename=36ceb3f3766f44d78d918b31b0a47184.pdf) |
| 政务与公共数据 | GB/T 39477—2020 | 信息安全技术 政务信息共享 数据安全技术要求 | 现行 | 3 | [官方来源](https://openstd.samr.gov.cn/bzgk/std/newGbInfo?hcno=1C7D8CDD4B3EF8CA8C7822D99B76EA4F) |
| 政务与公共数据 | 武汉市2025年试行 | 武汉市公共数据分类分级指南（试行） | 现行 | 3 | [官方来源](https://whsj.wuhan.gov.cn/zwgk/zcfg/gfxwj1/202609/t20260909_2845237.shtml) |
| 教育 | JY/T 0661—2025 | 教育数据分类分级指南 | 现行 | 7 | [官方来源](https://www.moe.gov.cn/srcsite/A16/s3342/202601/W020260107510856466340.pdf) |
| 林业草原 | LY/T 2170—2026 | 林业草原网络安全技术要求 | 现行 | 4 | [官方来源](https://hbba.sacinfo.org.cn/portal/online/84104ba1dfe2508a0b00ca3aaac7508a53df73ecac1d9aacd07ec9737d57814a) |
| 民航 | MH/T 3039—2025 | 民航领域数据分类分级要求 | 现行 | 3 | [官方来源](https://www.caac.gov.cn/XXGK/XXGK/BZGF/HYBZ/202507/P020250724401412250948.pdf) |
| 气象 | QX/T 757—2025 | 气象数据安全分级指南 | 现行 | 4 | [官方来源](https://www.cma.gov.cn/zfxxgk/gknr/flfgbz/bz/202512/P020251225481554080615.pdf) |
| 水利 | SL/T 864—2026 | 水利数据分类分级规则 | 现行 | 0 | [官方来源](https://slt.hunan.gov.cn/slt/xxgk/slxw/slxw_1/202601/t20260119_33897364.html) |
| 水运与船舶 | GB/T 47313—2026 | 智能船舶数据分类、分级与标识 | 现行 | 6 | [官方来源](https://openstd.samr.gov.cn/bzgk/std/newGbInfo?hcno=0A9BB522D082EC646A28EA1440251AD6) |
| 汽车 | GB/T 44464—2024 | 汽车数据通用要求 | 现行 | 3 | [官方来源](https://openstd.samr.gov.cn/bzgk/std/newGbInfo?hcno=D63AAC0203E9B169F74B10E547A3CBCE) |
| 汽车 | YD/T 6617—2025 | 车联网平台重要数据识别指南 | 现行 | 0 | [官方来源](https://std.samr.gov.cn/hb/search/stdHBDetailed?id=490534C38E2AC1A3E06397BE0A0A0AB9) |
| 汽车 | 第7号令 | 汽车数据安全管理若干规定（试行） | 现行 | 5 | [官方来源](https://www.cac.gov.cn/2021-08/20/c_1631049984897667.htm) |
| 海洋 | HY/T 0366—2023 | 海洋数据分类分级标准 | 现行 | 0 | [官方来源](https://hbba.sacinfo.org.cn/snDetail/cdcd86a0978b80e6f39effe8784e58df32527753425b78967f2ff90e6b2aaa84) |
| 生态环境 | HJ 417—2025 | 生态环境信息分类与代码 | 现行 | 2 | [官方来源](https://www.mee.gov.cn/ywgz/fgbz/bz/bzwb/other/xxbz/202502/t20250220_1102564.shtml) |
| 电信 | GB/T 42447—2023 | 信息安全技术 电信领域数据安全指南 | 现行 | 2 | [官方来源](https://openstd.samr.gov.cn/bzgk/std/newGbInfo?hcno=78419327C5FA2389D10516874C69D7CE) |
| 电信 | YD/T 3802—2025 | 电信领域数据安全分级保护要求 | 现行 | 0 | [官方来源](https://std.samr.gov.cn/hb/search/stdHBDetailed?id=490534C38E29C1A3E06397BE0A0A0AB9) |
| 电信 | YD/T 3867—2024 | 电信领域重要数据识别指南 | 现行 | 7 | [官方来源](https://hubca.miit.gov.cn/zwgk/wlaqgl/art/2025/art_3ab1b8e5a3614e4893438fba11b9ee83.html) |
| 电信网和互联网 | YD/T 4244—2023 | 电信网和互联网数据分类分级技术要求与测试方法 | 现行 | 0 | [官方来源](https://std.samr.gov.cn/hb/search/stdHBDetailed?id=FE86C10A0D163123E05397BE0A0A2FC5) |
| 电子商务 | GB/T 42014—2022 | 信息安全技术 网上购物服务数据安全要求 | 现行 | 2 | [官方来源](https://openstd.samr.gov.cn/bzgk/std/newGbInfo?hcno=9679A3BF5D736D7FC92FD8157AAAAC82) |
| 矿山 | KSSJ/AQ11—2023 | 智能化矿山数据融合共享 数据分级定级规范 | 现行 | 3 | [官方来源](https://www.chinamine-safety.gov.cn/xw/zt/2018zt/mkjqryfyy/gzdt_01/202307/W020230704681873630871.pdf) |
| 科学数据 | GB/T 43705—2025 | 科学数据安全分类分级指南 | 现行 | 4 | [官方来源](https://openstd.samr.gov.cn/bzgk/std/newGbInfo?hcno=80B39CC3A1E12960B2AB7001C2B9437A) |
| 科学数据 | 国办发〔2018〕17号 | 科学数据管理办法 | 现行 | 1 | [官方来源](https://www.most.gov.cn/ztzl/zdzx/zcwj/201804/t20180403_5725.html) |
| 统计 | 2022 年版 | 统计数据分类分级标准规范（2022） | 现行 | 4 | [官方来源](https://www.stats.gov.cn/zs/tjws/jbtjzswd/tjbz/202503/t20250324_1959129.html) |
| 网络支付 | GB/T 42015—2022 | 信息安全技术 网络支付服务数据安全要求 | 现行 | 2 | [官方来源](https://openstd.samr.gov.cn/bzgk/std/newGbInfo?hcno=CEB5771DBBF05ED5EA99EBA50896537F) |
| 网络音视频 | GB/T 42016—2022 | 信息安全技术 网络音视频服务数据安全要求 | 现行 | 2 | [官方来源](https://openstd.samr.gov.cn/bzgk/std/newGbInfo?hcno=20615352AC128FC97F8005161976B921) |
| 能源 | 2026年版 | 能源行业数据分类分级指南（2026年版） | 现行 | 6 | [官方来源](https://www.nea.gov.cn/20260630/3b456b6f83144abcb989110dcfba4d7d/c.html) |
| 自然资源与地理信息 | 2025年指南 | 地理信息数据分类分级工作指南（试行） | 已发布 | 0 | [官方来源](https://www.gsjn.gov.cn/zfxxgk/zfbmxxgk/zrzyj/fdzdgknr/zrzyxx/chdlxxgl/art/2025/art_98959233d8c94a2ca55d0cc90c6668f0.html) |
| 自然资源与地理信息 | 自然资发〔2024〕57号 | 自然资源领域数据安全管理办法 | 现行 | 2 | [官方来源](https://app.www.gov.cn/govdata/gov/202403/30/513508/article.html) |
| 证券期货业 | GB/T 42775—2023 | 证券期货业数据安全风险防控 数据分类分级指引 | 现行 | 4 | [官方来源](https://openstd.samr.gov.cn/bzgk/std/newGbInfo?hcno=DB820CE40307DA73731814F2AB0E2DD6) |
| 证券期货业 | JR/T 0158—2018 | 证券期货业数据分类分级指引 | 已替代 | 0 | [官方来源](https://cfstc.pbc.gov.cn/bzgk/detail/?bzId=1753&id=3) |
| 通用 | GB/T 43697—2024 | 数据安全技术 数据分类分级规则 | 现行 | 9 | [官方来源](https://openstd.samr.gov.cn/bzgk/std/newGbInfo?hcno=F0C385EDC38CBF277AEC021F23126ADE) |
| 通用 | GB/T 45574—2025 | 数据安全技术 敏感个人信息处理安全要求 | 现行 | 2 | [官方来源](https://openstd.samr.gov.cn/bzgk/std/newGbInfo?hcno=F9F3A2EBF49E9B4D73AD8C8912986D5A) |
| 邮政寄递 | GB/T 42013—2022 | 信息安全技术 快递物流服务数据安全要求 | 现行 | 3 | [官方来源](https://openstd.samr.gov.cn/bzgk/std/newGbInfo?hcno=CDBCE8F2E4CDE60A86092361A8796178) |
| 邮政寄递 | YZ/T 0189—2023 | 寄递服务用户个人信息保护要求 | 现行 | 1 | [官方来源](https://gd.spb.gov.cn/gdsyzglj/c106372/c106377/202408/eea8cc4961ff444d86d5497d73b919b0.shtml) |
| 金融业 | JR/T 0171—2020 | 个人金融信息保护技术规范 | 现行 | 3 | [官方来源](https://cfstc.pbc.gov.cn/bzgk/detail/?bzId=1856&id=0) |
| 金融业 | JR/T 0197—2020 | 金融数据安全 数据安全分级指南 | 现行 | 6 | [官方来源](https://cfstc.pbc.gov.cn/bzgk/detail/?bzId=1873&id=0) |
| 金融信息服务 | 国信办通字〔2026〕2号 | 金融信息服务数据分类分级指南 | 现行 | 7 | [官方来源](https://www.cac.gov.cn/2026-06/13/c_1782919789934988.htm) |
| 银行保险业 | 金规〔2024〕24号 | 银行保险机构数据安全管理办法 | 现行 | 4 | [官方来源](https://www.nfra.gov.cn/cn/view/pages/ItemDetail.html?docId=1192308) |
