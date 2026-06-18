# PTCL / CamSAT Ablation Notes

This note summarizes the current PTCL, prefetch, and CamSAT-related implementation in `hyperScan`.

## Current Goal

The current experiment studies whether adaptive PTCL-granularity translation can reduce page-table-walk pressure without relying too heavily on a translation prefetcher.

The working hypothesis is:

- PTCL mode is useful when nearby pages in the same 8-page PTCL are accessed soon after each other.
- The GMMU prefetcher can help, but it may add downstream traffic and can become late.
- A cleaner next direction is to make PTCL demand fills cheaper by reducing the serial access cost of returning multiple PTEs.

## Main Experiment Driver

The main script is:

```text
akkalat/runall2.py
```

The important default latency knobs are:

```text
DEFAULT_MMUTLB_PTCL_RETURN_LATENCY = 80
DEFAULT_GMMU_PTE_LOOKUP_LATENCY = 32
```

`runall2.py` passes these to the benchmark binary as:

```text
-mmutlb-ptcl-return-latency=<cycles>
-gmmu-pte-lookup-latency=<cycles>
```

The default adaptive PTCL thresholds are:

```text
DEFAULT_ADAPTIVE_LOW = 2
DEFAULT_ADAPTIVE_HIGH = 6
```

Adaptive configs start with GMMU PTCL mode off:

```text
-gmmu-initial-ptcl-mode=false
-gmmu-ptcl-threshold-low=<low>
-gmmu-ptcl-threshold-high=<high>
```

This means PTCL mode is not forced from the beginning. It is enabled only after the adaptive counter decides the workload has enough PTCL locality.

## Ablation Configs

`runall2.py` currently defines five PTCL/CamSAT configs.

### baseline

Flags:

```text
-gmmu-vpn-mshr-baseline
-mmutlb-vpn-mshr-baseline
-gmmu-initial-ptcl-mode=false
-gmmu-ptcl-threshold-low=0
-gmmu-ptcl-threshold-high=1000000
-mmutlb-demand-pte-only
```

Meaning:

- GMMU uses per-VPN MSHR behavior.
- MMUTLB/IOTLB uses per-VPN MSHR behavior.
- GMMU adaptive PTCL mode is effectively disabled.
- MMUTLB demand requests return only the requested PTE.

This is the non-PTCL baseline.

### ptcl_mode

Flags:

```text
-gmmu-initial-ptcl-mode=false
-gmmu-ptcl-threshold-low=<low>
-gmmu-ptcl-threshold-high=<high>
```

Meaning:

- GMMU can adaptively switch between PTE mode and PTCL mode.
- No GMMU prefetcher is enabled.
- MMU walk coalescing is not enabled by this config.

This is the main demand-driven PTCL-only configuration.

### prefetching

Flags:

```text
-gmmu-initial-ptcl-mode=false
-gmmu-ptcl-threshold-low=<low>
-gmmu-ptcl-threshold-high=<high>
-gmmu-prefetch
-gmmu-prefetch-admission=6
-gmmu-prefetch-max-learners=4
-gmmu-prefetch-lookahead=2
-gmmu-prefetch-max-candidates=4
```

Meaning:

- GMMU adaptive PTCL mode is enabled.
- GMMU translation prefetcher is enabled.
- MMU walk coalescing is not enabled.

### coalescing

Flags:

```text
-gmmu-vpn-mshr-baseline
-mmutlb-vpn-mshr-baseline
-gmmu-initial-ptcl-mode=false
-gmmu-ptcl-threshold-low=0
-gmmu-ptcl-threshold-high=1000000
-mmutlb-demand-pte-only
-mmu-walk-coalescing
```

Meaning:

- Same as baseline, but MMU page-walk coalescing is enabled.
- GMMU PTCL mode is disabled.
- GMMU prefetching is disabled.

Important: `-mmu-walk-coalescing` currently applies to the MMU engine, not to GMMUCache.

### camsat

Flags:

```text
-gmmu-initial-ptcl-mode=false
-gmmu-ptcl-threshold-low=<low>
-gmmu-ptcl-threshold-high=<high>
-mmu-walk-coalescing
-gmmu-prefetch
-gmmu-prefetch-admission=6
-gmmu-prefetch-max-learners=4
-gmmu-prefetch-lookahead=2
-gmmu-prefetch-max-candidates=4
```

Meaning:

- GMMU adaptive PTCL mode is enabled.
- MMU page-walk coalescing is enabled.
- GMMU translation prefetcher is enabled.

This is the full current CamSAT-style configuration.

## Current GMMUCache State

The main GMMUCache TLB implementation is:

```text
akita/mem/vm/tlb_gmmu/tlb copy 2.go
```

Current behavior:

- GMMU supports adaptive PTCL mode.
- In PTCL mode, a demand miss can issue a full 8-page PTCL bitmap downstream.
- In PTE mode, it issues only the requested PTE bitmap.
- GMMU PTE lookup delay is modeled with `pteLookupLatencyCycles`.
- GMMU prefetcher logic is still present, but it only runs when `-gmmu-prefetch` is enabled.
- GMMU walk coalescing is not currently connected.
- The previous experimental pending-response queue / GMMU walk-coalescing path has been removed from the active code.

The current response path is:

```text
OutsidePort or bottomPort Peek
  -> isPTEReady
  -> processRsp
  -> Retrieve
```

## Current MMUTLB State

The MMUTLB implementation is:

```text
akita/mem/vm/mmuTLB/tlb.go
```

The current PTCL return latency model is serial across requested PTE bits:

```go
lookupBits := tlb.bitmapCount(req.BitMap)
tlb.lookupReadyTimes[req.ID] = tlb.Freq.NCyclesLater(
    tlb.lookupLatencyCycles*lookupBits,
    now,
)
```

This means a full 8-PTE PTCL request pays:

```text
8 * mmutlb-ptcl-return-latency
```

With the default latency of 80 cycles, a full PTCL request costs 640 cycles.

This serial model can make PTCL mode look worse than it should if the intended design assumes parallel or burst PTCL access.

## Host 8-PTE / PTCL Probe

There is also a host-only microbenchmark for checking whether this server shows
an observable 8-PTE/PTCL granularity effect independent of the simulator:

```text
akkalat/host_ptcl_probe
```

It provides:

- `ptcl_probe.c`: independent C benchmark using 4KB anonymous pages and `MADV_NOHUGEPAGE`.
- `run_timing.sh`: timing sweep for `A` then `B=A+d*4096`, `d=1..16`.
- `run_perf.sh`: `perf stat` mode sweep for same-PTCL, cross-PTCL, far-random, and hit-only workloads.
- `setup_perf.sh`: installs kernel-matched perf tools and lowers `perf_event_paranoid`.

The timing result should be interpreted conservatively:

- `d=1..7` close to hit latency suggests possible sibling TLB fill.
- `d=1..7` faster than `d>=8`, but still slower than hit latency, suggests page-walk or PTE-cacheline locality.
- no `d=1..7` advantage means no clear host evidence for an 8-PTE/PTCL effect.

## Important Metrics

The most useful metrics to compare across configs are:

```text
total_time
ptcl_mode_enabled
ptcl_coalescing_counter
ptcl_switch_to_ptcl
ptcl_switch_to_pte
downstream_req_count
local_req_count
iommu_req_count
lookup_latency_cycles_per_pte
pte_lookup_delay_count
pte_lookup_delay_cycles
prefetch_exact_inserted
prefetch_exact_useful
prefetch_exact_useful_hit
prefetch_exact_late_useful
prefetch_exact_unused
```

For MMU walk coalescing, check the MMU engine metrics reported by the runner.

## Current Observation

In the recent `relu` data from:

```text
akkalat/results/2026-06-07-21-00-23-ptcl-prefetch-sweep
```

`ptcl_mode` was faster than `camsat`.

The likely reason is:

- `camsat` enabled GMMU prefetching.
- The prefetcher produced useful entries, but many were late.
- The prefetcher also increased downstream traffic and GMMU PTE lookup work.
- Since the extra prefetch work did not arrive early enough, it could hurt total time.

So the current data suggests that PTCL demand coalescing may be more robust than aggressive translation prefetching for some workloads.

## Matrix Multiplication Note

Typical LLM GEMM / matrix multiplication can be weakly page-walk sensitive because:

- It is compute-heavy.
- Accesses are often regular and reused.
- Once the working set is warmed, translation locality is high.
- Page walks can be hidden behind compute and memory-level parallelism.

To make matrix multiplication more page-walk sensitive, the benchmark needs more translation pressure, for example:

- larger working sets,
- lower data reuse,
- strided or scattered access,
- more concurrent streams touching different pages,
- fewer repeated accesses per page,
- smaller page size or more page-table levels if supported.

## Suggested Next Ablation

The next clean experiment is to drop the GMMU prefetcher and make PTCL access non-serial.

A proposed new config is:

```text
ptcl_parallel
```

Conceptually:

```text
adaptive GMMU PTCL mode
no GMMU prefetch
no GMMU walk coalescing
parallel or burst PTCL return latency in MMUTLB
```

Instead of:

```text
latency = lookupLatencyCycles * bitmapCount
```

try:

```text
latency = baseLatency + ceil(bitmapCount / parallelWidth) * laneLatency
```

Examples:

```text
parallelWidth=1: serial, same as current
parallelWidth=4: full PTCL costs 2 latency units
parallelWidth=8: full PTCL costs 1 latency unit
```

Another option is critical-word-first:

```text
demand PTE returns after one latency unit
remaining PTCL PTEs fill in the background
```

This would preserve the benefit of PTCL locality while avoiding the demand latency penalty of serially returning all 8 PTEs.

## Useful Commands

Small sampled run:

```bash
python3 runall2.py \
  --configs baseline,ptcl_mode,camsat \
  --extra-benchmark-flags "-sampled -branch-sampled -kernel-sampled -loop-sampled -sampled-warmup=512 -sampled-granularity=512" \
  --min-free-ram-gb 60 \
  --memory-scan-interval-minutes 30 \
  --max-workloads 16
```

Adaptive threshold scan:

```bash
python3 runall2.py \
  --adaptive-threshold-scan \
  --adaptive-threshold-pairs "0:2,1:4,2:6,2:8,4:12,4:16,8:24"
```

Build checks:

```bash
cd akita
GOCACHE=/tmp/gocache go build -buildvcs=false ./mem/vm/tlb_gmmu

cd ../akkalat/400latency
GOCACHE=/tmp/gocache go build -buildvcs=false

cd ..
GOCACHE=/tmp/gocache go test -buildvcs=false ./400latency
```

## Current Status

The active code has been restored to a stable state:

- GMMU adaptive PTCL mode is active when selected by config.
- GMMU prefetcher is optional and controlled by `-gmmu-prefetch`.
- MMU walk coalescing is only connected to the MMU engine.
- GMMU walk coalescing and the pending-response queue experiment are not active.
- Current build/test checks passed after the restore.
