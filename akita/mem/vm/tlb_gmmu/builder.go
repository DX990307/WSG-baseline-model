package tlb_gmmu

import (
	"github.com/sarchlab/akita/v3/mem/mem"
	"github.com/sarchlab/akita/v3/mem/vm"
	"github.com/sarchlab/akita/v3/sim"
)

// A Builder can build TLBs
type Builder struct {
	engine         sim.Engine
	freq           sim.Freq
	numReqPerCycle int
	numSets        int
	numWays        int
	pageSize       uint64
	lowModule      sim.Port
	numMSHREntry   int
	log2PageSize   uint64
	deviceID       uint64
	pageTable      vm.PageTable
	ioMMUPort      sim.Port
	// gmmuCacheTable map[uint64]sim.Port
	gmmuCacheTable         *mem.MultiPageFinder
	InnerLayer             map[uint64]uint64
	MiddleLayer            map[uint64]uint64
	OuterLayer             map[uint64]uint64
	setSize                int
	ptclHighThres          int
	ptclLowThres           int
	initialPTCL            bool
	perVPNMSHR             bool
	flexTLBEnabled         bool
	flexPromotionThreshold int
	ptclSerialLookup       bool
	pteLookupLatencyCycles int
	prefetchEnabled        bool
	prefetchAdmission      int
	prefetchMaxLearners    int
	prefetchLookahead      int
	prefetchMaxCandidates  int
	localPTWState          PTWStateProvider
}

// MakeBuilder returns a Builder
func MakeBuilder() Builder {
	return Builder{
		freq:                   1 * sim.GHz,
		numReqPerCycle:         4,
		numSets:                1,
		numWays:                32,
		pageSize:               4096,
		numMSHREntry:           4,
		ptclHighThres:          6,
		ptclLowThres:           2,
		initialPTCL:            false,
		flexPromotionThreshold: 3,
		pteLookupLatencyCycles: 32,
		prefetchAdmission:      3,
		prefetchMaxLearners:    4,
		prefetchLookahead:      64,
		prefetchMaxCandidates:  4,
	}
}

func (b Builder) WithFlexTLB(enabled bool) Builder {
	b.flexTLBEnabled = enabled
	return b
}

func (b Builder) WithFlexPromotionThreshold(threshold int) Builder {
	b.flexPromotionThreshold = threshold
	return b
}

func (b Builder) WithPTCLSerialLookup(enabled bool) Builder {
	b.ptclSerialLookup = enabled
	return b
}

func (b Builder) WithSetSize(setSize int) Builder {
	b.setSize = setSize
	return b
}

func (b Builder) WithPTCLModeThresholds(low, high int) Builder {
	b.ptclLowThres = low
	b.ptclHighThres = high
	return b
}

func (b Builder) WithInitialPTCLMode(enabled bool) Builder {
	b.initialPTCL = enabled
	return b
}

func (b Builder) WithPerVPNMSHRBaseline(enabled bool) Builder {
	b.perVPNMSHR = enabled
	return b
}

func (b Builder) WithPTELookupLatencyCycles(cycles int) Builder {
	b.pteLookupLatencyCycles = cycles
	return b
}

func (b Builder) WithTranslationPrefetcher(enabled bool) Builder {
	b.prefetchEnabled = enabled
	return b
}

func (b Builder) WithPrefetchAdmissionThreshold(threshold int) Builder {
	b.prefetchAdmission = threshold
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
	b.prefetchMaxCandidates = limit
	return b
}

func (b Builder) WithLocalPTWStateProvider(provider PTWStateProvider) Builder {
	b.localPTWState = provider
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

func (b Builder) WithLog2PageSize(log2PageSize uint64) Builder {
	b.log2PageSize = log2PageSize
	return b
}

func (b Builder) WithDeviceID(deviceID uint64) Builder {
	b.deviceID = deviceID
	return b
}

func (b Builder) WithPageTable(pageTable vm.PageTable) Builder {
	b.pageTable = pageTable
	return b
}

func (b Builder) WithIOMMUPort(ioMMUPort sim.Port) Builder {
	b.ioMMUPort = ioMMUPort
	return b
}

func (b Builder) WithGMMUCacheTable(gmmuCacheTable *mem.MultiPageFinder) Builder {
	b.gmmuCacheTable = gmmuCacheTable
	return b
}

func (b Builder) WithInnerLayer(innerLayer map[uint64]uint64) Builder {
	b.InnerLayer = innerLayer
	return b
}

func (b Builder) WithMiddleLayer(middleLayer map[uint64]uint64) Builder {
	b.MiddleLayer = middleLayer
	return b
}

func (b Builder) WithOuterLayer(outerLayer map[uint64]uint64) Builder {
	b.OuterLayer = outerLayer
	return b
}

// Build creates a new TLB
func (b Builder) Build(name string) *GMMUTLB {
	tlb := &GMMUTLB{}
	tlb.TickingComponent =
		sim.NewTickingComponent(name, b.engine, b.freq, tlb)

	tlb.log2Pagesize = b.log2PageSize

	tlb.numSets = b.numSets
	tlb.numWays = b.numWays
	tlb.numReqPerCycle = b.numReqPerCycle
	tlb.pageSize = b.pageSize
	tlb.LowModule = b.lowModule
	tlb.mshr = newMSHR(b.numMSHREntry, 64, b.log2PageSize, b.perVPNMSHR)
	tlb.DeviceID = b.deviceID
	tlb.pageTable = b.pageTable
	tlb.IOMMUPort = b.ioMMUPort
	tlb.vpnMSHRBaseline = b.perVPNMSHR
	tlb.flexTLBEnabled = b.flexTLBEnabled
	tlb.flexPromotionThreshold = b.flexPromotionThreshold
	tlb.ptclSerialLookup = b.ptclSerialLookup
	tlb.gmmuCacheTable = b.gmmuCacheTable
	tlb.localPTWState = b.localPTWState
	tlb.pteLookupLatencyCycles = b.pteLookupLatencyCycles
	tlb.prefetcher = newTranslationPrefetcher(
		b.prefetchEnabled,
		b.prefetchAdmission,
		b.prefetchMaxLearners,
		b.prefetchLookahead,
		b.prefetchMaxCandidates,
	)
	tlb.initPrefetchState()
	lowThres := b.ptclLowThres
	highThres := b.ptclHighThres
	if lowThres > highThres {
		lowThres, highThres = highThres, lowThres
	}
	tlb.ptclMode = b.initialPTCL && !b.perVPNMSHR
	tlb.ptclHighThreshold = highThres
	tlb.ptclLowThreshold = lowThres
	if tlb.ptclMode {
		tlb.coalescingCounter = tlb.ptclHighThreshold
	}
	tlb.pteLookupGroups = make(map[pteLookupGroupKey]*pteLookupGroup)

	b.createPorts(name, tlb)

	tlb.reset()

	return tlb
}

func (b Builder) createPorts(name string, tlb *GMMUTLB) {
	tlb.topPort = sim.NewLimitNumMsgPort(tlb, 1280,
		name+".TopPort")
	tlb.AddPort("Top", tlb.topPort)
	// b.numReqPerCycle
	tlb.bottomPort = sim.NewLimitNumMsgPort(tlb, 1280,
		name+".BottomPort")
	tlb.AddPort("Bottom", tlb.bottomPort)

	tlb.controlPort = sim.NewLimitNumMsgPort(tlb, 1,
		name+".ControlPort")
	tlb.AddPort("Control", tlb.controlPort)

	tlb.OutsidePort = sim.NewLimitNumMsgPort(tlb, 1280,
		name+".OutsidePort")
	tlb.AddPort(".Outside", tlb.OutsidePort)
}
