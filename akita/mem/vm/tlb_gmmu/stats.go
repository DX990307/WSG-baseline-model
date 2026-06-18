package tlb_gmmu

import "github.com/sarchlab/akita/v3/mem/vm/tlb_gmmu/internal"

// PTCLModeEnabled reports whether the L2 TLB is currently coalescing at PTCL
// granularity.
func (tlb *GMMUTLB) PTCLModeEnabled() bool {
	return tlb.ptclMode && !tlb.vpnMSHRBaseline
}

// CoalescingCounter returns the adaptive PTCL/PTE score.
func (tlb *GMMUTLB) CoalescingCounter() int {
	return tlb.coalescingCounter
}

// PTCLThresholds returns the low and high hysteresis thresholds for the
// coalescing score.
func (tlb *GMMUTLB) PTCLThresholds() (low, high int) {
	return tlb.ptclLowThreshold, tlb.ptclHighThreshold
}

// ModeSwitchCounts returns the number of transitions into PTCL and PTE modes.
func (tlb *GMMUTLB) ModeSwitchCounts() (toPTCL, toPTE int) {
	return tlb.switchToPTCLCount, tlb.switchToPTECount
}

// ModeCompletionCounts returns how many completed MSHR entries were accounted
// while the adaptive L2 TLB was in PTE or PTCL mode.
func (tlb *GMMUTLB) ModeCompletionCounts() (pteMode, ptclMode int) {
	return tlb.pteModeCompletions, tlb.ptclModeCompletions
}

// PTCLSetModeFlushes returns how often the set-as-line TLB state was flushed
// because the MSHR coalescing mode switched.
func (tlb *GMMUTLB) PTCLSetModeFlushes() int {
	return tlb.ptclSetModeFlushes
}

// VPNMSHRBaselineEnabled reports whether the GMMU L2 TLB is using exact-VPN
// MSHR entries instead of PTCL-granularity entries.
func (tlb *GMMUTLB) VPNMSHRBaselineEnabled() bool {
	return tlb.vpnMSHRBaseline
}

// DownstreamRequestCounts reports how many translation requests the GMMU L2 TLB
// issued downstream in total, to the local MMU, and to the IOMMU path.
func (tlb *GMMUTLB) DownstreamRequestCounts() (total, local, iommu int) {
	return tlb.downstreamReqCount, tlb.localReqCount, tlb.iommuReqCount
}

// PTELookupLatencyCycles reports the fixed GMMUCache PTE lookup delay charged
// for each lookup slot job before the local TLB entry lookup.
func (tlb *GMMUTLB) PTELookupLatencyCycles() int {
	return tlb.pteLookupLatencyCycles
}

// PTELookupDelayStats reports how many internal PTE lookup jobs paid the
// GMMUCache lookup budget and the total charged slot-cycles.
func (tlb *GMMUTLB) PTELookupDelayStats() (count int, cycles int) {
	return tlb.pteLookupDelayCount, tlb.pteLookupDelayCycles
}

// PTELookupQueueStats reports the maximum number of occupied lookup slots and
// the maximum waiting queue length observed.
func (tlb *GMMUTLB) PTELookupQueueStats() (maxInflight, maxWaiting int) {
	return tlb.pteLookupMaxInflight, tlb.pteLookupMaxWaiting
}

func (tlb *GMMUTLB) resetFlexStats() {
	tlb.flexLookupJobs = 0
	tlb.flexLookupRequestedBits = 0
	tlb.flexLookupHitBits = 0
	tlb.flexLookupMissBits = 0
	tlb.flexLookupSavedJobs = 0
	tlb.flexPTEPackHits = 0
	tlb.flexPTCLLineHits = 0
	tlb.flexPartialPTCLHits = 0
	tlb.flexFullPTCLHits = 0
	tlb.flexPromotions = 0
	tlb.flexDemotions = 0
	tlb.flexInvalidatedPTEPackSlots = 0
	tlb.flexEvictedValidSlotsForPTCL = 0
	tlb.ptclSetModeFlushes = 0
	tlb.pcdFallbackLookupBits = 0
	tlb.pcdStaleBits = 0
}

func (tlb *GMMUTLB) FlexTLBStats() (
	enabled bool,
	promotionThreshold int,
	ptePackEntries int,
	ptclLineEntries int,
	lookupJobs int,
	requestedBits int,
	hitBits int,
	missBits int,
	savedJobs int,
	ptePackHits int,
	ptclLineHits int,
	partialPTCLHits int,
	fullPTCLHits int,
	promotions int,
	demotions int,
	invalidatedPTEPackSlots int,
	evictedValidSlotsForPTCL int,
	flexSets int,
	flexWays int,
	flexPTESlots int,
) {
	if tlb.flexTLBEnabled {
		flexSets = tlb.numSets
		flexWays = tlb.numWays
		flexPTESlots = tlb.numSets * tlb.numWays
		for _, set := range tlb.Sets {
			ptclSet, ok := set.(internal.PTCLSet)
			if !ok {
				continue
			}
			ptePackEntries += ptclSet.ValidPageCount()
		}
		if tlb.pcd != nil {
			ptclLineEntries = tlb.pcd.validEntryCount()
		}
	}

	return tlb.flexTLBEnabled,
		tlb.flexPromotionThreshold,
		ptePackEntries,
		ptclLineEntries,
		tlb.flexLookupJobs,
		tlb.flexLookupRequestedBits,
		tlb.flexLookupHitBits,
		tlb.flexLookupMissBits,
		tlb.flexLookupSavedJobs,
		tlb.flexPTEPackHits,
		tlb.flexPTCLLineHits,
		tlb.flexPartialPTCLHits,
		tlb.flexFullPTCLHits,
		tlb.flexPromotions,
		tlb.flexDemotions,
		tlb.flexInvalidatedPTEPackSlots,
		tlb.flexEvictedValidSlotsForPTCL,
		flexSets,
		flexWays,
		flexPTESlots
}

// PrefetchStats reports whether the GMMU-side prefetcher is enabled and how
// many candidates it generated, issued, or rejected.
func (tlb *GMMUTLB) PrefetchStats() (
	enabled bool,
	demandPTCLReturn bool,
	generated int,
	enqueued int,
	dropped int,
	rejectedByPrefix int,
	rejectedByDuplicate int,
	rejectedByInvalid int,
	rejectedByIOMMUFallbackGate int,
	noClearPatternSkips int,
	admitted int,
	promoted int,
) {
	if tlb.prefetcher == nil {
		return false, false, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0
	}

	return tlb.prefetcher.enabled,
		false,
		tlb.prefetcher.generatedCandidates,
		tlb.prefetcher.enqueuedCandidates,
		tlb.prefetcher.droppedCandidates,
		tlb.prefetcher.rejectedByPrefix,
		tlb.prefetcher.rejectedByDuplicate,
		tlb.prefetcher.rejectedByInvalid,
		tlb.prefetcher.rejectedByIOMMUFallbackGate,
		tlb.prefetcher.noClearPatternSkips,
		tlb.prefetcher.admittedLearnersCount,
		0
}

// PrefetchOutcomeStats reports the observed completion count for GMMU-side
// prefetches. Useful/late/lost are kept for metric compatibility.
func (tlb *GMMUTLB) PrefetchOutcomeStats() (
	completed int,
	useful int,
	late int,
	lostBeforeUse int,
) {
	return tlb.prefetchCompletedCount,
		tlb.prefetchUsefulCount,
		tlb.prefetchLateDemandCount,
		tlb.prefetchLostCount
}

func (tlb *GMMUTLB) PrefetchDiagnosisStats() (
	lateDemandQueued int,
	lateDemandInflight int,
	redundantFill int,
	servedOutstandingDemand int,
	unusedResident int,
) {
	return tlb.prefetchLateDemandQueuedCount,
		tlb.prefetchLateDemandInflightCount,
		tlb.prefetchRedundantFillCount,
		tlb.prefetchServedDemandCount,
		len(tlb.prefetchedResident)
}

func (tlb *GMMUTLB) PrefetchAdaptiveStats() (
	demandLatencyCycles int,
	localPrefetchLatencyCycles int,
	remotePrefetchLatencyCycles int,
	currentLookahead int,
	queueLen int,
	issued int,
	blockedByNoFreePTW int,
	iommuFallbacks int,
) {
	if tlb.prefetcher == nil {
		return 0, 0, 0, 0, 0, 0, 0, 0
	}

	return tlb.prefetcher.demandLatencyCycles,
		tlb.prefetcher.localPrefetchLatencyCycles,
		tlb.prefetcher.remotePrefetchLatencyCycles,
		tlb.prefetcher.currentLookahead,
		len(tlb.prefetchQueue),
		tlb.prefetcher.issuedCandidates,
		tlb.prefetcher.blockedByNoFreePTW,
		tlb.prefetcher.iommuFallbackCandidates
}

func (tlb *GMMUTLB) PrefetchFeedbackStats() (
	disabledBlocks int,
	rejectedByFeedback int,
	disabledByFeedback bool,
) {
	if tlb.prefetcher == nil {
		return 0, 0, false
	}

	return len(tlb.prefetchDisabledBlocks),
		tlb.prefetcher.rejectedByFeedback,
		false
}
