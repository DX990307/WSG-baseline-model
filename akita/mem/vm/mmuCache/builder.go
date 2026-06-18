package mmuCache

import (
	"github.com/sarchlab/akita/v3/mem/mem"
	"github.com/sarchlab/akita/v3/mem/vm"
	"github.com/sarchlab/akita/v3/sim"
)

// A Builder can build TLBs
type Builder struct {
	engine          sim.Engine
	freq            sim.Freq
	numReqPerCycle  int
	numLevels       int
	numSets         int
	numWays         int
	pageSize        uint64
	segLength       int
	lowModule       sim.Port
	upModule        sim.Port
	numMSHREntry    int
	mshrEntryDepth  int
	isPrediction    bool
	bloomFilterSize int
	latencyPerLevel uint64
	log2PageSize    uint64
	gmmuCacheTable  *mem.MultiPageFinder

	maxInflightTransactions int
	inflightTransactions    int
	translationRequests     map[uint64]map[vm.PID]*vm.TranslationReq
}

// MakeBuilder returns a Builder
func MakeBuilder() Builder {
	return Builder{
		freq:                    1 * sim.GHz,
		numReqPerCycle:          4,
		numLevels:               4,
		numSets:                 1,
		numWays:                 32,
		pageSize:                4096,
		numMSHREntry:            64,
		mshrEntryDepth:          64,
		latencyPerLevel:         100,
		log2PageSize:            12,
		isPrediction:            false,
		bloomFilterSize:         64,
		maxInflightTransactions: 17,
		inflightTransactions:    0,
		translationRequests:     make(map[uint64]map[vm.PID]*vm.TranslationReq),
		segLength:               16,
	}
}

func (b Builder) WithLatencyPerLevel(latency uint64) Builder {
	b.latencyPerLevel = latency
	return b
}

func (b Builder) WithUpperModule(m sim.Port) Builder {
	b.upModule = m
	return b
}

// WithNumLevels sets the number of levels in the mmuCache
func (b Builder) WithNumLevels(n int) Builder {
	b.numLevels = n
	return b
}

func (b Builder) WithSegLength(length int) Builder {
	b.segLength = length
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

func (b Builder) WithLog2PageSize(log2PageSize uint64) Builder {
	b.log2PageSize = log2PageSize
	b.pageSize = 1 << log2PageSize
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
func (b Builder) Build(name string) *MMUCache {
	mmuCache := &MMUCache{}
	mmuCache.TickingComponent =
		sim.NewTickingComponent(name, b.engine, b.freq, mmuCache)

	mmuCache.numSets = b.numSets
	mmuCache.numWays = b.numWays
	mmuCache.numReqPerCycle = b.numReqPerCycle
	mmuCache.pageSize = b.pageSize
	mmuCache.LowModule = b.lowModule
	mmuCache.numLevels = b.numLevels
	mmuCache.latencyPerLevel = b.latencyPerLevel
	mmuCache.UpModule = b.upModule
	mmuCache.log2PageSize = b.log2PageSize

	b.createPorts(name, mmuCache)

	mmuCache.reset()

	return mmuCache
}

func (b Builder) createPorts(name string, mmuCache *MMUCache) {
	// mmuCache.topPort = sim.NewLimitNumMsgPort(mmuCache, b.numReqPerCycle,
	mmuCache.topPort = sim.NewLimitNumMsgPort(mmuCache, 4800,
		name+".TopPort")
	mmuCache.AddPort("Top", mmuCache.topPort)

	// mmuCache.bottomPort = sim.NewLimitNumMsgPort(mmuCache, b.numReqPerCycle,
	mmuCache.bottomPort = sim.NewLimitNumMsgPort(mmuCache, 4800,
		name+".BottomPort")
	mmuCache.AddPort("Bottom", mmuCache.bottomPort)

	mmuCache.controlPort = sim.NewLimitNumMsgPort(mmuCache, 1,
		name+".ControlPort")
	mmuCache.AddPort("Control", mmuCache.controlPort)
}
