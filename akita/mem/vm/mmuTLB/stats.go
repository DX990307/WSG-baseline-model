package mmuTLB

import (
	"sort"

	"github.com/sarchlab/akita/v3/mem/vm/mmuTLB/internal"
)

// PrefetchOutcomeBlockStat summarizes observed prefetch outcomes for one BO/page block.
type PrefetchOutcomeBlockStat struct {
	PageBlock     uint64
	Enqueued      int
	Completed     int
	Useful        int
	Late          int
	LostBeforeUse int
}

// IncomingRequestCount reports how many translation requests entered the
// IOMMU-side TLB from the GMMU path.
func (tlb *TLB) IncomingRequestCount() int {
	return tlb.incomingReqCount
}

// DownstreamRequestCount reports how many translation requests the IOMMU-side
// TLB sent toward MMUCache/MMU.
func (tlb *TLB) DownstreamRequestCount() int {
	return tlb.downstreamReqCount
}

// DemandPTEOnlyEnabled reports whether the IOMMU-side demand path is forced
// to single-page requests while still allowing PTCL prefetches.
func (tlb *TLB) DemandPTEOnlyEnabled() bool {
	return tlb.demandPTEOnly
}

// VPNMSHRBaselineEnabled reports whether the IOMMU-side TLB is using exact-VPN
// MSHR entries instead of PTCL-granularity entries.
func (tlb *TLB) VPNMSHRBaselineEnabled() bool {
	return tlb.vpnMSHRBaseline
}

// LookupLatencyCycles reports the fixed MMUTLB/IOTLB lookup delay applied to
// each buffered request before tag lookup/hit-miss handling proceeds.
func (tlb *TLB) LookupLatencyCycles() int {
	return tlb.lookupLatencyCycles
}

// SetAsLineStats reports PTCL set-as-line lookup/fill activity in the
// IOMMU-side TLB. The optimization is separate from the prefetcher.
func (tlb *TLB) SetAsLineStats() (
	enabled bool,
	lookupJobs int,
	requestedBits int,
	hitBits int,
	missBits int,
	savedJobs int,
	fills int,
	conflictEvictions int,
	lineEntries int,
) {
	for _, set := range tlb.Sets {
		ptclSet, ok := set.(internal.PTCLSet)
		if ok && ptclSet.PTCLLineValid() {
			lineEntries++
		}
	}

	return tlb.setAsLineTLBEnabled,
		tlb.setLookupJobs,
		tlb.setLookupRequestedBits,
		tlb.setLookupHitBits,
		tlb.setLookupMissBits,
		tlb.setLookupSavedJobs,
		tlb.setFills,
		tlb.setConflictEvictions,
		lineEntries
}

// PrefetchStats reports whether the MMUTLB prefetcher is enabled and how many
// candidates it generated, enqueued, dropped, and admitted as active BO
// learners.
func (tlb *TLB) PrefetchStats() (
	enabled bool,
	demandPTCLReturn bool,
	generated int,
	enqueued int,
	dropped int,
	rejectedByPrefix int,
	rejectedByDuplicate int,
	rejectedByInvalid int,
	noClearPatternSkips int,
	admitted int,
	promoted int,
) {
	if tlb.prefetcher == nil {
		return false, false, 0, 0, 0, 0, 0, 0, 0, 0, 0
	}

	return tlb.prefetcher.enabled,
		tlb.prefetcher.promoteDemandToPTCL,
		tlb.prefetcher.generatedCandidates,
		tlb.prefetcher.enqueuedCandidates,
		tlb.prefetcher.droppedCandidates,
		tlb.prefetcher.rejectedByPrefix,
		tlb.prefetcher.rejectedByDuplicate,
		tlb.prefetcher.rejectedByInvalid,
		tlb.prefetcher.noClearPatternSkips,
		tlb.prefetcher.admittedLearnersCount,
		tlb.prefetcher.promotedDemandRequests
}

// PrefetchOutcomeStats reports the observed outcomes of issued prefetches.
func (tlb *TLB) PrefetchOutcomeStats() (
	completed int,
	useful int,
	late int,
	lostBeforeUse int,
) {
	return tlb.prefetchCompletedCount,
		tlb.prefetchUsefulHitCount,
		tlb.prefetchLateDemandCount,
		tlb.prefetchLostBeforeUseCount
}

// PrefetchBlockStats reports the observed prefetch outcomes grouped by page block.
func (tlb *TLB) PrefetchBlockStats() []PrefetchOutcomeBlockStat {
	if len(tlb.prefetchOutcomeByBlock) == 0 {
		return nil
	}

	pageBlocks := make([]uint64, 0, len(tlb.prefetchOutcomeByBlock))
	for pageBlock := range tlb.prefetchOutcomeByBlock {
		pageBlocks = append(pageBlocks, pageBlock)
	}

	sort.Slice(pageBlocks, func(i, j int) bool {
		return pageBlocks[i] < pageBlocks[j]
	})

	stats := make([]PrefetchOutcomeBlockStat, 0, len(pageBlocks))
	for _, pageBlock := range pageBlocks {
		counts := tlb.prefetchOutcomeByBlock[pageBlock]
		stats = append(stats, PrefetchOutcomeBlockStat{
			PageBlock:     pageBlock,
			Enqueued:      counts.Enqueued,
			Completed:     counts.Completed,
			Useful:        counts.Useful,
			Late:          counts.Late,
			LostBeforeUse: counts.LostBeforeUse,
		})
	}

	return stats
}
