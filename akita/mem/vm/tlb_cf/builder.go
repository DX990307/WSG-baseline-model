package tlb_cf

import (
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
	filterFlag     bool
	log2PageSize   uint64
	deviceID       uint64
	pageTable      vm.PageTable
}

// MakeBuilder returns a Builder
func MakeBuilder() Builder {
	return Builder{
		freq:           1 * sim.GHz,
		numReqPerCycle: 4,
		numSets:        1,
		numWays:        32,
		pageSize:       4096,
		numMSHREntry:   4,
		filterFlag:     false,
	}
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

func (b Builder) WithFilterFlag(flag bool) Builder {
	b.filterFlag = flag
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
	tlb.mshr = newMSHR(b.numMSHREntry)
	tlb.deviceID = b.deviceID
	tlb.pageTable = b.pageTable

	b.createPorts(name, tlb)

	if b.filterFlag {
		tlb.cf = NewCuckooFilter()
	}

	tlb.reset()

	return tlb
}

func (b Builder) createPorts(name string, tlb *TLB) {
	tlb.topPort = sim.NewLimitNumMsgPort(tlb, b.numReqPerCycle,
		name+".TopPort")
	tlb.AddPort("Top", tlb.topPort)

	tlb.bottomPort = sim.NewLimitNumMsgPort(tlb, b.numReqPerCycle,
		name+".BottomPort")
	tlb.AddPort("Bottom", tlb.bottomPort)

	tlb.controlPort = sim.NewLimitNumMsgPort(tlb, 1,
		name+".ControlPort")
	tlb.AddPort("Control", tlb.controlPort)

	tlb.outsidePort = sim.NewLimitNumMsgPort(tlb, 90000000,
		name+".OutsidePort")
	tlb.AddPort("Outside", tlb.outsidePort)

	tlb.cmpPort = sim.NewLimitNumMsgPort(tlb, 1,
		name+".CmpPort")
	tlb.AddPort("Cmp", tlb.cmpPort)
}
