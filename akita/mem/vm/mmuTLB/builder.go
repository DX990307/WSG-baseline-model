package mmuTLB

import (
	"github.com/sarchlab/akita/v3/mem/mem"
	"github.com/sarchlab/akita/v3/mem/vm"
	"github.com/sarchlab/akita/v3/sim"
)

// A Builder can build TLBs
type Builder struct {
	engine                      sim.Engine
	freq                        sim.Freq
	numReqPerCycle              int
	numSets                     int
	numWays                     int
	pageSize                    uint64
	log2PageSize                uint64
	lowModule                   sim.Port
	numMSHREntry                int
	mshrEntryDepth              int
	isPrediction                bool
	bloomFilterSize             int
	gmmuCacheTable              *mem.MultiPageFinder
	pagetable                   vm.PageTable
	perVPNMSHRBaseline          bool
	demandPTEOnly               bool
	setAsLineTLBEnabled         bool
	lookupLatencyCycles         int
	prefetchEnabled             bool
	prefetchDemandPTCLReturn    bool
	prefetchAdmissionThreshold  int
	prefetchMaxLearners         int
	prefetchLookahead           int
	prefetchMaxCandidatesPerReq int

	maxInflightTransactions int
	inflightTransactions    int
	translationRequests     map[uint64]map[vm.PID]*vm.TranslationReq
}

// MakeBuilder returns a Builder
func MakeBuilder() Builder {
	return Builder{
		freq:                        1 * sim.GHz,
		numReqPerCycle:              4,
		numSets:                     1,
		numWays:                     32,
		pageSize:                    4096,
		numMSHREntry:                64,
		mshrEntryDepth:              64,
		isPrediction:                false,
		bloomFilterSize:             64,
		maxInflightTransactions:     17,
		inflightTransactions:        0,
		log2PageSize:                12,
		translationRequests:         make(map[uint64]map[vm.PID]*vm.TranslationReq),
		lookupLatencyCycles:         80,
		prefetchAdmissionThreshold:  6,
		prefetchMaxLearners:         4,
		prefetchLookahead:           2,
		prefetchMaxCandidatesPerReq: 4,
	}
}

func (b Builder) WithPageTable(pageTable vm.PageTable) Builder {
	b.pagetable = pageTable
	return b
}

func (b Builder) WithLog2PageSize(log2PageSize uint64) Builder {
	b.log2PageSize = log2PageSize
	return b
}

func (b Builder) WithMSHREntryDepth(depth int) Builder {
	b.mshrEntryDepth = depth
	return b
}

func (b Builder) WithGMMUCacheTable(gmmuCacheTable *mem.MultiPageFinder) Builder {
	b.gmmuCacheTable = gmmuCacheTable
	return b
}

func (b Builder) WithTranslationPrefetcher(enabled bool) Builder {
	b.prefetchEnabled = enabled
	return b
}

func (b Builder) WithDemandPTEOnly(enabled bool) Builder {
	b.demandPTEOnly = enabled
	return b
}

func (b Builder) WithSetAsLineTLB(enabled bool) Builder {
	b.setAsLineTLBEnabled = enabled
	return b
}

func (b Builder) WithPTCLReturnLatencyCycles(cycles int) Builder {
	b.lookupLatencyCycles = cycles
	return b
}

func (b Builder) WithLookupLatencyCycles(cycles int) Builder {
	b.lookupLatencyCycles = cycles
	return b
}

func (b Builder) WithPerVPNMSHRBaseline(enabled bool) Builder {
	b.perVPNMSHRBaseline = enabled
	return b
}

func (b Builder) WithPrefetchDemandPTCLReturn(enabled bool) Builder {
	b.prefetchDemandPTCLReturn = enabled
	return b
}

func (b Builder) WithPrefetchAdmissionThreshold(threshold int) Builder {
	b.prefetchAdmissionThreshold = threshold
	return b
}

func (b Builder) WithPrefetchMaxLearners(maxLearners int) Builder {
	b.prefetchMaxLearners = maxLearners
	return b
}

func (b Builder) WithPrefetchLookahead(lookahead int) Builder {
	b.prefetchLookahead = lookahead
	return b
}

func (b Builder) WithPrefetchMaxCandidatesPerReq(limit int) Builder {
	b.prefetchMaxCandidatesPerReq = limit
	return b
}

// WithEngine sets the engine that the TLBs to use
func (b Builder) WithEngine(engine sim.Engine) Builder {
	b.engine = engine
	return b
}

// WithFreq sets the freq the TLBs use
func (b Builder) WithFreq(freq sim.Freq) Builder {
	b.freq = freq
	return b
}

// WithNumSets sets the number of sets in a TLB. Use 1 for fully associated
// TLBs.
func (b Builder) WithNumSets(n int) Builder {
	b.numSets = n
	return b
}

// WithNumWays sets the number of ways in a TLB. Set this field to the number
// of TLB entries for all the functions.
func (b Builder) WithNumWays(n int) Builder {
	b.numWays = n
	return b
}

// WithPageSize sets the page size that the TLB works with.
func (b Builder) WithPageSize(n uint64) Builder {
	b.pageSize = n
	return b
}

// WithNumReqPerCycle sets the number of requests per cycle can be processed by
// a TLB
func (b Builder) WithNumReqPerCycle(n int) Builder {
	b.numReqPerCycle = n
	return b
}

// WithLowModule sets the port that can provide the address translation in case
// of tlb miss.
func (b Builder) WithLowModule(lowModule sim.Port) Builder {
	b.lowModule = lowModule
	return b
}

// WithNumMSHREntry sets the number of mshr entry
func (b Builder) WithNumMSHREntry(num int) Builder {
	b.numMSHREntry = num
	return b
}

func (b Builder) WithPrediction() Builder {
	b.isPrediction = true
	return b
}

func (b Builder) WithBloomFilterSize(size int) Builder {
	b.bloomFilterSize = size
	return b
}

// Build creates a new TLB
func (b Builder) Build(name string) *TLB {
	tlb := &TLB{}
	tlb.TickingComponent =
		sim.NewTickingComponent(name, b.engine, b.freq, tlb)

	tlb.numSets = b.numSets
	tlb.numWays = b.numWays
	tlb.numReqPerCycle = b.numReqPerCycle
	tlb.pageSize = b.pageSize
	tlb.LowModule = b.lowModule
	tlb.isPrediction = b.isPrediction
	tlb.gmmuCacheTable = b.gmmuCacheTable
	tlb.log2PageSize = b.log2PageSize
	tlb.pageTable = b.pagetable
	tlb.vpnMSHRBaseline = b.perVPNMSHRBaseline
	tlb.demandPTEOnly = b.demandPTEOnly
	tlb.setAsLineTLBEnabled = b.setAsLineTLBEnabled
	tlb.lookupLatencyCycles = b.lookupLatencyCycles
	tlb.prefetcher = newTranslationPrefetcher(
		b.prefetchEnabled,
		b.prefetchDemandPTCLReturn,
		b.prefetchAdmissionThreshold,
		b.prefetchMaxLearners,
		b.prefetchLookahead,
		b.prefetchMaxCandidatesPerReq,
	)
	tlb.inflightPrefetches = make(map[prefetchTargetKey]struct{})
	tlb.prefetchReqStates = make(map[string]*prefetchReqState)
	tlb.completedPrefetches = make(map[prefetchTargetKey]*completedPrefetchState)
	tlb.inflightPrefetchStateByKey = make(map[prefetchTargetKey]*prefetchReqState)
	tlb.prefetchOutcomeByBlock = make(map[uint64]*prefetchOutcomeCounts)
	tlb.prefetchFeedbackByTarget = make(map[prefetchFeedbackKey]*prefetchFeedbackCounters)
	tlb.lookupReadyTimes = make(map[string]sim.VTimeInSec)

	if b.isPrediction {
		tlb.BloomFilter = NewBloomFilter(b.bloomFilterSize)
	}

	tlb.mshr = newMSHR(
		b.numMSHREntry,
		b.mshrEntryDepth,
		b.log2PageSize,
		b.perVPNMSHRBaseline,
	)

	b.createPorts(name, tlb)

	tlb.reset()

	return tlb
}

func (b Builder) createPorts(name string, tlb *TLB) {
	// tlb.topPort = sim.NewLimitNumMsgPort(tlb, b.numReqPerCycle,
	tlb.topPort = sim.NewLimitNumMsgPort(tlb, 48,
		name+".TopPort")
	tlb.AddPort("Top", tlb.topPort)

	// tlb.bottomPort = sim.NewLimitNumMsgPort(tlb, b.numReqPerCycle,
	tlb.bottomPort = sim.NewLimitNumMsgPort(tlb, 48,
		name+".BottomPort")
	tlb.AddPort("Bottom", tlb.bottomPort)

	tlb.controlPort = sim.NewLimitNumMsgPort(tlb, 1,
		name+".ControlPort")
	tlb.AddPort("Control", tlb.controlPort)
}
