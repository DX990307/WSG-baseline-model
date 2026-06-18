# MMUTLB Prefetcher Trace Summary

## Version

- Version: `v0002`
- Status: current archived summary
- Scope: `400latency` `relu` timing run
- Position: current best-so-far observed runtime

## What changed relative to `v0001`

This version records the behavior after tightening the runtime scope of prefetching:

- pattern-based prefetching is only allowed when the current GPU belongs to the learned window
- `inter` is only considered in the narrow `offset=0` case
- the general `inter-first` policy is no longer used
- the runtime falls back to a very conservative `intra` path

This was done to stop window-local patterns from being incorrectly reused by GPUs outside the learned window.

## Run shape

```bash
./400latency -timing -max-wg=38400 -benchmark=relu -num-memory-banks=16 -bandwidth=48 -switch-latency=32 -magic-memory-copy -report-all
```

## Main result

Performance in this run:

- `Driver total_time = 19.263 us`

This is better than the previous important reference points:

- better than the earlier conservative run at `19.531 us`
- better than the previous stronger result around `19.498 us`
- much better than the aggressive `inter-first` run at `21.357 us`

## Observed runtime pattern

From the current trace:

- `demand = 1000`
- `issue = 6`
- `push = 48`
- `wait = 0`
- `inter learned = 2`
- `inter issued = 0`
- all issued prefetches are `intra`

From the current prefetcher summary line:

- `issue_passes = 983`
- `no_candidates = 977`
- `candidates = 12`
- `issued = 6`
- `inter_candidates = 0`
- `inter_issued = 0`
- `resident_miss_invalidation = 2`

This means the runtime is now extremely selective:

- the learner is still active
- most demand observations do not produce any candidate
- only a very small number of high-confidence `intra` prefetches survive to issue

## Why this version performs better

The main improvement is not from issuing more prefetches. It is from refusing to issue low-confidence ones.

The most important correction is scope control:

- GPUs outside the learned window no longer reuse the same BO-local pattern
- this removes a large amount of cross-GPU noise
- the runtime no longer amplifies traffic by repeatedly trying low-value candidates

The trace supports this directly:

- `inter-drop` is entirely dominated by `scope`
- count observed: `97`

Interpretation:

- the old runtime was often trying to apply a valid local pattern too broadly
- the tighter scope check prevents that
- once this noise is removed, the prefetcher becomes much cheaper
- the remaining small number of `intra` prefetches is enough to help without destabilizing the system

## Practical conclusion

At the moment, this is the healthiest operating point seen so far.

The current pattern is:

- learner side still works
- duplicate push bug remains fixed
- pushing fills to GPU TLBs remains beneficial
- aggressive `inter` runtime policies are still risky
- strong scope restriction is clearly beneficial

So the current takeaway is:

- "less but more trustworthy" is better than "more prefetching"

## Recommended next step

This version should be treated as the current baseline before any further tuning.

If future work continues from here, the next change should be narrow and local, for example:

- only try limited `inter` when the access is especially trustworthy
- avoid reopening broad `inter-first` behavior
- preserve the current window membership guard

## Files most relevant to this version

- `akita/mem/vm/mmuTLB/prefetcher_runtime.go`
- `akita/mem/vm/mmuTLB/prefetcher_unit.go`
- `akita/mem/vm/mmuTLB/tlb.go`
- `akkalat/400latency/test.txt`
- `akkalat/400latency/metrics.csv`
