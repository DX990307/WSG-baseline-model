# MMUTLB Prefetcher Trace Summary

## Version

- Version: `v0001`
- Status: current archived summary
- Scope: `400latency` `relu` timing run

## Scope

This note summarizes the current observed behavior of the `mmuTLB` prefetcher in the `400latency` `relu` timing run.

Run shape used in recent checks:

```bash
./400latency -timing -max-wg=38400 -benchmark=relu -num-memory-banks=16 -bandwidth=48 -switch-latency=32 -magic-memory-copy -report-all
```

The prefetcher currently lives inside `MMUTLB` and:

- learns BO-local access patterns
- issues translation prefetches to `MMU`
- pushes prefetched fills to GPU-side TLBs instead of installing them into the local `IOTLB`

## What We Confirmed

### 1. The duplicate push bug is fixed

There used to be a bug where the first page of a prefetched 8-page cacheline could be pushed twice to the target GPU TLB.

That bug is fixed now.

Evidence from the recent trace:

- `issue = 67`
- `push = 536`
- `536 = 67 * 8`

This means each issued prefetch now forwards exactly 8 pages, which matches the intended cacheline granularity.

## Stable behavior pattern before the latest priority experiment

In the healthier recent run before trying `inter-first` priority, the observed pattern was:

- `demand = 951`
- `issue = 67`
- `wait = 10`
- `push = 536`
- `inter learned = 2`
- `inter issued = 0`
- all issued prefetches were `intra`

Performance in that run:

- `Driver total_time = 19.531 us`

Interpretation:

- The prefetcher was conservative.
- The system benefited mostly from pushing prefetched fills to GPU TLBs and avoiding `IOTLB` pollution.
- `inter` was being learned but almost never became a real issued prefetch.

## Why `inter` was not issuing in that phase

Detailed tracing showed that `inter` was mostly blocked after learning, not before learning.

Main blocking causes in that phase:

- `quota`
- `frontier`
- `offset unavailable`

This means:

- `inter` was often generated
- but it lost to `intra` because `MaxPrefetchPerDemand=1`
- or it was filtered because the same `(page_block, cacheline)` had already been issued before

So the bottleneck was not "cannot learn inter", but "learned inter does not survive runtime filtering and quota".

## Latest experiment: make `inter` higher priority than `intra`

We then changed candidate order so that `inter` gets considered before `intra` under the same one-prefetch budget.

This successfully made `inter` issue for real, but overall performance got worse.

Observed behavior in that run:

- `demand = 676`
- `issue = 489`
- `reason=inter = 47`
- `reason=intra = 442`
- `wait = 43`
- `push = 3488`
- `3488 = 489 * 8`

Performance in that run:

- `Driver total_time = 21.357 us`

## Why the `inter-first` experiment got slower

The key problem is not that `inter` is wrong. The problem is that globally prioritizing `inter` causes too much traffic amplification.

The trace shows:

- many more total prefetch issues
- many more forwarded pages
- only a small fraction of issues are actually `inter`

The strongest signal from the trace is the `inter-drop` breakdown:

- `frontier = 1548`
- `quota = 132`
- `offset = 83`
- `eligibility = 9`

The corresponding reasons are:

- `already-issued = 1548`
- `max-prefetch-per-demand = 132`
- `current-pattern-offset-unavailable = 83`
- `resident = 5`
- `mshr = 4`

Interpretation:

1. Many `inter` candidates are repetitive.
   They are repeatedly rediscovered and then filtered by the BO-local frontier.

2. Giving `inter` first priority changes which prefetch gets the single slot.
   This can displace short-range `intra` prefetches that were previously more useful.

3. Once more misses survive to runtime, the prefetch machinery runs more often.
   That creates a feedback loop with higher `issue` count and higher MMU-side traffic.

In short:

- `inter-first` does make `inter` visible
- but it also destabilizes the previous low-traffic regime
- so total runtime becomes worse

## Current pattern summary

At this point the overall pattern is:

- learner side is functioning
- duplicate prefetched page push is fixed
- pushing prefetched fills to GPU TLBs is beneficial
- `inter` learning exists but runtime policy is still the weak point
- pure `inter-first` priority is too aggressive
- the best recent runtime so far came from the more conservative policy, not the aggressive one

## Practical conclusion

The next tuning direction should not be:

- "always prioritize inter over intra"

The next tuning direction should be something narrower, for example:

- keep `intra` as the default first choice
- allow `inter` to take the slot only under specific conditions
- avoid re-attempting `inter` candidates that are already repeatedly blocked by frontier
- consider restricting `inter` to more trustworthy current-window situations

## Files most relevant to this behavior

- `akita/mem/vm/mmuTLB/prefetcher_runtime.go`
- `akita/mem/vm/mmuTLB/tlb.go`
- `akkalat/400latency/test.txt`
- `akkalat/400latency/metrics.csv`
