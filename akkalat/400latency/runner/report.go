package runner

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/sarchlab/akita/v3/mem/vm/translationtrace"
	"github.com/sarchlab/mgpusim/v3/timing/cu"
)

func (r *Runner) reportStats() {
	r.reportExecutionTime()
	r.reportInstCount()
	r.reportCPIStack()
	r.reportCacheLatency()
	r.reportRDMALatency()
	r.reportGMMULatency()
	r.reportMMULatency()
	r.reportCacheHitRate()
	r.reportTLBHitRate()
	r.reportGMMUCacheHitRate()
	r.reportTLBLatency()
	r.reportGMMUCacheLatency()
	r.reportRDMATransactionCount()
	r.reportGMMUTransactionCount()
	r.reportMMUTransactionCount()
	r.reportDRAMTransactionCount()
	r.reportIOMMUTLBStats()
	r.reportMMUCoalescingStats()
	// r.reportGMMUCounts()
	// r.reportGMMUCacheCounts()
	r.dumpMetrics()
	if err := translationtrace.Dump(); err != nil {
		panic(err)
	}
}

func (r *Runner) reportInstCount() {
	// kernelTime := float64(r.kernelTimeCounter.BusyTime())
	for _, t := range r.instCountTracers {
		// kernelTime := float64(r.kernelTimeCounter.BusyTime())
		// float64(r.kernelTimeCounter.BusyTime())
		cuName := t.cu.Name()
		gpuID := regexp.MustCompile(`GPU\[(\d+)\]`)
		match := gpuID.FindStringSubmatch(cuName)
		num, err := strconv.Atoi(match[1])
		if err != nil {
			return
		}
		if num > 23 {
			num = num - 1
		}
		kernelTime := float64(r.perGPUKernelTimeCounter[num].BusyTime())

		cuFreq := float64(t.cu.(*cu.ComputeUnit).Freq)
		numCycle := kernelTime * cuFreq

		r.metricsCollector.Collect(
			t.cu.Name(), "cu_inst_count", float64(t.tracer.count))

		r.metricsCollector.Collect(
			t.cu.Name(), "cu_CPI", numCycle/float64(t.tracer.count))
	}
}

func (r *Runner) reportCPIStack() {
	for _, t := range r.cuCPITraces {
		cu := t.cu
		hook := t.tracer

		r.reportCPIStackEntries(hook, cu, false)
		// r.reportCPIStackEntries(hook, cu, true)
	}
}

func (r *Runner) reportCPIStackEntries(
	hook *cu.CPIStackTracer,
	cu TraceableComponent,
	simdStack bool,
) {
	cpiStack := hook.GetCPIStack()
	if simdStack {
		cpiStack = hook.GetSIMDCPIStack()
	}

	keys := make([]string, 0, len(cpiStack))
	for k := range cpiStack {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	stackTypeName := "CPIStack"
	if simdStack {
		stackTypeName = "SIMDCPIStack"
	}

	for _, name := range keys {
		value := cpiStack[name]
		r.metricsCollector.Collect(cu.Name(), stackTypeName+"."+name, value)
	}
}

func (r *Runner) reportExecutionTime() {
	if r.Timing {
		r.metricsCollector.Collect(
			r.platform.Driver.Name(),
			"kernel_time", float64(r.kernelTimeCounter.BusyTime()))
		r.metricsCollector.Collect(
			r.platform.Driver.Name(),
			"total_time", float64(r.platform.Engine.CurrentTime()))

		for i, c := range r.perGPUKernelTimeCounter {
			r.metricsCollector.Collect(
				r.platform.GPUs[i].CommandProcessor.Name(),
				"kernel_time", float64(c.BusyTime()))
		}
	}
}

func (r *Runner) reportCacheLatency() {
	for _, tracer := range r.cacheLatencyTracers {
		if tracer.tracer.AverageTime() == 0 {
			continue
		}

		r.metricsCollector.Collect(
			tracer.cache.Name(),
			"req_average_latency",
			float64(tracer.tracer.AverageTime()),
		)
	}
}

func (r *Runner) reportRDMALatency() {
	for _, tracer := range r.rdmaLatencyTracers {
		if tracer.tracer.AverageTime() == 0 {
			continue
		}

		r.metricsCollector.Collect(
			tracer.rdma.Name(),
			"req_average_latency",
			float64(tracer.tracer.AverageTime()),
		)
	}
}

func (r *Runner) reportTLBLatency() {
	for _, tracer := range r.tlbLatencyTracers {
		if tracer.tracer.AverageTime() == 0 {
			continue
		}

		r.metricsCollector.Collect(
			tracer.tlb.Name(),
			"req_average_latency",
			float64(tracer.tracer.AverageTime()),
		)
	}
}

func (r *Runner) reportCacheHitRate() {
	for _, tracer := range r.cacheHitRateTracers {
		readHit := tracer.tracer.GetStepCount("read-hit")
		readMiss := tracer.tracer.GetStepCount("read-miss")
		readMSHRHit := tracer.tracer.GetStepCount("read-mshr-miss")
		writeHit := tracer.tracer.GetStepCount("write-hit")
		writeMiss := tracer.tracer.GetStepCount("write-miss")
		writeMSHRHit := tracer.tracer.GetStepCount("write-mshr-miss")

		totalTransaction := readHit + readMiss + readMSHRHit +
			writeHit + writeMiss + writeMSHRHit

		if totalTransaction == 0 {
			continue
		}

		r.metricsCollector.Collect(
			tracer.cache.Name(), "read-hit", float64(readHit))
		r.metricsCollector.Collect(
			tracer.cache.Name(), "read-miss", float64(readMiss))
		r.metricsCollector.Collect(
			tracer.cache.Name(), "read-mshr-hit", float64(readMSHRHit))
		r.metricsCollector.Collect(
			tracer.cache.Name(), "write-hit", float64(writeHit))
		r.metricsCollector.Collect(
			tracer.cache.Name(), "write-miss", float64(writeMiss))
		r.metricsCollector.Collect(
			tracer.cache.Name(), "write-mshr-hit", float64(writeMSHRHit))
	}
}

func (r *Runner) reportTLBHitRate() {
	for _, tracer := range r.tlbHitRateTracers {
		hit := tracer.tracer.GetStepCount("hit")
		miss := tracer.tracer.GetStepCount("miss")
		mshrHit := tracer.tracer.GetStepCount("mshr-hit")

		totalTransaction := hit + miss + mshrHit

		if totalTransaction == 0 {
			continue
		}

		r.metricsCollector.Collect(
			tracer.tlb.Name(), "hit", float64(hit))
		r.metricsCollector.Collect(
			tracer.tlb.Name(), "miss", float64(miss))
		r.metricsCollector.Collect(
			tracer.tlb.Name(), "mshr-hit", float64(mshrHit))
	}
}

func (r *Runner) reportRDMATransactionCount() {
	for _, t := range r.rdmaTransactionCounters {
		r.metricsCollector.Collect(
			t.rdmaEngine.Name(),
			"outgoing_trans_count",
			float64(t.outgoingTracer.TotalCount()),
		)
		r.metricsCollector.Collect(
			t.rdmaEngine.Name(),
			"incoming_trans_count",
			float64(t.incomingTracer.TotalCount()),
		)
	}
}

func (r *Runner) reportGMMUTransactionCount() {
	for _, t := range r.gmmuTransactionCounters {
		r.metricsCollector.Collect(
			t.gmmuEngine.Name(),
			"outgoing_trans_count",
			float64(t.outgoingTracer.TotalCount()),
		)
		r.metricsCollector.Collect(
			t.gmmuEngine.Name(),
			"incoming_trans_count",
			float64(t.incomingTracer.TotalCount()),
		)
	}
}

func (r *Runner) reportMMUTransactionCount() {
	for _, t := range r.mmuTransactionCounters {
		r.metricsCollector.Collect(
			t.mmuEngine.Name(),
			"outgoing_trans_count",
			float64(t.outgoingTracer.TotalCount()),
		)
		r.metricsCollector.Collect(
			t.mmuEngine.Name(),
			"incoming_trans_count",
			float64(t.incomingTracer.TotalCount()),
		)
	}
}

func (r *Runner) reportDRAMTransactionCount() {
	for _, t := range r.dramTracers {
		r.metricsCollector.Collect(
			t.dram.Name(),
			"read_trans_count",
			float64(t.tracer.readCount),
		)
		r.metricsCollector.Collect(
			t.dram.Name(),
			"write_trans_count",
			float64(t.tracer.writeCount),
		)
		r.metricsCollector.Collect(
			t.dram.Name(),
			"read_avg_latency",
			float64(t.tracer.readAvgLatency),
		)
		r.metricsCollector.Collect(
			t.dram.Name(),
			"write_avg_latency",
			float64(t.tracer.writeAvgLatency),
		)
		r.metricsCollector.Collect(
			t.dram.Name(),
			"read_size",
			float64(t.tracer.readSize),
		)
		r.metricsCollector.Collect(
			t.dram.Name(),
			"write_size",
			float64(t.tracer.writeSize),
		)
	}
}

func (r *Runner) reportGMMUCacheHitRate() {
	for _, tracer := range r.gmmuCacheHitRateTracers {
		low, high := tracer.gmmuCache.PTCLThresholds()
		toPTCL, toPTE := tracer.gmmuCache.ModeSwitchCounts()
		pteModeCompletions, ptclModeCompletions :=
			tracer.gmmuCache.ModeCompletionCounts()
		ptclSetModeFlushes := tracer.gmmuCache.PTCLSetModeFlushes()
		totalDownstream, localDownstream, iommuDownstream :=
			tracer.gmmuCache.DownstreamRequestCounts()
		pteLookupDelayCount, pteLookupDelayCycles :=
			tracer.gmmuCache.PTELookupDelayStats()
		pteLookupMaxInflight, pteLookupMaxWaiting :=
			tracer.gmmuCache.PTELookupQueueStats()
		flexEnabled, flexPromotionThreshold, flexPTEPackEntries,
			flexPTCLLineEntries, flexLookupJobs, flexRequestedBits,
			flexHitBits, flexMissBits, flexSavedJobs, flexPTEPackHits,
			flexPTCLLineHits, flexPartialPTCLHits, flexFullPTCLHits,
			flexPromotions, flexDemotions, flexInvalidatedPTEPackSlots,
			flexEvictedValidSlotsForPTCL, flexSets, flexWays, flexPTESlots :=
			tracer.gmmuCache.FlexTLBStats()
		prefetchEnabled, _, generated, enqueued, dropped, rejectedByPrefix,
			rejectedByDuplicate, rejectedByInvalid, rejectedByIOMMUFallbackGate,
			noClearPatternSkips, admitted, _ :=
			tracer.gmmuCache.PrefetchStats()
		prefetchCompleted, prefetchUseful, prefetchLate, prefetchLost :=
			tracer.gmmuCache.PrefetchOutcomeStats()
		prefetchLateQueued, prefetchLateInflight, prefetchRedundantFill,
			prefetchServedOutstandingDemand, prefetchUnusedResident :=
			tracer.gmmuCache.PrefetchDiagnosisStats()
		prefetchDemandLatency, prefetchLocalLatency, prefetchRemoteLatency,
			prefetchLookahead, prefetchQueueLen, prefetchIssued,
			prefetchBlockedByPTW, prefetchIOMMUFallbacks :=
			tracer.gmmuCache.PrefetchAdaptiveStats()
		prefetchDisabledBlocks, prefetchRejectedByFeedback, prefetchDisabledByFeedback :=
			tracer.gmmuCache.PrefetchFeedbackStats()
		ptclModeEnabled := 0.0
		if tracer.gmmuCache.PTCLModeEnabled() {
			ptclModeEnabled = 1.0
		}
		prefetchEnabledFloat := 0.0
		if prefetchEnabled {
			prefetchEnabledFloat = 1.0
		}
		flexEnabledFloat := 0.0
		if flexEnabled {
			flexEnabledFloat = 1.0
		}
		prefetchDisabledByFeedbackFloat := 0.0
		if prefetchDisabledByFeedback {
			prefetchDisabledByFeedbackFloat = 1.0
		}
		vpnMSHRBaselineEnabled := 0.0
		if tracer.gmmuCache.VPNMSHRBaselineEnabled() {
			vpnMSHRBaselineEnabled = 1.0
		}

		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(), "ptcl_mode_enabled", ptclModeEnabled)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"vpn_mshr_baseline_enabled",
			vpnMSHRBaselineEnabled,
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"ptcl_coalescing_counter",
			float64(tracer.gmmuCache.CoalescingCounter()),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(), "ptcl_threshold_low", float64(low))
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(), "ptcl_threshold_high", float64(high))
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(), "ptcl_switch_to_ptcl", float64(toPTCL))
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(), "ptcl_switch_to_pte", float64(toPTE))
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"pte_mode_completions",
			float64(pteModeCompletions),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"ptcl_mode_completions",
			float64(ptclModeCompletions),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"ptcl_set_mode_flushes",
			float64(ptclSetModeFlushes),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"downstream_req_count",
			float64(totalDownstream),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"local_req_count",
			float64(localDownstream),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"iommu_req_count",
			float64(iommuDownstream),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"lookup_latency_cycles_per_pte",
			float64(tracer.gmmuCache.PTELookupLatencyCycles()),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"pte_lookup_delay_count",
			float64(pteLookupDelayCount),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"pte_lookup_delay_cycles",
			float64(pteLookupDelayCycles),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"pte_lookup_max_inflight_slots",
			float64(pteLookupMaxInflight),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"pte_lookup_waiting_max_len",
			float64(pteLookupMaxWaiting),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(), "flex_tlb_enabled", flexEnabledFloat)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"flex_promotion_threshold",
			float64(flexPromotionThreshold),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"flex_pte_pack_entries",
			float64(flexPTEPackEntries),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"flex_ptcl_line_entries",
			float64(flexPTCLLineEntries),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"flex_lookup_jobs",
			float64(flexLookupJobs),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"flex_lookup_requested_bits",
			float64(flexRequestedBits),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"flex_lookup_hit_bits",
			float64(flexHitBits),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"flex_lookup_miss_bits",
			float64(flexMissBits),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"flex_lookup_saved_jobs",
			float64(flexSavedJobs),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"flex_pte_pack_hits",
			float64(flexPTEPackHits),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"flex_ptcl_line_hits",
			float64(flexPTCLLineHits),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"flex_partial_ptcl_hits",
			float64(flexPartialPTCLHits),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"flex_full_ptcl_hits",
			float64(flexFullPTCLHits),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"flex_promotions",
			float64(flexPromotions),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"flex_demotions",
			float64(flexDemotions),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"flex_invalidated_pte_pack_slots",
			float64(flexInvalidatedPTEPackSlots),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"flex_evicted_valid_slots_for_ptcl_line",
			float64(flexEvictedValidSlotsForPTCL),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(), "flex_num_sets", float64(flexSets))
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(), "flex_num_ways", float64(flexWays))
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(), "flex_pte_slot_capacity", float64(flexPTESlots))
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(), "prefetch_enabled", prefetchEnabledFloat)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_generated_candidates",
			float64(generated),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_enqueued_candidates",
			float64(enqueued),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_dropped_candidates",
			float64(dropped),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_rejected_by_prefix_filter",
			float64(rejectedByPrefix),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_rejected_by_duplicate_filter",
			float64(rejectedByDuplicate),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_rejected_by_invalid_target",
			float64(rejectedByInvalid),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_rejected_by_iommu_fallback_gate",
			float64(rejectedByIOMMUFallbackGate),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_rejected_by_feedback",
			float64(prefetchRejectedByFeedback),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_disabled_blocks",
			float64(prefetchDisabledBlocks),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_disabled_by_feedback",
			prefetchDisabledByFeedbackFloat,
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_admitted_learners",
			float64(admitted),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_no_clear_pattern_skips",
			float64(noClearPatternSkips),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_completed_fills",
			float64(prefetchCompleted),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_useful_hits",
			float64(prefetchUseful),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_late_demands",
			float64(prefetchLate),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_late_demand_queued",
			float64(prefetchLateQueued),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_late_demand_inflight",
			float64(prefetchLateInflight),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_redundant_fills",
			float64(prefetchRedundantFill),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_served_outstanding_demands",
			float64(prefetchServedOutstandingDemand),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_unused_resident_entries",
			float64(prefetchUnusedResident),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_lost_before_use",
			float64(prefetchLost),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_demand_translation_latency_cycles",
			float64(prefetchDemandLatency),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_local_latency_cycles",
			float64(prefetchLocalLatency),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_remote_latency_cycles",
			float64(prefetchRemoteLatency),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_adaptive_lookahead",
			float64(prefetchLookahead),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_queue_len",
			float64(prefetchQueueLen),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_issued_candidates",
			float64(prefetchIssued),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_blocked_by_no_free_ptw",
			float64(prefetchBlockedByPTW),
		)
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"prefetch_iommu_fallback_candidates",
			float64(prefetchIOMMUFallbacks),
		)

		hit := tracer.tracer.GetStepCount("hit")
		miss := tracer.tracer.GetStepCount("miss")
		mshrHit := tracer.tracer.GetStepCount("mshr-hit")

		totalTransaction := hit + miss + mshrHit

		if totalTransaction == 0 {
			continue
		}

		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(), "hit", float64(hit))
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(), "miss", float64(miss))
		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(), "mshr-hit", float64(mshrHit))
	}
}

func (r *Runner) reportGMMUCacheLatency() {
	for _, tracer := range r.gmmuCacheLatencyTracers {
		if tracer.tracer.AverageTime() == 0 {
			continue
		}

		r.metricsCollector.Collect(
			tracer.gmmuCache.Name(),
			"req_average_latency",
			float64(tracer.tracer.AverageTime()),
		)
	}
}

func (r *Runner) reportGMMULatency() {
	for _, tracer := range r.gmmuLatencyTracers {
		if tracer.tracer.AverageTime() == 0 {
			continue
		}

		r.metricsCollector.Collect(
			tracer.gmmu.Name(),
			"req_average_latency",
			float64(tracer.tracer.AverageTime()),
		)
	}
}

func (r *Runner) reportMMULatency() {
	for _, tracer := range r.mmuLatencyTracers {
		if tracer.tracer.AverageTime() == 0 {
			continue
		}

		r.metricsCollector.Collect(
			"MMU",
			"req_average_latency",
			float64(tracer.tracer.AverageTime()),
		)
	}
}

func (r *Runner) reportIOMMUTLBStats() {
	if r.platform == nil || r.platform.IOMMUTLB == nil {
		return
	}

	prefetchEnabled, demandPTCLReturn, generated, enqueued, dropped, rejectedByPrefix,
		rejectedByDuplicate, rejectedByInvalid, noClearPatternSkips, admitted, promoted :=
		r.platform.IOMMUTLB.PrefetchStats()
	completed, useful, late, lostBeforeUse := r.platform.IOMMUTLB.PrefetchOutcomeStats()
	blockStats := r.platform.IOMMUTLB.PrefetchBlockStats()
	setAsLineEnabled, setLookupJobs, setRequestedBits, setHitBits, setMissBits,
		setSavedJobs, setFills, setConflictEvictions, setLineEntries :=
		r.platform.IOMMUTLB.SetAsLineStats()
	prefetchEnabledFloat := 0.0
	if prefetchEnabled {
		prefetchEnabledFloat = 1.0
	}
	setAsLineEnabledFloat := 0.0
	if setAsLineEnabled {
		setAsLineEnabledFloat = 1.0
	}
	demandPTCLReturnFloat := 0.0
	if demandPTCLReturn {
		demandPTCLReturnFloat = 1.0
	}

	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"incoming_req_count",
		float64(r.platform.IOMMUTLB.IncomingRequestCount()),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"req_to_mmu_count",
		float64(r.platform.IOMMUTLB.DownstreamRequestCount()),
	)
	demandPTEOnlyFloat := 0.0
	if r.platform.IOMMUTLB.DemandPTEOnlyEnabled() {
		demandPTEOnlyFloat = 1.0
	}
	vpnMSHRBaselineFloat := 0.0
	if r.platform.IOMMUTLB.VPNMSHRBaselineEnabled() {
		vpnMSHRBaselineFloat = 1.0
	}
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"demand_pte_only_enabled",
		demandPTEOnlyFloat,
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"vpn_mshr_baseline_enabled",
		vpnMSHRBaselineFloat,
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"lookup_latency_cycles_per_bit",
		float64(r.platform.IOMMUTLB.LookupLatencyCycles()),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"iotlb_set_as_line_enabled",
		setAsLineEnabledFloat,
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"iotlb_set_lookup_jobs",
		float64(setLookupJobs),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"iotlb_set_lookup_requested_bits",
		float64(setRequestedBits),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"iotlb_set_lookup_hit_bits",
		float64(setHitBits),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"iotlb_set_lookup_miss_bits",
		float64(setMissBits),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"iotlb_set_lookup_saved_jobs",
		float64(setSavedJobs),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"iotlb_set_fills",
		float64(setFills),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"iotlb_set_conflict_evictions",
		float64(setConflictEvictions),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"iotlb_set_line_entries",
		float64(setLineEntries),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"prefetch_enabled",
		prefetchEnabledFloat,
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"prefetch_demand_ptcl_return_enabled",
		demandPTCLReturnFloat,
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"prefetch_generated_candidates",
		float64(generated),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"prefetch_enqueued_candidates",
		float64(enqueued),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"prefetch_dropped_candidates",
		float64(dropped),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"prefetch_rejected_by_prefix_filter",
		float64(rejectedByPrefix),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"prefetch_rejected_by_duplicate_filter",
		float64(rejectedByDuplicate),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"prefetch_rejected_by_invalid_target",
		float64(rejectedByInvalid),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"prefetch_admitted_learners",
		float64(admitted),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"prefetch_promoted_demands",
		float64(promoted),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"prefetch_no_clear_pattern_skips",
		float64(noClearPatternSkips),
	)
	// Keep the old metric name for compatibility with existing analysis scripts.
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"prefetch_pattern_resets",
		float64(noClearPatternSkips),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"prefetch_completed_fills",
		float64(completed),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"prefetch_useful_hits",
		float64(useful),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"prefetch_late_demands",
		float64(late),
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"prefetch_lost_before_use",
		float64(lostBeforeUse),
	)
	usefulRateByEnqueued := 0.0
	if enqueued > 0 {
		usefulRateByEnqueued = float64(useful) / float64(enqueued)
	}
	usefulRateByCompleted := 0.0
	if completed > 0 {
		usefulRateByCompleted = float64(useful) / float64(completed)
	}
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"prefetch_useful_rate_by_enqueued",
		usefulRateByEnqueued,
	)
	r.metricsCollector.Collect(
		r.platform.IOMMUTLB.Name(),
		"prefetch_useful_rate_by_completed",
		usefulRateByCompleted,
	)

	if !prefetchEnabled {
		return
	}

	fmt.Printf(
		"[PF][summary] component=%s enqueued=%d completed=%d useful=%d late=%d lost_before_use=%d useful_rate_enqueued=%.6f useful_rate_completed=%.6f no_clear_pattern_skips=%d\n",
		r.platform.IOMMUTLB.Name(),
		enqueued,
		completed,
		useful,
		late,
		lostBeforeUse,
		usefulRateByEnqueued,
		usefulRateByCompleted,
		noClearPatternSkips,
	)

	for _, block := range blockStats {
		if block.Enqueued == 0 && block.Completed == 0 && block.Useful == 0 && block.Late == 0 && block.LostBeforeUse == 0 {
			continue
		}

		blockUsefulRateByEnqueued := 0.0
		if block.Enqueued > 0 {
			blockUsefulRateByEnqueued = float64(block.Useful) / float64(block.Enqueued)
		}
		blockUsefulRateByCompleted := 0.0
		if block.Completed > 0 {
			blockUsefulRateByCompleted = float64(block.Useful) / float64(block.Completed)
		}

		fmt.Printf(
			"[PF][bo-summary] component=%s page_block=%d enqueued=%d completed=%d useful=%d late=%d lost_before_use=%d useful_rate_enqueued=%.6f useful_rate_completed=%.6f\n",
			r.platform.IOMMUTLB.Name(),
			block.PageBlock,
			block.Enqueued,
			block.Completed,
			block.Useful,
			block.Late,
			block.LostBeforeUse,
			blockUsefulRateByEnqueued,
			blockUsefulRateByCompleted,
		)
	}
}

func (r *Runner) reportMMUCoalescingStats() {
	if r.platform == nil || len(r.platform.GPUs) == 0 || r.platform.GPUs[0].MMUEngine == nil {
		return
	}

	enabled := 0.0
	if r.platform.GPUs[0].MMUEngine.WalkCoalescingEnabled() {
		enabled = 1.0
	}
	lastLevel, twoLevel := r.platform.GPUs[0].MMUEngine.CoalescingStats()
	r.metricsCollector.Collect(
		"MMU",
		"coalescing_enabled",
		enabled,
	)
	r.metricsCollector.Collect(
		"MMU",
		"last_level_coalesced_reqs",
		float64(lastLevel),
	)
	r.metricsCollector.Collect(
		"MMU",
		"two_level_coalesced_reqs",
		float64(twoLevel),
	)
}

// func (r *Runner) reportGMMUCounts() {
// 	for _, t := range r.gmmuCountTracers {
// 		r.metricsCollector.Collect(
// 			t.gmmu.Name(),
// 			"total_ats_count",
// 			float64(t.tracer.GetTotalATSCount()),
// 		)
// 		r.metricsCollector.Collect(
// 			t.gmmu.Name(),
// 			"local_ats_count",
// 			float64(t.tracer.GetLocalATSCount()),
// 		)
// 		r.metricsCollector.Collect(
// 			t.gmmu.Name(),
// 			"remote_ats_count",
// 			float64(t.tracer.GetRemoteATSCount()),
// 		)
// 	}
// }

// func (r *Runner) reportGMMUCacheCounts() {
// 	for _, t := range r.GMMUTLBTracers {
// 		r.metricsCollector.Collect(
// 			t.tlb.Name(),
// 			"AverageLocalAccessCounts",
// 			float64(t.tracer.ReportAverageLocalAccessCounts()),
// 		)
// 		r.metricsCollector.Collect(
// 			t.tlb.Name(),
// 			"AverageRemoteAccessCounts",
// 			float64(t.tracer.ReportAverageRemoteAccessCounts()),
// 		)
// 	}
// }
