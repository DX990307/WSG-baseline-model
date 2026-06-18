package runner

import (
	"flag"

	"github.com/sarchlab/akita/v3/mem/vm/translationtrace"
)

var timingFlag = flag.Bool("timing", false, "Run detailed timing simulation.")
var maxInstCount = flag.Uint64("max-inst", 0,
	"Terminate the simulation after the given number of instructions is retired.")
var maxWGCount = flag.Uint64("max-wg", 0,
	"Terminate the simulation after the given number of workgroups is retired.")
var parallelFlag = flag.Bool("parallel", false,
	"Run the simulation in parallel.")
var isaDebug = flag.Bool("debug-isa", false, "Generate the ISA debugging file.")
var visTracing = flag.Bool("trace-vis", false,
	"Generate trace for visualization purposes.")
var visTraceStartTime = flag.Float64("trace-vis-start", -1,
	"The starting time to collect visualization traces. A negative number "+
		"represents starting from the beginning.")
var visTraceEndTime = flag.Float64("trace-vis-end", -1,
	"The end time of collecting visualization traces. A negative number"+
		"means that the trace will be collected to the end of the simulation.")
var verifyFlag = flag.Bool("verify", false, "Verify the emulation result.")
var memTracing = flag.Bool("trace-mem", false, "Generate memory trace")
var instCountReportFlag = flag.Bool("report-inst-count", false,
	"Report the number of instructions executed in each compute unit.")
var cacheLatencyReportFlag = flag.Bool("report-cache-latency", false,
	"Report the average cache latency.")
var cacheHitRateReportFlag = flag.Bool("report-cache-hit-rate", false,
	"Report the cache hit rate of each cache.")
var tlbHitRateReportFlag = flag.Bool("report-tlb-hit-rate", false,
	"Report the TLB hit rate of each TLB.")
var rdmaTransactionCountReportFlag = flag.Bool("report-rdma-transaction-count",
	false, "Report the number of transactions going through the RDMA engines.")
var dramTransactionCountReportFlag = flag.Bool("report-dram-transaction-count",
	false, "Report the number of transactions accessing the DRAMs.")
var useUnifiedMemoryFlag = flag.Bool("use-unified-memory", false,
	"Run benchmark with Unified Memory or not")
var reportAll = flag.Bool("report-all", false, "Report all metrics to .csv file.")
var filenameFlag = flag.String("metric-file-name", "metrics",
	"Modify the name of the output csv file.")
var translationTraceFlag = flag.Bool("translation-trace", false,
	"Collect translation pressure time-series and request-stage breakdown CSVs.")
var translationTraceWindowCycles = flag.Uint64("translation-trace-window-cycles", 10000,
	"Window size in cycles for translation pressure time-series CSV.")
var translationTraceFile = flag.String("translation-trace-file", "",
	"Output file prefix for translation trace CSVs. Defaults to -metric-file-name.")
var magicMemoryCopy = flag.Bool("magic-memory-copy", false,
	"Copy data from CPU directly to global memory")
var switchLatencyFlag = flag.Int("switch-latency", 20,
	"The latency of the switch")
var bandwidthFlag = flag.Int("bandwidth", 1,
	"The bandwidth of the network as a multiple of 16GB/s.")
var maxNumHopsFlag = flag.Int("max-num-hops", -1,
	"The maximum number of hops in the network")
var numMemBankFlag = flag.Int("num-memory-banks", 16,
	"The maximum number of hops in the network")
var analyszerNameFlag = flag.String("analyzer-Name", "",
	"The name of the analyzer to use.")
var analyszerPeriodFlag = flag.Float64("analyzer-period", 0.0,
	"The period to dump the analyzer results.")
var visTracerDB = flag.String("trace-vis-db", "sqlite",
	"The database to store the visualization trace. Possible values are "+
		"sqlite, mysql, and csv.")
var visTracerDBFileName = flag.String("trace-vis-db-file", "",
	"The file name of the database to store the visualization trace. "+
		"Extension names are not required. "+
		"If not specified, a random file name will be used. "+
		"This flag does not work with Mysql db. When MySQL is used, "+
		"the database name is always randomly generated.")
var gmmuPTCLThresholdLow = flag.Int("gmmu-ptcl-threshold-low", 4,
	"The low threshold of the coalescing score for switching the GMMU L2 TLB back to PTE mode.")
var gmmuPTCLThresholdHigh = flag.Int("gmmu-ptcl-threshold-high", 16,
	"The high threshold of the coalescing score for switching the GMMU L2 TLB into PTCL mode.")
var gmmuInitialPTCLMode = flag.Bool("gmmu-initial-ptcl-mode", false,
	"Whether the GMMU L2 TLB starts in PTCL coalescing mode.")
var gmmuVPNMSHRBaseline = flag.Bool("gmmu-vpn-mshr-baseline", false,
	"Use a per-VPN GMMU L2 TLB MSHR baseline instead of PTCL-granularity MSHRs.")
var gmmuPTELookupLatency = flag.Int("gmmu-pte-lookup-latency", 32,
	"Fixed GMMU L2 TLB lookup latency per internal PTE lookup job, in cycles. PTCL mode can issue multiple lookup jobs in parallel.")
var gmmuPTCLSerialLookup = flag.Bool("gmmu-ptcl-serial-lookup", false,
	"Model non-flex GMMU PTCL lookup as one serial bitmap lookup whose latency is requested bits times gmmu-pte-lookup-latency.")
var gmmuFlexTLB = flag.Bool("gmmu-flex-tlb", false,
	"Enable the Flex-PTCL/PTE entry format in the GMMU L2 TLB.")
var gmmuFlexPromotionThreshold = flag.Int("gmmu-flex-promotion-threshold", 3,
	"The minimum valid response bits required before Flex stores a PTCL-line entry.")
var gmmuPrefetch = flag.Bool("gmmu-prefetch", false,
	"Enable the BO-aware PTCL translation prefetcher in the GMMU L2 TLB.")
var gmmuPrefetchAdmission = flag.Int("gmmu-prefetch-admission", 3,
	"The number of unique (GPM, PTCL) demand observations required before a GMMU prefetch learner is admitted.")
var gmmuPrefetchMaxLearners = flag.Int("gmmu-prefetch-max-learners", 4,
	"The maximum number of active BO-local translation prefetch learners retained in the GMMU L2 TLB.")
var gmmuPrefetchLookahead = flag.Int("gmmu-prefetch-lookahead", 64,
	"The maximum adaptive intra-GPM PTCL lookahead used by the GMMU L2 TLB prefetcher.")
var gmmuPrefetchMaxCandidates = flag.Int("gmmu-prefetch-max-candidates", 4,
	"The maximum number of GMMU prefetch candidates generated per demand request.")
var mmuWalkCoalescing = flag.Bool("mmu-walk-coalescing", false,
	"Enable MMU page-walk coalescing independently of the MMUTLB prefetcher.")
var mmutlbVPNMSHRBaseline = flag.Bool("mmutlb-vpn-mshr-baseline", false,
	"Use a per-VPN MMUTLB/IOTLB MSHR baseline instead of PTCL-granularity MSHRs.")
var mmutlbPrefetch = flag.Bool("mmutlb-prefetch", false,
	"Enable the BO-aware PTCL translation prefetcher in the MMUTLB.")
var mmutlbDemandPTEOnly = flag.Bool("mmutlb-demand-pte-only", false,
	"Force demand requests in the MMUTLB/IOTLB to issue and return only the requested PTE, while allowing PTCL-level prefetching to remain enabled.")
var mmutlbFlexTLB = flag.Bool("mmutlb-flex-tlb", false,
	"Enable PTCL set-as-line lookup/fill in the MMUTLB/IOTLB when PTCL-granularity MSHR coalescing is active.")
var mmutlbPTCLReturnLatency = flag.Int("mmutlb-ptcl-return-latency", 80,
	"Fixed MMUTLB/IOTLB lookup latency per requested PTE (per bitmap bit), in cycles, applied before each buffered translation request is looked up.")
var mmutlbPrefetchDemandPTCLReturn = flag.Bool("mmutlb-prefetch-demand-ptcl-return", false,
	"When a BO learner is confirmed, promote demand requests in the MMUTLB to PTCL-granularity returns.")
var mmutlbPrefetchAdmission = flag.Int("mmutlb-prefetch-admission", 6,
	"The number of unique (GPM, PTCL) demand observations required before a BO learner is admitted.")
var mmutlbPrefetchMaxLearners = flag.Int("mmutlb-prefetch-max-learners", 4,
	"The maximum number of active BO-local translation prefetch learners retained in the MMUTLB.")
var mmutlbPrefetchLookahead = flag.Int("mmutlb-prefetch-lookahead", 2,
	"The number of intra-GPM PTCL steps ahead predicted by the MMUTLB prefetcher.")
var mmutlbPrefetchMaxCandidates = flag.Int("mmutlb-prefetch-max-candidates", 4,
	"The maximum number of selected prefetch candidates generated per demand request.")
var disableServersFlag = flag.Bool("disable-servers", false,
	"Disable profiling and monitoring servers. Useful for automated tests.")
var log2PageSizeFlag = flag.Uint64("log2-page-size", 12,
	"GPU page size as log2(bytes). For example 12=4KB, 14=16KB, 15=32KB, 21=2MB.")

func configuredLog2PageSize() uint64 {
	return *log2PageSizeFlag
}

// ParseFlag applies the runner flag to runner object
//
//nolint:gocyclo
func (r *Runner) ParseFlag() *Runner {
	if *parallelFlag {
		r.Parallel = true
	}

	if *verifyFlag {
		r.Verify = true
	}

	if *timingFlag {
		r.Timing = true
	}

	if *useUnifiedMemoryFlag {
		r.UseUnifiedMemory = true
	}

	if *translationTraceFlag {
		prefix := *translationTraceFile
		if prefix == "" {
			prefix = *filenameFlag
		}
		translationtrace.Configure(
			true,
			prefix,
			*translationTraceWindowCycles,
		)
	} else {
		translationtrace.Configure(false, "", 0)
	}

	if *instCountReportFlag {
		r.ReportInstCount = true
	}

	if *cacheLatencyReportFlag {
		r.ReportCacheLatency = true
	}

	if *cacheHitRateReportFlag {
		r.ReportCacheHitRate = true
	}

	if *tlbHitRateReportFlag {
		r.ReportTLBHitRate = true
	}

	if *dramTransactionCountReportFlag {
		r.ReportDRAMTransactionCount = true
	}

	if *rdmaTransactionCountReportFlag {
		r.ReportRDMATransactionCount = true
	}

	if *reportAll {
		r.ReportInstCount = true
		r.ReportCacheLatency = true
		r.ReportCacheHitRate = true
		r.ReportTLBHitRate = true
		r.ReportDRAMTransactionCount = true
		r.ReportRDMATransactionCount = true
		r.ReportRDMALatency = true
		r.ReportTLBLatency = true
		r.ReportGMMULatency = true
		r.ReportMMULatency = true
		r.ReportGMMUTransactionCount = true
		r.ReportMMUTransactionCount = true
		r.ReportSIMDBusyTime = true
		r.ReportGMMUCacheHitRate = true
		r.ReportGMMUCacheLatency = true
	}

	if *disableServersFlag {
		r.DisableServers = true
	}

	return r
}
