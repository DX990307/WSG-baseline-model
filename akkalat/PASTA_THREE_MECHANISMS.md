# PASTA Mechanism Summary

This note summarizes the three mechanisms currently implemented around the
GMMU L2 TLB path. It is intended as a starting point for the paper write-up,
so it describes the design intent, the runtime behavior, the ablation
configuration, and the most important metrics.

## Scope

The current implementation has three separable mechanisms:

```text
1. PTCL-aware MSHR and downstream coalescing
2. Flex TLB entry placement and bitmap lookup
3. Stride-aware translation prefetching
```

The cleanest way to think about the design is:

```text
PTCL-aware MSHR:
  reduces MSHR pressure and downstream translation traffic.

Flex entry:
  reduces internal L2 TLB lookup/access work when PTCL bitmap requests exist.

Prefetcher:
  predicts future translation requests and fills the L2 TLB ahead of demand.
```

The current `runall2.py` ablation mapping is:

```text
baseline:
  per-VPN MSHR baseline, no PTCL mode, no Flex entry, no GMMU prefetch.

flex_entry:
  baseline MSHR/downstream semantics + Flex TLB entry.
  This is pure entry-only in the current script.

ptcl_mode:
  PTCL-aware MSHR/downstream coalescing, no Flex entry, no prefetch.

gmmu_prefetch:
  baseline MSHR/downstream semantics + GMMU prefetcher.

pasta:
  PTCL-aware MSHR/downstream + Flex entry + GMMU prefetcher.
```

Important: `flex_entry` by itself does not enable PTCL mode. Therefore it
mainly exercises PTE_PACK behavior. PTCL_LINE lookup compression becomes most
visible when Flex entry is combined with PTCL mode, as in `pasta` or in a
future dedicated `ptcl_flex_entry` config.

## Background

GPU address translation overhead comes from several places:

```text
1. L1/L2 TLB lookup latency
2. L2 TLB MSHR pressure
3. downstream page-table walk traffic
4. IOMMU or remote translation traffic
```

A page table cacheline can cover multiple adjacent PTEs. In this codebase, the
PTCL granularity is modeled as 8 PTEs:

```text
baseVAddr = align_down(vAddr, 8 * pageSize)
bit       = (vAddr / pageSize) & 0x7
bitmap[8] = requested PTEs within the PTCL
```

This creates an opportunity: if several translation requests target the same
PTCL, the L2 TLB can merge their miss state and reduce downstream page walks.
The remaining issue is that internal TLB entry lookup can still behave like
eight independent PTE lookups. The Flex entry mechanism addresses this second
level of overhead.

## Mechanism 1: PTCL-Aware MSHR And Downstream Coalescing

### Goal

The first mechanism reduces MSHR occupancy and downstream translation traffic
by tracking misses at PTCL granularity rather than exact PTE granularity.

In the baseline mode, each missed VPN uses its own MSHR entry and generally
generates its own downstream request:

```text
miss VPN A -> MSHR entry A -> downstream request A
miss VPN B -> MSHR entry B -> downstream request B
```

In PTCL-aware mode, multiple misses in the same PTCL share one MSHR entry:

```text
miss VPN 18 -> base VPN 16, bit 2
miss VPN 19 -> base VPN 16, bit 3
miss VPN 21 -> base VPN 16, bit 5

one MSHR entry:
  baseVPN = 16
  bitmap  = 00101100
```

### MSHR Entry Format

The GMMU TLB MSHR entry is bitmap-aware:

```text
mshrEntry:
  pid
  baseVAddr
  UplevelBitMap[8]
  IssuedBitMap[8]
  ResponseBitMap[8]
  Requests[]
  Pages[8]
  RealAddrBitmap[8]
  RealAddrTime[8]
```

The main bitmaps are:

```text
UplevelBitMap:
  demand bits that must eventually be returned to upper-level TLBs.

IssuedBitMap:
  bits that have already been sent into internal lookup or downstream
  translation.

ResponseBitMap:
  bits for which translation results have returned.

RealAddrBitmap / RealAddrTime:
  observed real demand bits and their arrival times, used by the adaptive
  PTCL/PTE mode policy.
```

### Request Flow

For a demand translation request:

```text
1. Normalize the request bitmap.
2. Search MSHR using either exact VPN or PTCL base address.
3. If an MSHR entry exists:
     - update UplevelBitMap
     - append the original request
     - enqueue lookup work only for bits not already issued
4. If no entry exists:
     - allocate a new MSHR entry
     - record the original request
     - enqueue lookup work
5. When lookup misses remain:
     - send downstream translation request according to current mode
```

MSHR keying depends on mode:

```text
baseline / per-VPN mode:
  key = exact page-aligned VPN

PTCL-aware mode:
  key = PTCL baseVAddr
```

### Downstream Semantics

After internal TLB lookup finishes, miss bits are sent downstream.

In PTE mode:

```text
downstreamBitmap = missBitmap
```

In PTCL mode:

```text
downstreamBitmap = representative bitmap
```

The representative request is one selected bit from the PTCL miss bitmap. When
the response comes back, the current MSHR entry decides which bits are actually
needed by checking `UplevelBitMap`. This avoids returning or installing
unrequested PTEs while still using one downstream translation to represent a
PTCL group.

### Adaptive PTCL/PTE Mode

The GMMU L2 TLB can switch between PTE mode and PTCL mode. The switching policy
uses a bounded coalescing counter:

```text
counter >= high threshold -> switch to PTCL mode
counter <= low threshold  -> switch back to PTE mode
```

The current default thresholds are:

```text
low  = 2
high = 6
```

The current adaptive delta is intentionally moderate:

```text
scoreBits <= 1  -> -2
scoreBits <= 3  -> -1
scoreBits <= 5  -> +1
scoreBits >= 6  -> +2
```

`scoreBits` is computed from the number of real demand bits that arrive within
a short window after the first demand in the MSHR entry. This prevents very
late coalesced bits from being counted as strong evidence for PTCL mode.

### Main Benefit

This mechanism targets:

```text
1. fewer MSHR entries per PTCL
2. fewer downstream translation requests
3. lower page-table walk pressure
```

The key metrics are:

```text
ptcl_mode_enabled
ptcl_coalescing_counter
ptcl_switch_to_ptcl
ptcl_switch_to_pte
pte_mode_completions
ptcl_mode_completions
downstream_req_count
local_req_count
iommu_req_count
```

## Mechanism 2: Flex TLB Entry Placement And Bitmap Lookup

### Goal

The second mechanism attacks a different cost. PTCL-aware MSHR coalescing can
reduce downstream traffic, but internal L2 TLB lookup may still expand one PTCL
bitmap request into several PTE lookup jobs:

```text
PTCL bitmap request:
  bitmap = 11111111

legacy internal lookup:
  enqueue up to 8 exact-PTE lookup jobs
```

Flex entry makes the L2 TLB storage and lookup path aware of PTCL locality:

```text
PTCL bitmap request:
  bitmap = 11111111

Flex lookup:
  enqueue 1 bitmap-aware lookup job
  return hitBitmap and missBitmap
```

### Entry Format

Each Flex entry can be interpreted in one of two modes:

```text
PTE_PACK:
  8 slots store 8 independent PTEs.
  Each slot has its own PTCL tag and bit index.

PTCL_LINE:
  8 slots store sectors from one PTCL.
  One base tag identifies the PTCL.
  valid[8] identifies which PTE sectors are resident.
```

Conceptually:

```text
flexEntry:
  mode
  valid[8]
  tags[8]
  bits[8]
  pages[8]
  replacement state
```

For `PTCL_LINE`, `tags[0]` stores the PTCL base tag. For `PTE_PACK`, each slot
stores its own tag and bit.

### Capacity Accounting

To avoid giving Flex entry a free capacity advantage, the number of Flex ways
is derived from the legacy way count:

```text
flexWays = max(1, legacyWays / 8)
```

Each Flex entry has 8 data slots, so the PTE slot budget is approximately:

```text
numSets * flexWays * 8
```

This keeps Flex storage comparable to the original PTE-entry capacity.

### Lookup Semantics

For a single PTE lookup:

```text
1. Compute baseVAddr and bit.
2. Search matching PTCL_LINE entry first:
     hit if ptclTag matches and valid[bit] is true.
3. Search PTE_PACK entries:
     hit if any slot has matching tag and bit.
```

For a PTCL bitmap lookup:

```text
1. Compute baseVAddr.
2. If a PTCL_LINE entry matches:
     hitBitmap = valid & requestBitmap
3. Also search PTE_PACK slots for partial hits.
4. Return:
     hitBitmap
     missBitmap = requestBitmap - hitBitmap
     pages[8]
```

This means Flex entry supports partial hits. Even if only a few bits are
resident, the lookup can still return those bits and send only the remaining
misses downstream.

### Fill Semantics

For a single-PTE response:

```text
1. If matching PTCL_LINE exists:
     fill the corresponding sector.
2. Else insert/update a PTE_PACK slot.
```

For a bitmap/PTCL response:

```text
1. If matching PTCL_LINE exists:
     fill all valid response bits.
2. Else if response has enough bits:
     allocate/promote a PTCL_LINE entry.
3. Else:
     fill the bits as PTE_PACK slots.
```

The current promotion threshold is:

```text
flexPromotionThreshold = 3
```

That means a bitmap fill with at least 3 valid bits can create a PTCL_LINE
entry.

Duplicate cleanup is performed so that the same PTE is not stored in both a
PTCL_LINE sector and a PTE_PACK slot.

### Interaction With PTCL Mode

Flex entry is independent of PTCL mode in the configuration system.

Current configs:

```text
flex_entry:
  baseline MSHR/downstream + Flex entry.
  This mostly tests PTE_PACK placement and replacement.

pasta:
  PTCL mode + Flex entry + prefetch.
  This can exercise bitmap lookup compression and PTCL_LINE placement.
```

Therefore, if the goal is to measure the pure incremental benefit of Flex entry
inside PTCL mode, the cleanest future ablation should be:

```text
ptcl_flex_entry:
  PTCL mode + Flex entry, no prefetch.

entry-only PTCL improvement:
  ptcl_flex_entry / ptcl_mode
```

Without PTCL bitmap requests, Flex entry cannot fully demonstrate the
`8 lookup jobs -> 1 lookup job` benefit.

### Main Benefit

This mechanism targets:

```text
1. fewer internal lookup jobs for PTCL bitmap requests
2. fewer lookup latency cycles
3. PTCL-line placement when locality is dense
4. PTE_PACK behavior when locality is sparse
```

The key metrics are:

```text
flex_tlb_enabled
flex_promotion_threshold
flex_pte_pack_entries
flex_ptcl_line_entries
flex_lookup_jobs
flex_lookup_requested_bits
flex_lookup_hit_bits
flex_lookup_miss_bits
flex_lookup_saved_jobs
flex_pte_pack_hits
flex_ptcl_line_hits
flex_partial_ptcl_hits
flex_full_ptcl_hits
flex_promotions
flex_invalidated_pte_pack_slots
flex_evicted_valid_slots_for_ptcl_line
```

`flex_lookup_saved_jobs` is the most direct metric for the entry lookup benefit:

```text
saved = requested_bits - flex_lookup_jobs
```

## Mechanism 3: Stride-Aware Translation Prefetching

### Goal

The third mechanism tries to fill future translations before demand requests
arrive. It is implemented in the GMMU L2 TLB and uses observed PTCL access
patterns across GPMs and within each GPM.

The prefetcher is intentionally conservative. Demand translation has priority;
prefetches are admitted only after a pattern is confirmed and are filtered by
duplicate, resident, invalid-target, and feedback checks.

### Pattern Learning

The prefetcher groups observations by page block. Each observation records:

```text
GPM ID
PTCL ID
```

For each page block, a learner records the set of PTCLs seen by each GPM:

```text
perGPMPTCLs[gpmID] = {ptclID...}
```

The learner tries to confirm either:

```text
1. clear 3x3 inter-GPM and intra-GPM pattern
2. clear intra-GPM-only stride pattern
3. no-clear pattern, which disables prefetching for that block
```

The strongest pattern uses three consecutive GPMs and the first three PTCLs
from each:

```text
GPM g:     p0, p1, p2
GPM g+1:   q0, q1, q2
GPM g+2:   r0, r1, r2
```

The learner confirms:

```text
intraStride = p1 - p0 = p2 - p1
            = q1 - q0 = q2 - q1
            = r1 - r0 = r2 - r1

interStride = q0 - p0 = r0 - q0
```

If this full table does not confirm, it can still confirm an intra-GPM stride
from one GPM row.

### Candidate Generation

When a demand request arrives, the learner predicts future PTCL IDs:

```text
current PTCL + k * intraStride
```

It can also infer corresponding PTCLs for other GPMs using the learned
inter-GPM stride.

Candidates are limited by:

```text
maxCandidatesPerReq
issue budget
lookahead distance
prefix filter
duplicate filter
resident filter
feedback gate
```

Current builder defaults:

```text
prefetchAdmission      = 3
prefetchMaxLearners    = 4
prefetchLookahead      = 64
prefetchMaxCandidates  = 4
```

### PTCL-Aware Prefetch Bitmap

The prefetch bitmap depends on the current mode:

```text
PTCL mode:
  prefetch full PTCL bitmap, then filter unmapped/resident bits.

PTE mode:
  prefetch only the corresponding single PTE bit.
```

This keeps sparse workloads from always fetching eight PTEs, while allowing
PTCL-friendly workloads to prefetch a whole PTCL line.

### Adaptive Lookahead

The prefetcher uses moving-average latency windows to decide how far ahead to
prefetch:

```text
demandLatencyWindow
localPrefetchLatencyWindow
remotePrefetchLatencyWindow
```

The window size is currently 16 samples. Default initial values are:

```text
demand latency = 500 cycles
local prefetch latency = 500 cycles
remote prefetch latency = 800 cycles
```

The lookahead formula is:

```text
pace    = learner stream interval or average demand translation latency
service = average local prefetch latency or average remote prefetch latency

lookahead = ceil(service / pace) + margin

if remote path:
  lookahead += 1
```

The result is clamped between the minimum lookahead and the configured maximum
lookahead.

The intuition is:

```text
if remote translation is slow, prefetch further ahead;
if local/demand translation is fast, prefetch further ahead;
if the stream itself is slow, prefetch less aggressively.
```

### Local PTW Priority And IOMMU Fallback

Demand requests have priority over prefetches.

When issuing a queued prefetch:

```text
1. Prefer local GMMU/PTW path if local PTW has free capacity.
2. If local PTW is full, the prefetch may fall back to IOMMU.
3. IOMMU fallback is allowed only after enough prefetch history exists and
   feedback shows useful prefetches.
```

The fallback gate avoids sending low-confidence prefetch traffic to the IOMMU
too early.

### Feedback Control

The prefetcher tracks usefulness per page block:

```text
completed fills
useful hits
late demands
lost-before-use entries
unused resident entries
```

The issue budget is reduced or disabled when feedback is bad:

```text
if completed < probe threshold:
  allow a small probe budget

if useful == 0 after enough completions:
  disable prefetching for the page block

if lost-before-use dominates useful hits:
  reduce or stop issuing

if useful is consistently high:
  allow more candidates per demand
```

Current constants:

```text
minPrefetchIssuesBeforeIOMMUFallback = 16
prefetchProbeCompletionThreshold = 4
prefetchZeroUsefulCompletionThreshold = 8
```

### Main Benefit

This mechanism targets:

```text
1. lower demand translation miss latency
2. fewer demand-triggered downstream requests
3. better use of idle local PTW capacity
4. optional remote/IOMMU assistance when local PTW is full
```

The key metrics are:

```text
prefetch_enabled
prefetch_generated_candidates
prefetch_enqueued_candidates
prefetch_issued_candidates
prefetch_completed_fills
prefetch_useful_hits
prefetch_late_demands
prefetch_lost_before_use
prefetch_redundant_fills
prefetch_unused_resident_entries
prefetch_demand_translation_latency_cycles
prefetch_local_latency_cycles
prefetch_remote_latency_cycles
prefetch_adaptive_lookahead
prefetch_blocked_by_no_free_ptw
prefetch_iommu_fallback_candidates
prefetch_disabled_blocks
prefetch_rejected_by_feedback
```

Useful prefetching should show:

```text
prefetch_useful_hits increases
prefetch_late_demands stays low
prefetch_lost_before_use stays low
downstream_req_count decreases or critical demand latency decreases
```

Bad prefetching often shows:

```text
many late demands
many dropped candidates
many duplicate rejections
extra lookup jobs or downstream traffic
little change in total time
```

## Ablation Interpretation

The current configs should be interpreted as:

```text
baseline:
  reference per-VPN translation behavior.

flex_entry:
  pure TLB entry storage/placement change, without PTCL MSHR/downstream mode.
  This is not the clean measurement of PTCL_LINE bitmap lookup benefit.

ptcl_mode:
  MSHR/downstream PTCL coalescing only.

gmmu_prefetch:
  prefetcher only on top of baseline-style demand handling.

pasta:
  combined design: PTCL MSHR/downstream + Flex entry + prefetcher.
```

Recommended additional config for the paper:

```text
ptcl_flex_entry:
  PTCL MSHR/downstream + Flex entry, no prefetcher.
```

Then the contribution can be decomposed cleanly:

```text
MSHR/downstream PTCL benefit:
  ptcl_mode / baseline

Flex entry benefit under PTCL:
  ptcl_flex_entry / ptcl_mode

Prefetch benefit on top of PTCL + Flex:
  pasta / ptcl_flex_entry

Full system benefit:
  pasta / baseline
```

## Suggested Paper Structure

One possible paper organization:

```text
1. Motivation and observations
   - many GPU translations share PTCL locality
   - per-PTE MSHR tracking causes pressure
   - downstream page walks are redundant
   - entry lookup remains PTE-oriented even after PTCL coalescing

2. PTCL-aware MSHR coalescing
   - bitmap MSHR entry
   - adaptive PTCL/PTE mode
   - representative downstream request

3. Flex-PTCL TLB entry
   - fused PTE_PACK/PTCL_LINE entry
   - bitmap lookup
   - promotion and duplicate cleanup
   - capacity accounting

4. Stride-aware translation prefetching
   - observation table
   - intra/inter stride
   - adaptive lookahead
   - local PTW first, IOMMU fallback second
   - feedback gating

5. Evaluation
   - baseline vs ptcl_mode vs ptcl_flex_entry vs pasta
   - per-workload speedups
   - geomean
   - downstream request reduction
   - lookup job reduction
   - prefetch usefulness and lateness
```

## Current Limitations And Notes

1. `flex_entry` is currently pure entry-only in `runall2.py`.
   It will not enable PTCL mode by itself.

2. The clean Flex-under-PTCL ablation should be added as `ptcl_flex_entry`.
   Without this, `pasta / ptcl_mode` mixes Flex entry and prefetcher.

3. Prefetch performance is workload-sensitive.
   Regular workloads can benefit, while sparse/irregular workloads can produce
   late or useless prefetches.

4. PTCL mode is adaptive.
   The current scoring is moderate:

```text
scoreBits <= 1  -> -2
scoreBits <= 3  -> -1
scoreBits <= 5  -> +1
scoreBits >= 6  -> +2
```

5. Flex entry capacity must be reported.
   The paper should report both baseline PTE slots and Flex effective PTE slot
   capacity to avoid ambiguity.

