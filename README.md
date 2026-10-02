# Interest Rate Swap Service

本服务提供本地利率互换合约的登记、计息、浮动端重置、净额结算、提前终止、盯市估值与崩溃恢复能力。

## 核心规则

### 合约与日历

- 每份合约使用单一记账货币。
- 支持固定对浮动合约，可设置固定端与浮动端各自的计息基准。
- 支持月度、季度、半年度、年度和零息（到期一次支付）频率。
- 支持名义本金计划；每个计息期使用该期开始日（含）之前最近一次变更后的本金。
- 支持以下工作日调整：`following`、`modified_following`、`preceding`、`unadjusted`。
- 市场曲线与工作日历通过接口注入：
  - `BusinessCalendar`
  - `CalendarRegistry`
  - `MarketCurve`

### 计息与现金流

每个计息期生成固定端和浮动端两条现金流：

```text
固定端金额 = 当期名义本金 × 固定利率 × 固定端计息期限
浮动端金额 = 当期名义本金 × (远期利率 + 利差) × 浮动端计息期限
```

支持的计息基准：

- `ACT/360`：实际天数 / 360
- `ACT/365F`：实际天数 / 365，不区分闰年
- `30/360`：每月 30 天，每年 360 天
- `ACT/ACT`：按各年度实际天数与该年闰年长度分段计算

利率与金额使用 `github.com/shopspring/decimal` 精确计算，现金流金额保留两位小数。

### 浮动利率重置

- 重置日由计息期开始日按合约工作日调整规则确定。
- 同一重置日的第一次重置会写入 `RateReset`。
- 之后同一重置日的调用无论使用何种曲线都返回已记录结果，不会重复写入或覆盖已确认现金流。
- 已确认支付对应的现金流不可再被重置修改。

### 净额结算

每期生成一条 `Payment`，同时记录固定端金额、浮动端金额和净额：

- 若 `FixedPayer=true`，净额 = 固定端金额 - 浮动端金额。
- 若 `FixedPayer=false`，净额 = 浮动端金额 - 固定端金额。
- 净额为正表示该合约的固定支付方（`FixedPayer=true` 时）或浮动支付方（`FixedPayer=false` 时）应支付的金额。

`ConfirmPayment` 按计息期开始日幂等。全部期数确认后，合约状态变为 `matured`。`NetPayable` 返回已确认净额的累计值。

### 提前终止

`Terminate` 计算终止日之后剩余现金流的现值：

```text
固定端 PV = Σ 固定端金额 × 贴现因子
浮动端 PV = Σ 浮动端金额 × 贴现因子
```

- 终止日当天及之前的现金流不纳入终止结算。
- 尚未重置的浮动端现金流使用终止日曲线预测：远期利率 + 合约利差。
- 净额符号与每期净额保持一致。
- 合约状态与终止记录在同一互斥锁和一次持久化中更新；持久化失败会回滚内存状态，不留中间状态。

### 批量估值

`ValueAll` 一次估值全部合约，且不修改任何合约状态：

- 不生成、不修改现金流、重置或支付记录。
- 对尚未重置的浮动端，使用估值曲线预测远期利率。
- `ValueAll` 中每份合约的结果与单独调用 `ValueContract` 完全一致。
- `AccruedNet` 为估值日所在计息期的应计净额，固定端和浮动端分别使用各自计息基准。

### 并发与崩溃恢复

- 服务使用 `sync.RWMutex` 保护全部内存状态。
- 现金流、重置、支付和终止记录均有确定性 ID。
- 重复重置和重复结算幂等。
- JSON 持久化先写临时文件、刷盘后再原子替换，避免半写文件。
- 服务重启时通过 `NewService` 加载快照，并基于已持久化状态继续推进。

## 内置本地实现

- `NewWeekendCalendar`：周六、周日为非工作日。
- `NewMapCalendarRegistry`：内存日历注册表。
- `NewFlatCurve`：以连续复利计算贴现因子的扁平曲线。
- `NewJSONStore`：JSON 文件快照存储。

## 示例

```go
store := irswap.NewJSONStore("/path/to/state.json")
calendars := irswap.NewMapCalendarRegistry(irswap.NewWeekendCalendar("weekend"))
service, err := irswap.NewService(calendars, store)
if err != nil {
    panic(err)
}

contract := &irswap.Contract{
    ID: "swap-001",
    Currency: "USD",
    StartDate: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
    MaturityDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
    FixedRate: decimal.NewFromFloat(0.02),
    Spread: decimal.NewFromFloat(0.001),
    FixedDayCount: irswap.DayCountThirty360,
    FloatingDayCount: irswap.DayCountThirty360,
    PaymentFrequency: irswap.FrequencyQuarterly,
    ResetFrequency: irswap.FrequencyQuarterly,
    BusinessDayConvention: irswap.BusinessDayFollowing,
    CalendarID: "weekend",
    FixedPayer: true,
    Notional: decimal.NewFromInt(1_000_000),
}
_ = service.CreateContract(context.Background(), contract)

curve := irswap.NewFlatCurve("curve", "USD", decimal.NewFromFloat(0.025), contract.StartDate)
_, _ = service.ResetFloatingRate(context.Background(), contract.ID, contract.StartDate, curve)
_, _ = service.ConfirmPayment(context.Background(), contract.ID, contract.StartDate)

valuations, _ := service.ValueAll(context.Background(), irswap.ValuationRequest{
    Curve: curve,
    ValuationDate: contract.StartDate,
})
_ = valuations
```
