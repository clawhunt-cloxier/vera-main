# EMQuant SDK 接入准备交接

- 操作者：Codex；准备分支：`agent/codex/T014b-emquant`；基线：`447aee1`。
- 时间：2026-11-12 UTC。此记录是接入调查，不代表 T014b 已领取或完成；T014 仍未标记完成，公共契约及迁移尚未修改。

## 已验证

- 用户提供的 `EMQuantAPI_Python/python3/libs/linux/x64/libEMQuantAPIx64.so` 为 Linux x86-64 动态库；`ldd` 所列依赖在本机可解析。此前将搜索限制在三层目录，误报缺少 Linux 库；实际库位于更深层。
- 复制 SDK 到 `/tmp/vera-emquant-probe` 后，使用包内 Python 包装做一次临时诊断：账号登录返回错误码 0，`csd` 查询 `300059.SZ` 的 `CLOSE`（2026-11-02）返回错误码 0，结果有一个代码和一个日期。已调用 `stop`。登录使用 `ForceLogin=0`、`RecordLoginInfo=0`；未在此仓库保存凭证、返回值或原始响应。这仅证明该账户该次调用可用，不证明其他字段、数据集或许可范围。
- 同一诊断在受限网络沙箱中返回错误码 10002002（厂商错误文本为 network connect failure）；开放网络后上述调用成功。SDK 可修改自身目录中的运行文件，联调用副本并按厂商要求持久配置运行目录。
- 原生库导出 `start`、`stop`、`csd`、`css`、`releasedata`、`setcallback` 和 `setserverlistdir`。Python 包装中有对应 ctypes 签名及数据结构，可作为 Go 原生绑定的参考；数据返回的内存须按 SDK 约定释放。厂商 C 头文件、ABI 兼容承诺及并发/取消语义尚未核实。
- 使用从 Go 官方发布清单核验 SHA-256 的 Go 1.27.3，在 `/tmp/vera-emquant-go-probe` 编译一次性 cgo/dlopen 探针。该探针根据 Python ctypes 声明调用 SDK 原生 ABI：登录返回 0；同一 `csd` 查询返回 0，代码、日期、指标和数值数量均为 1，数值类型为 11（double）；随后调用 `releasedata` 和 `stop`。未记录价格。验证范围限于本机这份 SDK 的一次调用，不证明 ABI 稳定、映射正确、线程安全或可用于生产；临时探针不属于应用实现。
- 在 `backend/internal/dataservice/emquant` 增加未注册的 Go 调用准备组件：Linux/cgo 动态加载、同步 CSD 查询、返回维度与类型解码、串行单会话和显式释放；不支持的平台明确返回错误。模拟动态库覆盖登录/查询错误、数组截断、顺序、空值和关闭行为；可选真实库测试仅检查数据形状和类型，不输出行情。SDK 包装及并发语义尚需厂商确认，不应视为完整适配器。
- 验证：`go test -race ./internal/dataservice/emquant`、`CGO_ENABLED=0 go test ./internal/dataservice/emquant`、`go vet ./internal/dataservice/emquant` 和 `make check-docs` 通过；用 `EMQUANT_INTEGRATION=1` 与隔离 SDK 副本运行 `TestLiveSDK` 通过。未执行全量项目测试。凭证只通过临时进程环境传入，未写入仓库。
- 解包 SDK 附带的 V2.7.9.0 CHM 指标手册（仅本地临时目录）。沪深股票序列行情表列出 `OPEN/HIGH/LOW/CLOSE/PRECLOSE/VOLUME/AMOUNT/TURN/TRADESTATUS/TAFACTOR`，可分别作为日线字段候选；参数表记载 `Period=1` 为日、`AdjustFlag=1` 为不复权。融资融券表列出 `TRADINGBALANCEM/PURCHASEWITHBORROWEDMONEYM/REPAYMENTTOBROKERM/SECLENDINGBALANCEVOLUMEM/SECLENDINGBALANCEM`。估值表列出 `MV/PETTM/PSTTM/PBMRQ` 等。以上只是字段名称候选，不是已经验证的映射：手册表格未明确这些字段的全部数值单位、停牌状态编码、后复权因子基准、流通市值与股息率口径；对应字段不得据此猜测换算或声明可用。需取得厂商口径、授权可保存的样本并与统一数据目录逐字段核验。
- SDK Python 包装将 Linux 语言版本设为 `LANGUAGEVERSION=503`；Go 组件已与其对齐并拒绝无效 UTF-8。原先探针用的 `4` 虽能跑通单次数值请求，但不足以证明文本编码；更新后的真实 SDK 测试查询了交易状态，确认返回文本包含汉字，未输出或留存返回内容。

## 未完成与下一步

1. T014b 依赖 T014，现有 `dataservice.Provider` 的全量 `Entities/Records/Capabilities` 接口与 DATA_SERVICE 的分页原始响应、映射与血缘设计不同；先厘清 T014 未完成范围及任务依赖，避免绕过原始响应存档、映射和快照机制。
2. 账户许可的存储、试用用户展示、第三方模型处理范围尚未确认；生产数据应继续默认禁用，不能由登录成功推断授权。不得提交真实凭证、登录日志和未获许可的录制样本。
3. 核实原生 ABI、版本、服务器连接和部署依赖；实现 Go 接入与安全的密钥注入。逐个验证各必需数据集的字段、单位、时间与修订语义和历史覆盖，并为每个映射制作允许入库的样本测试，完成目录、来源、血缘与许可登记。一次日线查询不能证明 V02。
4. 组件尚未接入 provider、数据治理或应用启动路径，没有原始存档和字段映射；不声明 T014b 或 V02 完成。用户原工作树中的 `.claude/` 未触碰。
