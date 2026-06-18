package runner

import (
	"fmt"
	"log"
	"os"

	memtraces "github.com/sarchlab/akita/v3/mem/trace"

	"github.com/sarchlab/akita/v3/analysis"
	"github.com/sarchlab/akita/v3/mem/mem"
	"github.com/sarchlab/akita/v3/mem/vm"
	"github.com/sarchlab/akita/v3/mem/vm/mmu"
	"github.com/sarchlab/akita/v3/mem/vm/mmuCache"
	"github.com/sarchlab/akita/v3/mem/vm/mmuTLB"
	"github.com/sarchlab/akita/v3/monitoring"
	mesh "github.com/sarchlab/akita/v3/noc/networking/mesh"
	"github.com/sarchlab/akita/v3/sim"
	"github.com/sarchlab/akita/v3/tracing"
	"github.com/sarchlab/mgpusim/v3/driver"
	"github.com/sarchlab/mgpusim/v3/emu"
	"github.com/sarchlab/mgpusim/v3/insts"
	"github.com/sarchlab/mgpusim/v3/timing/cp"
)

// R9NanoPlatformBuilder can build a platform that equips R9Nano GPU.
type R9NanoPlatformBuilder struct {
	useParallelEngine     bool
	debugISA              bool
	traceVis              bool
	visTraceStartTime     sim.VTimeInSec
	visTraceEndTime       sim.VTimeInSec
	traceMem              bool
	tileWidth, tileHeight int
	numSAPerGPU           int
	numCUPerSA            int
	useMagicMemoryCopy    bool
	log2PageSize          uint64
	bandwidth             int
	switchLatency         int
	maxNumHops            int

	engine       sim.Engine
	visTracer    tracing.Tracer
	monitor      *monitoring.Monitor
	IOMMUTLB     *mmuTLB.TLB
	IOMMUCache   *mmuCache.MMUCache
	mmu          *mmu.MMU
	mmuTopModule sim.Port

	globalStorage *mem.Storage

	perfAnalysisFileName string
	perfAnalyzingPeriod  float64
	perfAnalyzer         *analysis.PerfAnalyzer

	gpus []*GPU
}

// MakeR9NanoBuilder creates a EmuBuilder with default parameters.
func MakeR9NanoBuilder() R9NanoPlatformBuilder {
	b := R9NanoPlatformBuilder{
		tileWidth:         7,
		tileHeight:        7,
		log2PageSize:      12,
		visTraceStartTime: -1,
		visTraceEndTime:   -1,
		switchLatency:     20,
		numSAPerGPU:       8,
		numCUPerSA:        4,
		maxNumHops:        -1,
	}
	return b
}

// WithParallelEngine lets the EmuBuilder to use parallel engine.
func (b R9NanoPlatformBuilder) WithParallelEngine() R9NanoPlatformBuilder {
	b.useParallelEngine = true
	return b
}

// WithISADebugging enables ISA debugging in the simulation.
func (b R9NanoPlatformBuilder) WithISADebugging() R9NanoPlatformBuilder {
	b.debugISA = true
	return b
}

// WithVisTracing lets the platform to record traces for visualization purposes.
func (b R9NanoPlatformBuilder) WithVisTracing() R9NanoPlatformBuilder {
	b.traceVis = true
	return b
}

// WithPartialVisTracing lets the platform to record traces for visualization
// purposes. The trace will only be collected from the start time to the end
// time.
func (b R9NanoPlatformBuilder) WithPartialVisTracing(
	start, end sim.VTimeInSec,
) R9NanoPlatformBuilder {
	b.traceVis = true
	b.visTraceStartTime = start
	b.visTraceEndTime = end

	return b
}

// WithMemTracing lets the platform to trace memory operations.
func (b R9NanoPlatformBuilder) WithMemTracing() R9NanoPlatformBuilder {
	b.traceMem = true
	return b
}

// WithLog2PageSize sets the page size as a power of 2.
func (b R9NanoPlatformBuilder) WithLog2PageSize(
	n uint64,
) R9NanoPlatformBuilder {
	b.log2PageSize = n
	return b
}

// WithMonitor sets the monitor that is used to monitor the simulation
func (b R9NanoPlatformBuilder) WithMonitor(
	m *monitoring.Monitor,
) R9NanoPlatformBuilder {
	b.monitor = m
	return b
}

func (b R9NanoPlatformBuilder) WithPerfAnalyzer(
	Name string,
	Period float64,
) R9NanoPlatformBuilder {
	b.perfAnalysisFileName = Name
	b.perfAnalyzingPeriod = Period
	return b
}

// WithMagicMemoryCopy uses global storage as memory components
func (b R9NanoPlatformBuilder) WithMagicMemoryCopy() R9NanoPlatformBuilder {
	b.useMagicMemoryCopy = true
	return b
}

// WithBandwidth sets the bandwidth between adjacent GPUs in the unit of 16GB/s.
func (b R9NanoPlatformBuilder) WithBandwidth(
	bandwidth int,
) R9NanoPlatformBuilder {
	b.bandwidth = bandwidth
	return b
}

// WithSwitchLatency sets the switch latency.
func (b R9NanoPlatformBuilder) WithSwitchLatency(
	latency int,
) R9NanoPlatformBuilder {
	b.switchLatency = latency
	return b
}

// WithMaxNumHops sets the maximum number of hops that a flit can travel in the
// mesh network.
func (b R9NanoPlatformBuilder) WithMaxNumHops(
	n int,
) R9NanoPlatformBuilder {
	b.maxNumHops = n
	return b
}

// Build builds a platform with R9Nano GPUs.
func (b R9NanoPlatformBuilder) Build(numMemoryBank int) *Platform {
	b.engine = b.createEngine()
	if b.monitor != nil {
		b.monitor.RegisterEngine(b.engine)
	}

	b.setupVisTracing()
	b.setupPerfermanceTracing()

	numGPU := b.tileWidth*b.tileHeight - 1
	b.globalStorage = mem.NewStorage(uint64(1+numGPU) * 8 * mem.GB)

	rdmaAddressTable := b.createRDMAAddrTable()
	pmcAddressTable := b.createPMCPageTable()
	gmmuTLBTable := b.createGMMUTLBTable()

	mmuComponent, pageTable := b.createMMU(b.engine, gmmuTLBTable)
	b.mmu = mmuComponent

	b.createIOMMUCache()
	b.createIOMMUTLB(gmmuTLBTable, pageTable)
	// b.mmu.TopModule = b.IOMMUTLB.GetPortByName("Bottom")

	// b.connIOMMUWithIOMMUTLB()

	b.connIOMMUCacheWithIOMMUTLB()
	b.mmu.TopModule = b.IOMMUCache.GetPortByName("Bottom")
	b.IOMMUCache.UpModule = b.IOMMUTLB.GetPortByName("Bottom")

	b.connIOMMUWithIOMMUCache()
	// b.createIOMMUTLB(gmmuTLBTable)
	// b.mmu.TopModule = b.IOMMUTLB.GetPortByName("Bottom")

	// b.connIOMMUWithIOMMUTLB()

	gpuDriverBuilder := driver.MakeBuilder()
	if b.useMagicMemoryCopy {
		gpuDriverBuilder = gpuDriverBuilder.WithMagicMemoryCopyMiddleware()
	}
	gpuDriver := gpuDriverBuilder.
		WithEngine(b.engine).
		WithPageTable(pageTable).
		WithLog2PageSize(b.log2PageSize).
		WithGlobalStorage(b.globalStorage).
		WithMemorySize(8 * mem.GB).
		Build("Driver")
	// file, err := os.Create("driver_comm.csv")
	// if err != nil {
	// 	panic(err)
	// }
	// gpuDriver.GetPortByName("GPU").AcceptHook(
	// 	sim.NewPortMsgLogger(log.New(file, "", 0)))

	if b.monitor != nil {
		b.monitor.RegisterComponent(gpuDriver)
	}

	// connector := b.createConnection(b.engine, gpuDriver, mmuComponent)
	connector := b.createConnection(b.engine, gpuDriver)

	gpuBuilder := b.createGPUBuilder(b.engine, gpuDriver, mmuComponent, numMemoryBank, pageTable)

	mmuComponent.MigrationServiceProvider = gpuDriver.GetPortByName("MMU")

	b.createGPUs(
		connector,
		gpuBuilder, gpuDriver,
		rdmaAddressTable,
		pmcAddressTable,
		gmmuTLBTable)

	connector.EstablishNetwork()

	for _, gpu := range b.gpus {
		gpu.MMUEngine = mmuComponent
	}

	disassembler := insts.NewDisassembler()
	emu.CreateUniqSampledComputeUnit(
		"cu", gpuBuilder.freq, disassembler,
		pageTable, b.log2PageSize, b.globalStorage, nil)
	emu.CreateUniqBBVComputeUnit(
		"cu", gpuBuilder.freq, disassembler,
		pageTable, b.log2PageSize, b.globalStorage, nil)
	emu.CreateUniqStaticComputeUnit(
		"cu", gpuBuilder.freq, disassembler,
		pageTable, b.log2PageSize, b.globalStorage, nil)
	for _, gpu := range b.gpus {
		name := fmt.Sprintf("GPU%dCU", gpu.GPUID)
		emu.CreateSampledComputeUnitForGPU(
			gpu.GPUID,
			name,
			gpuBuilder.freq,
			disassembler,
			pageTable,
			b.log2PageSize,
			b.globalStorage,
			nil)
		emu.CreateStaticComputeUnitForGPU(
			gpu.GPUID,
			name,
			gpuBuilder.freq,
			disassembler,
			pageTable,
			b.log2PageSize,
			b.globalStorage,
			nil)
	}

	return &Platform{
		Engine:   b.engine,
		Driver:   gpuDriver,
		GPUs:     b.gpus,
		IOMMUTLB: b.IOMMUTLB,
	}
}

func (b *R9NanoPlatformBuilder) setupVisTracing() {
	if !b.traceVis {
		return
	}

	var backend tracing.TracerBackend
	switch *visTracerDB {
	case "sqlite":
		be := tracing.NewSQLiteTraceWriter(*visTracerDBFileName)
		be.Init()
		backend = be
	case "csv":
		be := tracing.NewCSVTraceWriter(*visTracerDBFileName)
		be.Init()
		backend = be
	case "mysql":
		be := tracing.NewMySQLTraceWriter()
		be.Init()
		backend = be
	default:
		panic(fmt.Sprintf(
			"Tracer database type must be [sqlite|csv|mysql]. "+
				"Provided value %s is not supported.",
			*visTracerDB))
	}

	visTracer := tracing.NewDBTracer(b.engine, backend)
	visTracer.SetTimeRange(b.visTraceStartTime, b.visTraceEndTime)

	b.visTracer = visTracer
}

func (b *R9NanoPlatformBuilder) createGPUs(
	connector *mesh.Connector,
	gpuBuilder R9NanoGPUBuilder,
	gpuDriver *driver.Driver,
	rdmaAddressTable *mem.BankedLowModuleFinder,
	pmcAddressTable *mem.BankedLowModuleFinder,
	gmmuTLBTable *mem.MultiPageFinder,
) {
	for y := 0; y < b.tileHeight; y++ {
		for x := 0; x < b.tileWidth; x++ {
			if x == b.tileWidth/2 && y == b.tileHeight/2 {
				continue
			}

			b.createGPU(x, y, gpuBuilder,
				gpuDriver, rdmaAddressTable,
				pmcAddressTable, connector,
				gmmuTLBTable)
		}
	}
}

func (b R9NanoPlatformBuilder) createPMCPageTable() *mem.BankedLowModuleFinder {
	pmcAddressTable := new(mem.BankedLowModuleFinder)
	pmcAddressTable.BankSize = 8 * mem.GB
	pmcAddressTable.LowModules = append(pmcAddressTable.LowModules, nil)
	return pmcAddressTable
}

func (b R9NanoPlatformBuilder) createRDMAAddrTable() *mem.BankedLowModuleFinder {
	rdmaAddressTable := new(mem.BankedLowModuleFinder)
	rdmaAddressTable.BankSize = 8 * mem.GB
	rdmaAddressTable.LowModules = append(rdmaAddressTable.LowModules, nil)
	return rdmaAddressTable
}

func (b R9NanoPlatformBuilder) createConnection(
	engine sim.Engine,
	gpuDriver *driver.Driver,
	// mmuComponent *mmu.MMU,
) *mesh.Connector {
	connector := mesh.NewConnector().
		WithEngine(engine).
		WithFreq(1 * sim.GHz).
		WithFlitSize(16).
		WithBandwidth(float64(b.bandwidth)).
		WithSwitchLatency(b.switchLatency)

	if b.traceVis {
		connector = connector.WithVisTracer(b.visTracer)
	}

	connector.CreateNetwork("Mesh")
	connector.AddTile([3]int{b.tileWidth / 2, b.tileHeight / 2, 0}, []sim.Port{
		gpuDriver.GetPortByName("GPU"),
		// gpuDriver.GetPortByName("MMUCache"),
		// b.IOMMU.GetPortByName("Migration"),
		b.IOMMUTLB.GetPortByName("Top"),
	})

	return connector
}

func (b R9NanoPlatformBuilder) createEngine() sim.Engine {
	var engine sim.Engine

	if b.useParallelEngine {
		engine = sim.NewParallelEngine()
	} else {
		engine = sim.NewSerialEngine()
	}
	// engine.AcceptHook(sim.NewEventLogger(log.New(os.Stdout, "", 0)))

	return engine
}

func (b R9NanoPlatformBuilder) createMMU(
	engine sim.Engine,
	gmmuCacheTable *mem.MultiPageFinder,
) (*mmu.MMU, vm.PageTable) {
	pageTable := vm.NewPageTable(b.log2PageSize)
	mmuBuilder := mmu.MakeBuilder().
		WithEngine(engine).
		WithFreq(1 * sim.GHz).
		WithPageWalkingLatency(500).
		WithLog2PageSize(b.log2PageSize).
		WithMaxNumReqInFlight(16).
		WithPageTable(pageTable).
		WithGMMUCacheTable(gmmuCacheTable).
		WithWalkCoalescing(*mmuWalkCoalescing).
		WithMMUTopModule(b.mmuTopModule)

	mmuComponent := mmuBuilder.Build("MMU")

	if b.monitor != nil {
		b.monitor.RegisterComponent(mmuComponent)
	}

	if b.perfAnalyzer != nil {
		b.perfAnalyzer.RegisterComponent(mmuComponent)
	}

	if b.visTracer != nil {
		tracing.CollectTrace(mmuComponent, b.visTracer)
	}

	return mmuComponent, pageTable
}

func (b *R9NanoPlatformBuilder) createGPUBuilder(
	engine sim.Engine,
	gpuDriver *driver.Driver,
	mmuComponent *mmu.MMU,
	numMemoryBank int,
	pageTable vm.PageTable,
) R9NanoGPUBuilder {
	gpuBuilder := MakeR9NanoGPUBuilder().
		WithEngine(engine).
		WithMMU(mmuComponent).
		WithIOMMUCache(b.IOMMUTLB).
		WithNumCUPerShaderArray(b.numCUPerSA).
		WithNumShaderArray(b.numSAPerGPU).
		WithNumMemoryBank(numMemoryBank).
		WithL2CacheSize(4 * mem.MB).
		WithLog2MemoryBankInterleavingSize(7).
		WithLog2PageSize(b.log2PageSize).
		WithGlobalStorage(b.globalStorage).
		WithPerfAnalyzer(b.perfAnalyzer).
		WithGMMUPageTable(pageTable)

	if b.monitor != nil {
		gpuBuilder = gpuBuilder.WithMonitor(b.monitor)
	}

	gpuBuilder = b.setVisTracer(gpuDriver, gpuBuilder)
	gpuBuilder = b.setMemTracer(gpuBuilder)
	gpuBuilder = b.setISADebugger(gpuBuilder)

	return gpuBuilder
}

func (b *R9NanoPlatformBuilder) setISADebugger(
	gpuBuilder R9NanoGPUBuilder,
) R9NanoGPUBuilder {
	if !b.debugISA {
		return gpuBuilder
	}

	gpuBuilder = gpuBuilder.WithISADebugging()
	return gpuBuilder
}

func (b *R9NanoPlatformBuilder) setMemTracer(
	gpuBuilder R9NanoGPUBuilder,
) R9NanoGPUBuilder {
	if !b.traceMem {
		return gpuBuilder
	}

	file, err := os.Create("mem.trace")
	if err != nil {
		panic(err)
	}
	logger := log.New(file, "", 0)
	memTracer := memtraces.NewTracer(logger, b.engine)
	gpuBuilder = gpuBuilder.WithMemTracer(memTracer)
	return gpuBuilder
}

func (b *R9NanoPlatformBuilder) setVisTracer(
	gpuDriver *driver.Driver,
	gpuBuilder R9NanoGPUBuilder,
) R9NanoGPUBuilder {
	if b.traceVis {
		gpuBuilder = gpuBuilder.WithVisTracer(b.visTracer)
	}

	return gpuBuilder
}

func (b *R9NanoPlatformBuilder) createGPU(
	x, y int,
	gpuBuilder R9NanoGPUBuilder,
	gpuDriver *driver.Driver,
	rdmaAddressTable *mem.BankedLowModuleFinder,
	pmcAddressTable *mem.BankedLowModuleFinder,
	connector *mesh.Connector,
	gmmuTLBTable *mem.MultiPageFinder,
) *GPU {
	index := uint64(len(b.gpus)) + 1
	gpuid := x + y*b.tileWidth
	name := fmt.Sprintf("GPU[%d]", gpuid)
	memAddrOffset := index * 8 * mem.GB
	// fmt.Printf("GPU[%d], index %d, memAddrOffset %X\n", gpuid, index, memAddrOffset)
	gpu := gpuBuilder.
		WithMemAddrOffset(memAddrOffset).
		WithGMMUCacheTable(gmmuTLBTable).
		Build(name, uint64(index))
	gpuDriver.RegisterGPU(gpu.Domain.GetPortByName("CommandProcessor"),
		driver.DeviceProperties{
			CUCount:  32,
			DRAMSize: 8 * mem.GB,
		})
	gpu.CommandProcessor.(*cp.CommandProcessor).Driver =
		gpuDriver.GetPortByName("GPU")

	gpu.GPUID = index

	b.configRDMAEngine(gpu, rdmaAddressTable)
	b.configPMC(gpu, gpuDriver, pmcAddressTable)
	b.configGMMUTLBTable(gpu, gmmuTLBTable)

	connector.AddTile([3]int{x, y, 0}, gpu.Domain.Ports())

	b.gpus = append(b.gpus, gpu)

	return gpu
}

func (b *R9NanoPlatformBuilder) configRDMAEngine(
	gpu *GPU,
	addrTable *mem.BankedLowModuleFinder,
) {
	gpu.RDMAEngine.RemoteRDMAAddressTable = addrTable

	addrTable.LowModules = append(
		addrTable.LowModules,
		gpu.RDMAEngine.ToOutside)
}

func (b *R9NanoPlatformBuilder) configPMC(
	gpu *GPU,
	gpuDriver *driver.Driver,
	addrTable *mem.BankedLowModuleFinder,
) {
	gpu.PMC.RemotePMCAddressTable = addrTable
	addrTable.LowModules = append(
		addrTable.LowModules,
		gpu.PMC.GetPortByName("Remote"))
	gpuDriver.RemotePMCPorts = append(
		gpuDriver.RemotePMCPorts, gpu.PMC.GetPortByName("Remote"))
}

func (b *R9NanoPlatformBuilder) setupPerfermanceTracing() {

	if b.perfAnalysisFileName != "" {
		b.perfAnalyzer = analysis.MakePerfAnalyzerBuilder().
			WithPeriod(sim.VTimeInSec(b.perfAnalyzingPeriod)).
			WithDBFilename(b.perfAnalysisFileName).
			WithEngine(b.engine).
			Build()
	}
}

func (b *R9NanoPlatformBuilder) createGMMUTLBTable() *mem.MultiPageFinder {
	table := mem.NewMultiPageFinder()
	return table
}

func (b *R9NanoPlatformBuilder) configGMMUTLBTable(
	gpu *GPU,
	table *mem.MultiPageFinder,
) {
	gpu.GMMUTLB.PageFinder = table
	table.LowModules[uint64(gpu.GPUID)] = gpu.GMMUTLB.OutsidePort
}

func (b *R9NanoPlatformBuilder) createIOMMUTLB(
	gmmuCacheTable *mem.MultiPageFinder,
	pageTable vm.PageTable,
) {
	name := fmt.Sprintf("IOMMUTLB")
	b.IOMMUTLB = mmuTLB.MakeBuilder().
		WithEngine(b.engine).
		WithFreq(1 * sim.GHz).
		WithNumWays(32).
		WithNumSets(64).
		WithNumMSHREntry(64).
		WithMSHREntryDepth(64).
		WithNumReqPerCycle(32).
		WithPageSize(1 << b.log2PageSize).
		WithLowModule(b.IOMMUCache.GetPortByName("Top")).
		WithPageTable(pageTable).
		WithGMMUCacheTable(gmmuCacheTable).
		WithLog2PageSize(b.log2PageSize).
		WithPerVPNMSHRBaseline(*mmutlbVPNMSHRBaseline).
		WithDemandPTEOnly(*mmutlbDemandPTEOnly).
		WithSetAsLineTLB(*mmutlbFlexTLB).
		WithLookupLatencyCycles(*mmutlbPTCLReturnLatency).
		WithTranslationPrefetcher(*mmutlbPrefetch).
		WithPrefetchDemandPTCLReturn(*mmutlbPrefetchDemandPTCLReturn).
		WithPrefetchAdmissionThreshold(*mmutlbPrefetchAdmission).
		WithPrefetchMaxLearners(*mmutlbPrefetchMaxLearners).
		WithPrefetchLookahead(*mmutlbPrefetchLookahead).
		WithPrefetchMaxCandidatesPerReq(*mmutlbPrefetchMaxCandidates).
		Build(name)

	// b.mmuTopModule = b.IOMMUTLB.GetPortByName("Bottom")

	if b.monitor != nil {
		b.monitor.RegisterComponent(b.IOMMUTLB)
	}
}

func (b *R9NanoPlatformBuilder) connectWithDirectConnection(
	port1, port2 sim.Port,
	bufferSize int,
) {
	conn := sim.NewDirectConnection(
		port1.Name()+"-"+port2.Name(),
		b.engine, 1*sim.GHz,
	)
	conn.PlugIn(port1, bufferSize)
	conn.PlugIn(port2, bufferSize)
}

// func (b *R9NanoPlatformBuilder) connIOMMUWithIOMMUCache() {
// 	conn := sim.NewDirectConnection(
// 		b.IOMMUCache.Name()+".IOMMU",
// 		b.engine, 1*sim.GHz,
// 	)
// 	conn.PlugIn(b.IOMMUCache.GetPortByName("Bottom"), 640)
// 	conn.PlugIn(b.mmu.GetPortByName("Top"), 640)
// }

func (b *R9NanoPlatformBuilder) createIOMMUCache() {
	name := fmt.Sprintf("IOMMUCache")
	b.IOMMUCache = mmuCache.MakeBuilder().
		WithEngine(b.engine).
		WithFreq(1 * sim.GHz).
		WithNumWays(1).
		WithNumSets(32).
		WithNumMSHREntry(64).
		WithMSHREntryDepth(64).
		WithNumReqPerCycle(256).
		WithLog2PageSize(b.log2PageSize).
		// WithUpperModule(b.IOMMUTLB.GetPortByName("Bottom")).
		WithLowModule(b.mmu.GetPortByName("Top")).
		Build(name)

	b.mmuTopModule = b.IOMMUCache.GetPortByName("Bottom")

	if b.monitor != nil {
		b.monitor.RegisterComponent(b.IOMMUCache)
	}
}

func (b *R9NanoPlatformBuilder) connIOMMUWithIOMMUCache() {
	conn := sim.NewDirectConnection(
		b.IOMMUTLB.Name()+".IOMMUCache",
		b.engine, 1*sim.GHz,
	)
	conn.PlugIn(b.IOMMUCache.GetPortByName("Bottom"), 6400)
	conn.PlugIn(b.mmu.GetPortByName("Top"), 6400)
}

func (b *R9NanoPlatformBuilder) connIOMMUCacheWithIOMMUTLB() {
	conn := sim.NewDirectConnection(
		b.IOMMUCache.Name()+".IOMMU",
		b.engine, 1*sim.GHz,
	)
	conn.PlugIn(b.IOMMUTLB.GetPortByName("Bottom"), 6400)
	conn.PlugIn(b.IOMMUCache.GetPortByName("Top"), 6400)
}
