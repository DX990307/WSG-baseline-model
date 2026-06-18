# Flex-PTCL TLB Entry Design

## Background

GPU address translation 的开销主要来自三个地方：

```text
1. L1/L2 TLB lookup latency
2. L2 TLB MSHR pressure
3. downstream page-table walk / IOMMU traffic
```

在传统 PTE-granular 设计中，每个 missed VPN 通常独立占用 MSHR entry，并独立向下游发起 translation request。如果一个 warp、一个 CU、或者多个 GPM 在短时间内访问同一个 page table cacheline 中的多个 PTE，那么这些请求在语义上有明显的局部性，但 baseline 仍会把它们当成多个独立 translation miss 处理。

PTCL 的核心观察是：一个 page table cacheline 通常可以覆盖 8 个连续 PTE。因此，如果多个 translation requests 落在同一个 PTCL 中，L2 TLB 可以用一个 base VPN 和一个 8-bit bitmap 来描述它们，而不是为每个 PTE 分配完全独立的 miss state。

## Existing PTCL Mode

当前代码中已经实现了 PTCL-aware translation flow，主要包括以下几个部分。

### PTCL-Granularity MSHR Coalescing

MSHR entry 使用 PTCL base address 加 bitmap 的形式：

```text
baseVAddr = align_down(vAddr, 8 * pageSize)
bitmap[8] = requested PTE bits in this PTCL
```

当新的 translation request 到达 L2 TLB：

```text
1. 先根据 baseVAddr 查找现有 MSHR entry。
2. 如果已有 entry，更新 bitmap，并把上层 request coalesce 进去。
3. 如果没有 entry，再分配新的 MSHR entry。
```

这样，同一个 PTCL 内的多个 PTE miss 可以共享一个 MSHR entry，降低 MSHR pressure。

### Adaptive PTCL/PTE Mode

当前机制不是永久开启 PTCL mode，而是用 coalescing counter 判断 workload 是否适合 PTCL：

```text
if completed request contains multiple useful bits:
    counter increases
else:
    counter decreases
```

当 counter 高于 high threshold 时，L2 TLB 进入 PTCL mode；当 counter 低于 low threshold 时，回到 PTE mode。threshold gap 用于避免频繁切换。

### PTCL Downstream Semantics

在 PTCL mode 下，L2 TLB 可以把同一个 PTCL 的多个 miss 合并为更少的 downstream translation requests。对于 ReLU 这类规整访问 workload，实验中可以观察到 downstream request 明显减少。例如一组 ReLU 数据中：

```text
baseline downstream_req_count = 17182
ptcl_mode downstream_req_count = 9006
```

这说明当前 PTCL mode 已经有效地降低了 downstream page walk / request traffic，也解释了 PTCL mode 在某些 workload 上有明显性能收益。

## Current GMMU L2 TLB Implementation

当前 active GMMU L2 TLB 代码主要在：

```text
akita/mem/vm/tlb_gmmu/tlb copy 2.go
akita/mem/vm/tlb_gmmu/tlbmshr copy.go
akita/mem/vm/tlb_gmmu/internal/set.go
```

### TLB Set / Entry Organization

当前 TLB set 是 PTE-granular 的 set-associative structure：

```text
Sets[] -> internal.Set
Set:
  blocks[]:
    page      vm.Page
    wayID     int
    lastVisit uint64
```

lookup key 是：

```text
PID + exact vAddr
```

也就是说，当前每个 set block 只保存一个 PTE：

```text
1 TLB block = 1 translated page / 1 PTE
```

访问接口：

```text
Lookup(pid, vAddr) -> wayID, page, found
Update(wayID, page)
Evict() -> LRU victim
Visit(wayID)
```

replacement 使用 `visitList` 维护 LRU 顺序。每次命中或填充后调用 `Visit` 更新访问时间。

当前 set 不理解 PTCL bitmap，也没有 sectorized entry。PTCL request 最终仍然会被拆成多个 exact-vAddr lookup。

### GMMUTLB Main State

当前 `GMMUTLB` 中与 PTCL/TLB lookup 相关的核心状态：

```text
Sets []internal.Set

mshr mshr
respondingMSHREntry []*mshrEntry

vpnMSHRBaseline bool
ptclMode bool
coalescingCounter int
ptclHighThreshold int
ptclLowThreshold int

pteLookupLatencyCycles int
pteLookupWaitingQueue []pteLookupJob
pteLookupInflight []pteLookupJob
pteLookupGroups map[pteLookupGroupKey]*pteLookupGroup
pteLookupReadyToIssue []pteLookupGroupKey
ptclRepresentativeMiss map[pteLookupGroupKey][8]bool
```

其中：

```text
vpnMSHRBaseline = true:
  MSHR key 使用 exact VPN。
  PTCL coalescing 被关闭，用于 baseline。

vpnMSHRBaseline = false:
  MSHR key 使用 PTCL baseVAddr。
  MSHR 可以在同一个 PTCL 内 coalesce 多个 PTE request。
```

### Tick Pipeline

每个 tick 中，GMMU L2 TLB 大致按以下顺序工作：

```text
1. process control request
2. advance internal PTE lookup jobs
3. parse downstream response
4. respond ready MSHR entry to upper level
5. process incoming request from top port / outside port
6. advance PTE lookup jobs again
7. issue queued prefetch
```

其中 internal PTE lookup jobs 是当前 PTCL mode 仍然需要优化的部分。

### MSHR Entry Format

当前 MSHR entry 已经是 PTCL-aware 的 bitmap format：

```text
type mshrEntry struct {
    pid            vm.PID
    baseVAddr      uint64

    UplevelBitMap  [8]bool  // upper-level requested bits
    IssuedBitMap   [8]bool  // bits already sent to lookup/downstream
    ResponseBitMap [8]bool  // bits already translated

    Requests       []*vm.TranslationReq
    reqToBottom    *vm.TranslationReq
    Pages          [8]vm.Page

    RealAddrBitmap [8]bool
    RealAddrTime   [8]sim.VTimeInSec
}
```

主要含义：

```text
UplevelBitMap:
  上层真正需要返回的 PTE bits。

IssuedBitMap:
  已经进入 internal lookup 或 downstream translation 的 bits。
  用于避免重复 enqueue / send。

ResponseBitMap:
  已经收到 translation response 或 TLB hit 的 bits。

RealAddrBitmap / RealAddrTime:
  记录真实 demand 到达的 bit 和时间，用于 adaptive PTCL mode 判断。
```

### MSHR Keying

MSHR 支持两种 key：

```text
per-VPN baseline:
  key = exact VPN page address

PTCL-aware mode:
  key = baseVAddr = align_down(vAddr, 8 * pageSize)
```

对应代码逻辑：

```text
if perVPNMSHR:
    entry base = exact page address
else:
    entry base = PTCL baseVAddr
```

因此同一 PTCL 内的 VPN 可以合并到同一个 MSHR entry 中。

### Request Flow

当前 incoming translation request 的主流程：

```text
processTranslation(now, req):
  1. normalize bitmap
  2. record demand against pending prefetch
  3. lookup MSHR entry by PID + baseVAddr
  4. if MSHR hit:
       processTLBMSHRHit
     else:
       handleTranslationMiss
```

注意：当前 `processTranslation` 不直接查 TLB set。真正的 TLB lookup 被建模为 internal PTE lookup job，进入 waiting queue 后延迟完成。

### MSHR Hit Flow

如果 request 命中已有 MSHR entry：

```text
processTLBMSHRHit:
  1. check entry depth
  2. update UplevelBitMap / RealAddrBitmap / RealAddrTime
  3. append original request into MSHR entry
  4. enqueue additional PTE lookup jobs if needed
  5. maybe enqueue prefetch
  6. if entry is ready, schedule response
```

这保证新来的 request 仍然按现有 MSHR coalescing 逻辑合并。

### Miss Flow

如果没有 MSHR entry：

```text
handleTranslationMiss:
  1. check MSHR capacity
  2. compute lookupBitmap
  3. allocate MSHR entry
  4. append original request
  5. record demand translation start
  6. enqueue internal PTE lookup jobs
  7. maybe enqueue prefetch
```

lookup bitmap 的计算：

```text
requestedBitmap = req.BitMap or single-page bit

if ptclMode && !vpnMSHRBaseline:
    lookupBitmap = fullBitmap()
else:
    lookupBitmap = requestedBitmap

lookupBitmap = filterMappedBitmap(lookupBitmap)
```

这就是为什么当前 PTCL mode 下，一个 request 可能展开成 8 个 internal lookup jobs。

### Internal PTE Lookup Queue

当前 L2 TLB lookup latency 通过 waiting queue + inflight slots 建模：

```text
pteLookupWaitingQueue []pteLookupJob
pteLookupInflight []pteLookupJob
```

每个 job：

```text
type pteLookupJob struct {
    groupKey  pteLookupGroupKey
    req       *vm.TranslationReq
    vAddr     uint64
    bit       int
    readyTime sim.VTimeInSec
}
```

每个 job 占一个 lookup slot，持续：

```text
pteLookupLatencyCycles
```

slot capacity 使用：

```text
numReqPerCycle
```

例如当前可以理解为 L2 TLB 每轮最多同时推进一定数量的 lookup jobs。

### Lookup Group

同一个 PTCL 的多个 lookup jobs 会被归入一个 group：

```text
type pteLookupGroup struct {
    key               pteLookupGroupKey
    req               *vm.TranslationReq
    ptclLookup        bool
    lookupBitmap      [8]bool
    missBitmap        [8]bool
    representedBitmap [8]bool
    remainingJobs     int
}
```

group 的作用：

```text
1. 追踪一个 request/PTCL 的所有 internal lookup jobs。
2. 记录哪些 bits lookup miss。
3. 等所有 jobs 完成后，再决定 downstream bitmap。
```

### PTE Lookup Completion

当一个 lookup job ready：

```text
processReadyPTELookupJob:
  1. exact-vAddr lookup into Sets[setID]
  2. if hit:
       update MSHR page/response bitmap
       schedule ready MSHR entry if possible
  3. if miss:
       set group.missBitmap[bit] = true
  4. group.remainingJobs--
  5. if group complete:
       finishPTELookupGroup
```

这里的关键点是：

```text
当前 TLB set lookup 是 exact-vAddr。
PTCL bitmap lookup 被拆成多个 exact-vAddr lookup jobs。
```

这正是 Flex-PTCL entry 想要优化的对象。

### Downstream Request Semantics

当 lookup group 完成且有 miss：

```text
issueReadyPTELookupGroup:
  1. compute downstreamBitmap
  2. call sendDownstream(now, req, downstreamBitmap)
  3. update MSHR issued bitmap
```

PTE mode：

```text
downstreamBitmap = missBitmap
```

PTCL mode：

```text
downstreamBitmap = ptclRepresentativeBitmap(group)
```

当前 PTCL downstream 语义是：PTCL lookup miss 后，不一定把所有 miss bits 都发下去；而是选择一个 representative bit 作为 downstream request。response 回来后，再用 page table 补齐 represented demand bits。

### Representative PTCL Response

PTCL representative response 回来后：

```text
completePTCLRepresentativeRsp:
  1. find representedBitmap for this PTCL
  2. intersect with real demand bitmap
  3. for each demanded bit:
       use pageTable.Find to get page
       install page into TLB
       update MSHR page/response bitmap
  4. schedule ready MSHR entry
```

这保持了 PTCL mode 对外只发送少量 downstream request 的语义，同时仍能返回上层真正 demand 的 PTE。

### Response Path

普通 downstream response：

```text
processRsp:
  1. install page into TLB set
  2. update MSHR page
  3. update ResponseBitMap
  4. if MSHR ready:
       scheduleReadyMSHREntry
```

ready 的条件：

```text
for every bit requested by UplevelBitMap:
    ResponseBitMap[bit] must be true
```

之后 `respondMSHREntry` 会逐个向上层 request 返回对应 PTE，并在所有 request 返回后移除 responding entry。

### Adaptive PTCL Mode Update

当前 mode switching 在 MSHR entry 完成时更新：

```text
scheduleReadyMSHREntry(..., updateMode=true)
  -> updateModeByMSHREntry
```

scoring 逻辑：

```text
scoreBits = number of real demand bits arriving within lookup latency window
totalBits = number of real demand bits in this MSHR entry
```

delta：

```text
if scoreBits <= 1:
    delta = -7
else if scoreBits >= 7:
    delta = +1
else:
    delta = -2
```

mode transition：

```text
if !ptclMode && counter >= high:
    switch to PTCL mode

if ptclMode && counter <= low:
    switch to PTE mode
```

### Current Limitations Relevant To Flex-PTCL

当前实现已经完成了：

```text
1. PTCL-aware MSHR coalescing
2. bitmap-based issued/response tracking
3. internal lookup latency modeling
4. PTCL representative downstream request
5. adaptive PTCL/PTE mode switching
```

但 TLB set 仍然是：

```text
PID + exact vAddr -> one page
```

因此 PTCL mode 下 lookup path 仍然需要：

```text
fullBitmap -> 8 pteLookupJobs -> 8 exact-vAddr set lookups
```

Flex-PTCL entry 应该主要替换或扩展：

```text
internal.Set
processReadyPTELookupJob
enqueuePTELookupJobsWithBitmap
installPage
```

目标是把 PTCL mode 中的 8 个 exact PTE lookup jobs 合并成：

```text
1 Flex entry lookup + bitmap hit/miss extraction
```

同时保留现有 MSHR bitmap 和 downstream semantics。

## Remaining Gap

当前 PTCL mode 主要优化了：

```text
1. MSHR coalescing
2. downstream request count
3. page-table walk pressure
```

但 L2 TLB entry lookup / placement 仍然接近 PTE-granular。

在当前实现中，即使一个 request 已经在 MSHR/downstream 层面以 PTCL bitmap 表示，L2 TLB 内部仍可能把这个 PTCL bitmap 展开成多个 PTE lookup jobs。也就是说：

```text
1 个 PTCL request
  -> 最多 8 个 internal PTE lookup/access procedures
```

这会造成一个不协调的状态：

```text
MSHR/downstream 已经 PTCL-aware
TLB entry access 仍然 PTE-oriented
```

因此 PTCL mode 的收益会被 entry lookup cost 抵消一部分。下一步优化的目标是让 TLB entry access 和 placement 也变成 PTCL-aware，同时保持对 PTE-mode workload 的 flexibility。

## Motivation

当前 PTCL mode 已经可以在 MSHR 和 downstream request 层面减少请求数，但 L2 TLB entry lookup 仍然偏 PTE-granular。一个 PTCL request 在内部可能需要检查 8 个 PTE slot，因此会消耗 8 次 access procedure。这会削弱 PTCL mode 的收益。

直接把所有 L2 TLB entry 都改成 PTCL-line entry 并不理想。对于 AES、SPMV 这类 sparse PTCL workload，一个 PTCL 里可能只用到 1 到 2 个 PTE。如果一个 entry 固定代表 8 个 PTE，就会浪费大量 sector，也可能降低 PTE-mode workload 的有效容量。

因此需要一个 unified entry format，让同一份 storage 可以根据 workload 行为在 PTE format 和 PTCL format 之间切换。

## High-Level Idea

使用一个 fused entry format：

```text
mode        // PTE_PACK or PTCL_LINE
ptclTag     // base VPN, used in PTCL_LINE mode
valid[8]
pteTag[8]   // exact VPN tags, used in PTE_PACK mode
page[8]
```

同一个 entry 有 8 个 data slots：

```text
PTE_PACK mode:
  8 个 slot 存 8 个独立 PTE。
  每个 slot 使用自己的 pteTag[i] 做 exact VPN match。

PTCL_LINE mode:
  8 个 slot 存同一个 PTCL cacheline 中的 8 个 PTE。
  entry 使用一个 ptclTag，valid[8] 表示每个 PTE 是否有效。
```

这样 TLB entry 不需要被固定成一种格式。PTE-friendly workload 保持 PTE_PACK，PTCL-friendly workload 使用 PTCL_LINE。

## Address Mapping

```text
vpn     = vAddr >> pageShift
baseVPN = vpn >> 3
bit     = vpn & 0x7
```

PTCL-line 的 base address：

```text
baseVAddr = (baseVPN << 3) << pageShift
```

## Lookup Semantics

### Single-PTE Lookup

对于普通 PTE request：

```text
vpn     = request VPN
baseVPN = vpn >> 3
bit     = vpn & 0x7
```

如果 entry 是 PTCL_LINE：

```text
hit = entry.ptclTag == baseVPN && entry.valid[bit]
```

如果 entry 是 PTE_PACK：

```text
hit = exists i:
        entry.valid[i] && entry.pteTag[i] == vpn
```

### PTCL Bitmap Lookup

对于 PTCL request，输入是一个 request bitmap：

```text
requestBitmap[8]
```

如果 entry 是 PTCL_LINE：

```text
if entry.ptclTag == baseVPN:
    hitBitmap = entry.valid & requestBitmap
```

如果 entry 是 PTE_PACK：

```text
hitBitmap = 0
for each slot i:
    if entry.valid[i] && (entry.pteTag[i] >> 3) == baseVPN:
        bit = entry.pteTag[i] & 0x7
        hitBitmap[bit] = true
hitBitmap &= requestBitmap
```

这样 PTCL request 即使遇到 PTE_PACK entry，也可以得到 partial hit。

## Fill Semantics

### PTE Fill

PTE-mode response 只填一个 bit：

```text
vpn     = response VPN
baseVPN = vpn >> 3
bit     = vpn & 0x7
```

填充策略：

```text
1. 如果已有 matching PTCL_LINE entry:
     fill page[bit], set valid[bit]

2. 否则，如果已有 PTE_PACK entry 还有空 slot:
     insert exact VPN into one free slot

3. 否则，allocate or evict one Flex entry in PTE_PACK mode
```

### PTCL Fill

PTCL-mode response 可能带一个 bitmap：

```text
responseBitmap[8]
```

填充策略：

```text
1. 如果已有 matching PTCL_LINE entry:
     fill all responseBitmap bits

2. 否则，allocate one Flex entry in PTCL_LINE mode:
     entry.ptclTag = baseVPN
     entry.valid |= responseBitmap
     entry.page[bit] = returned pages
```

## Promotion Policy

为了避免 sparse workload 被 PTCL_LINE 伤害，默认可以先使用 PTE_PACK。只有当某个 PTCL 显示出足够 locality 时，再 promote 成 PTCL_LINE。

一个简单 promotion 条件：

```text
same baseVPN observed bits >= promotionThreshold
```

例如：

```text
promotionThreshold = 3
```

Promotion 流程：

```text
1. 在 PTE_PACK entries 中扫描 same baseVPN 的 slots。
2. 如果 collected bits >= threshold:
     allocate one PTCL_LINE entry
     move these PTEs into corresponding page[bit]
     invalidate old PTE_PACK slots
```

这样：

```text
Sparse workload:
  很少触发 promotion，保持 PTE_PACK。

Dense/PTCL-friendly workload:
  快速触发 promotion，之后 PTCL lookup 只需要一次 entry access。
```

## Demotion Policy

如果 PTCL_LINE entry 长期只命中少量 bit，可以 demote 回 PTE_PACK。

可选规则：

```text
if usefulBits <= demotionThreshold over a window:
    demote PTCL_LINE to PTE_PACK slots
```

第一版可以不实现 demotion，只实现 promotion，减少复杂度。

## Capacity Fairness

不能让 fused entry 免费把容量放大 8 倍。

如果原始 L2 TLB 有：

```text
numSets * numWays PTE entries
```

那么 Flex-PTCL TLB 应该满足：

```text
numSets * flexWays * 8 ~= 原始 PTE entry 数量
```

也就是说：

```text
flexWays = ceil(numWays / 8)
```

或者显式使用 storage budget 计算。这样 PTE_PACK mode 下总 PTE 容量与 baseline 接近，PTCL_LINE mode 的收益来自 tag sharing 和 access reduction，而不是免费扩容。

## Access Model

当前问题是 PTCL mode 下 1 个 translation request 可能消耗 8 个 lookup jobs。

使用 Flex-PTCL entry 后：

```text
PTE_PACK lookup:
  1 entry access, compare up to 8 exact VPN tags

PTCL_LINE lookup:
  1 entry access, compare 1 baseVPN tag, bitmap check
```

因此 PTCL mode 的 internal lookup cost 可以从：

```text
8 * pteLookupLatencyCycles
```

变成：

```text
1 * flexEntryLookupLatencyCycles
```

如果需要更细的模型，可以设：

```text
PTE_PACK lookup latency = base + small parallel tag compare cost
PTCL_LINE lookup latency = base + bitmap cost
```

第一版可以直接让两者都使用同一个 L2 TLB lookup latency。

## Interaction With PTCL Mode

PTCL mode 不一定强制使用 PTCL_LINE entry。更安全的策略是：

```text
PTE mode:
  prefer PTE_PACK fill

PTCL mode:
  if MSHR/request bitmap shows enough useful bits:
      fill/promote PTCL_LINE
  else:
      still allow PTE_PACK fill
```

也就是说 PTCL mode 控制 request coalescing 和 downstream behavior，而 Flex-PTCL entry 控制 storage placement。

## Suggested First Implementation

第一版可以只做最小可行版本：

```text
1. 新增 Flex entry set，不立刻删除旧 set。
2. 实现:
     LookupPTE(pid, vpn)
     LookupBitmap(pid, baseVPN, bitmap)
     FillPTE(page)
     FillBitmap(baseVPN, pages, bitmap)

3. PTE mode:
     use LookupPTE + FillPTE

4. PTCL mode:
     use LookupBitmap + FillBitmap

5. 暂时不做 demotion。
6. promotionThreshold 作为配置参数，默认 3。
```

## Metrics To Add

建议增加这些指标，便于验证机制：

```text
flex_pte_pack_entries
flex_ptcl_line_entries
flex_promotions
flex_demotions
flex_pte_pack_hits
flex_ptcl_line_hits
flex_partial_ptcl_hits
flex_ptcl_lookup_saved_jobs
flex_invalidated_pte_slots_on_promotion
```

其中最重要的是：

```text
flex_ptcl_lookup_saved_jobs
```

它可以直接量化：

```text
原本需要 8 个 PTE lookup jobs，现在只需要 1 个 Flex lookup。
```

## Risks

1. PTCL_LINE 可能浪费 capacity。
   需要 promotion threshold 或 adaptive placement 避免 sparse workload 受伤。

2. PTE_PACK lookup 需要多个 parallel tag compare。
   需要在模型中说明这是一个 small associative compare，不是串行 8 次 lookup。

3. Promotion 可能带来额外复杂度。
   第一版可以只在 fill path 上做简单 promotion，不做 aggressive migration。

4. 容量公平性必须明确。
   如果不调整 numWays，性能可能被质疑来自容量扩大。

## Summary

Flex-PTCL entry 的核心是：

```text
同一份 storage，两种解释方式。
PTE sparse 时像普通 PTE TLB。
PTCL dense 时像 sectorized PTCL-line TLB。
切换依赖 mode bit、bitmap、简单 tag 逻辑。
```

这可以在不牺牲 PTE-mode flexibility 的情况下，降低 PTCL mode 的 internal lookup access cost。
