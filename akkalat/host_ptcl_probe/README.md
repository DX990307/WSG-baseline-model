# Host MMU 8-PTE/PTCL Probe

This directory contains a host-only experiment for checking whether this server
shows an observable 8-PTE/PTCL granularity effect. It does not use the simulator.

## Build

```bash
cd hyperScan/akkalat/host_ptcl_probe
make
```

## Timing Evidence

```bash
./run_timing.sh
```

The timing run sweeps `d=1..16` for page pairs `A` and `B=A+d*4096`, with
`A` aligned so `VPN(A) % 8 == 0`. Each trial evicts the TLB, measures `A`,
measures the first `B` access after `A`, immediately measures `B` again as a
hit lower bound, then measures cold `B` without a preceding `A`.

The CSV columns are:

```text
mode,d,trials,a_median,b1_after_a_median,b2_hit_median,cold_b_median,...
```

Interpretation:

- `d=1..7` close to `B2_hit`: possible sibling TLB fill.
- `d=1..7` faster than `d>=8` but slower than `B2_hit`: likely page-walk or
  PTE-cacheline locality.
- no difference across `d`: no visible 8-PTE/PTCL effect.

## Perf Evidence

Install a kernel-matched perf first:

```bash
./setup_perf.sh
```

Then run:

```bash
./run_perf.sh
```

The perf run uses an interleaved A/B workload over a large shuffled page set.
This avoids explicit TLB-eviction pointer chasing during the measured perf
region, so dTLB counters are less dominated by the eviction mechanism.
The script uses `perf stat -D` plus `--start-delay-ms` so page-table setup is
mostly excluded from the counted region.

Default modes:

```text
same_ptcl_d1
same_ptcl_d7
next_ptcl_d8
far_random
hit_only
vpn8_same
vpn8_stride8
vpn8_random
```

The `vpn8_*` modes test the stricter "access eight VPNs" case:

- `vpn8_same`: `VPN+0..VPN+7`, one aligned 8-PTE/PTCL group.
- `vpn8_stride8`: eight pages separated by 8 VPNs, crossing PTCL groups.
- `vpn8_random`: eight unrelated VPNs.

Default generic events:

```text
cycles,instructions,dTLB-loads,dTLB-load-misses,cache-references,cache-misses
```

You can override them after checking local AMD events:

```bash
perf list | rg -i "dtlb|tlb|walk|page"
EVENTS="cycles,instructions,dTLB-loads,dTLB-load-misses,<extra-events>" ./run_perf.sh
```

## PTE Cacheline Evidence

If the claim is specifically that the CPU page walker reads a final-level PTE
cacheline containing eight adjacent PTEs, use:

```bash
./run_pte_cacheline.sh
```

This focuses on the `d=1..7` versus `d=8` boundary:

- 4KB pages use 8-byte PTEs.
- A 64B cacheline therefore contains 8 adjacent PTEs.
- `A` and `A+d*4096` share the same final-level PTE cacheline for `d=1..7`
  when `VPN(A) % 8 == 0`.
- `d=8` is the next final-level PTE cacheline while still sharing upper-level
  page-table entries.

Evidence for final-PTE-cacheline behavior is a boundary at `d=8`: `d=1..7`
should have lower B-after-A latency or fewer page-walk cache misses than `d=8`.
Do not use reduced dTLB misses as the primary criterion here; reduced dTLB
misses would instead suggest sibling TLB fill.

## Distance Boundary Search

To find where A-to-B page-walk locality disappears, run:

```bash
./run_distance_boundary.sh
```

This tests selected distances instead of every `d`:

```text
1,2,3,4,5,6,7,8,9,16,32,64,128,256,511,512,513,1024,2048,4096,8192
```

It produces both warm-cache and cold-cache sweeps. The cold-cache sweep scans a
large buffer before each timed miss:

```bash
CACHE_EVICT_MB=64 ./run_distance_boundary.sh
```

If the relevant locality is final-level PTE cacheline locality, the most
important boundary is `d=8`. If there is no `d=8` step but a step near `d=512`,
the effect is more likely page-table-page or upper-level page-walk locality.

## PTW Capacity Search

To test whether a latency knee is caused by the number of concurrent page table
walkers, rather than by an address-distance boundary, run:

```bash
./run_ptw_capacity.sh
```

This mode fixes the address pattern to independent pages and sweeps the number
of TLB-missing loads issued in one timed batch:

```text
K=1,2,4,8,12,16,20,24,32,48,64,96,128
```

The default page stride is 512 pages, so adjacent batch entries are separated by
2 MiB and should not benefit from final-level PTE cacheline locality. The CSV
columns are:

```text
mode,k,trials,stride_pages,total_median,cycles_per_load_median,...
```

If the host has roughly 16 effective concurrent PTW slots, the expected evidence
is a knee near `K=16`: batch total latency should grow more slowly before 16 and
more steeply after 16, while cycles/load should stop improving around 16. If the
knee stays tied to VPN distance `d=16` but not to batch size `K=16`, the earlier
distance-boundary result is more likely an address-locality or prefetch effect.

Plot a result directory with:

```bash
python3 plot_ptw_capacity.py --result-dir results/<run-dir>
```

## Notes

- The probe uses `MADV_NOHUGEPAGE`; if results look suspicious, inspect
  `/proc/<pid>/smaps` in a longer run to confirm THP is not used.
- The conclusion should be phrased as host hardware evidence for or against
  8-PTE granularity. Timing alone should not be overstated as proof that the
  hardware literally returns eight TLB entries on one miss.
