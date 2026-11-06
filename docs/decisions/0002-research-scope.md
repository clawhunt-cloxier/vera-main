# ADR-0002：金融投研 Agent 与用户自选研究对象

状态：产品方向已由用户确认；工程变更待审查。日期：2026-11-05。

> 2026-11-06：本 ADR 的 `general_research` 模板、`stock/industry/other` 对象分类与 `research_question + time_range` 创建请求已被 [ADR-0003](0003-product-harness-runtime.md) 取代。“取消制造业年度点评限制”的方向保留。

用户明确 Vera 是金融投研 Agent，要求首版接入数据源，允许选择个股、行业或其他金融标的。因此取消 ADR-0001 的制造业年度点评限制；当前 SPEC 与 RESEARCH_SCOPE 为准。试用仍是发布阶段，不再作为产品定义。

本次同步产品介绍、任务创建契约、实体目录、数据模型、前端起始文案、验收与任务说明。annual_financial_review 改为 general_research；report_year 改为 research_question 和 time_range。当前业务尚未实现、无生产数据，不需要迁移已运行任务；未来不得静默重写旧版本。

单 Agent、Go 主导、全自动研究、信源留证、邀请容量控制与后期合规核查保持不变。多 Agent 收益评测仍后置。
