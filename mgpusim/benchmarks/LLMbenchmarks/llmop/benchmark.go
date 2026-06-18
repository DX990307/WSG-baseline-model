// Package llmop defines one-op synthetic LLM benchmarks.
package llmop

import (
	"flag"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/sarchlab/mgpusim/v3/benchmarks"
	"github.com/sarchlab/mgpusim/v3/benchmarks/LLMbenchmarks/operators"
	"github.com/sarchlab/mgpusim/v3/driver"
)

var _ benchmarks.Benchmark = (*Benchmark)(nil)

var (
	opFlag = flag.String("op", "linear",
		"LLM op: embedding, linear, split-linear, transfer, layernorm, gelu, residual-add, batchnorm2d, row-softmax, causal-mask, attention, causal-attention, or mlp.")
	rowsFlag = flag.Int(
		"rows", 8192, "Row count, usually batch-size * seq-len.")
	colsFlag = flag.Int(
		"cols", 0, "Column count for row-softmax, or width for batchnorm2d.")
	hiddenFlag = flag.Int(
		"hidden", 0, "Transformer hidden size.")
	inputDimFlag = flag.Int(
		"input-dim", 8192, "Input dimension for linear.")
	outputDimFlag = flag.Int(
		"output-dim", 8192, "Output dimension for linear, or intermediate size for mlp.")
	elementsFlag = flag.Int(
		"elements", 0, "Element count for elementwise ops.")
	seqLenFlag = flag.Int(
		"seq-len", 0, "Sequence length for attention masks.")
	batchSizeFlag = flag.Int(
		"batch-size", 1, "Batch size for attention masks.")
	splitKFlag = flag.Int(
		"split-k", 1, "K-dimension split count for split-linear.")
	logSubtasksFlag = flag.Bool(
		"llmop-log-subtasks", false, "Print LLM op subtask progress.")
	transferBytesFlag = flag.Int(
		"transfer-bytes", 0,
		"Byte count for -op=transfer. If unset, uses -elements*4 or -rows*-hidden*4.")
	srcGPUsFlag = flag.String(
		"src-gpus", "all",
		"Source tensor placement for -op=transfer: all, first, or a GPU list/range like 1,2,5-8.")
	dstGPUsFlag = flag.String(
		"dst-gpus", "first",
		"Destination tensor placement for -op=transfer: all, first, or a GPU list/range like 1,2,5-8.")
	copyGPUsFlag = flag.String(
		"copy-gpus", "dst",
		"GPU(s) launching -op=transfer copy: dst, src, all, first, or a GPU list/range.")
)

// Benchmark runs one synthetic transformer sub-operator.
type Benchmark struct {
	driver *driver.Driver
	ctx    *driver.Context
	gpus   []int

	Op            string
	Rows          int
	Cols          int
	Hidden        int
	InputDim      int
	OutputDim     int
	Elements      int
	SeqLen        int
	BatchSize     int
	SplitK        int
	TransferBytes int
	SrcGPUs       string
	DstGPUs       string
	CopyGPUs      string

	LogSubtasks bool

	to  *operators.GPUOperator
	ops *operators.Operator

	useUnifiedMemory bool
}

// NewBenchmark creates an LLM op benchmark.
func NewBenchmark(driver *driver.Driver) *Benchmark {
	return &Benchmark{
		driver:    driver,
		ctx:       driver.Init(),
		BatchSize: 1,
	}
}

// NewBenchmarkFromFlags creates an LLM op benchmark using package flags.
func NewBenchmarkFromFlags(driver *driver.Driver) *Benchmark {
	b := NewBenchmark(driver)
	b.ApplyFlags()
	return b
}

// ApplyFlags copies package flag values into the benchmark.
func (b *Benchmark) ApplyFlags() {
	b.Op = *opFlag
	b.Rows = *rowsFlag
	b.Cols = *colsFlag
	b.Hidden = *hiddenFlag
	b.InputDim = *inputDimFlag
	b.OutputDim = *outputDimFlag
	b.Elements = *elementsFlag
	b.SeqLen = *seqLenFlag
	b.BatchSize = *batchSizeFlag
	b.SplitK = *splitKFlag
	b.TransferBytes = *transferBytesFlag
	b.SrcGPUs = *srcGPUsFlag
	b.DstGPUs = *dstGPUsFlag
	b.CopyGPUs = *copyGPUsFlag
	b.LogSubtasks = *logSubtasksFlag
}

// SelectGPU selects GPUs.
func (b *Benchmark) SelectGPU(gpus []int) {
	b.gpus = gpus
}

// SetUnifiedMemory records the unified memory preference.
func (b *Benchmark) SetUnifiedMemory() {
	b.useUnifiedMemory = true
}

// Run executes the selected op.
func (b *Benchmark) Run() {
	if len(b.gpus) == 0 {
		log.Panic("llmop benchmark requires at least one GPU")
	}
	b.driver.SelectGPU(b.ctx, b.gpus[0])
	b.to = operators.NewGPUOperator(b.driver, b.ctx)
	b.to.ReportTime()
	b.ops = operators.NewOperator(
		b.driver, b.ctx, b.to, "LLMOp", b.LogSubtasks)

	switch b.Op {
	case "embedding":
		b.runEmbedding()
	case "linear":
		b.runLinear()
	case "split-linear":
		b.runSplitLinear()
	case "transfer":
		b.runTransfer()
	case "layernorm":
		b.runLayerNorm()
	case "gelu":
		b.runGELU()
	case "residual-add":
		b.runResidualAdd()
	case "batchnorm2d":
		b.runBatchNorm2D()
	case "row-softmax":
		b.runRowSoftmax()
	case "causal-mask":
		b.runCausalMask()
	case "attention":
		b.runAttention(false)
	case "causal-attention":
		b.runAttention(true)
	case "mlp":
		b.runMLP()
	default:
		log.Panicf("unknown -op %q", b.Op)
	}
}

// Verify is intentionally not implemented for synthetic one-op benchmarks.
func (b *Benchmark) Verify() {
}

func (b *Benchmark) runEmbedding() {
	b.requirePositive("rows", b.Rows)
	b.requirePositive("hidden", b.Hidden)
	out := b.ops.Embedding("embedding", b.Rows, b.Hidden)
	b.ops.Free(out)
}

func (b *Benchmark) runLinear() {
	b.requirePositive("rows", b.Rows)
	b.requirePositive("input-dim", b.InputDim)
	b.requirePositive("output-dim", b.OutputDim)
	input := b.ops.Input("linear", []int{b.Rows, b.InputDim})
	out := b.ops.Linear("linear", input, b.Rows, b.InputDim, b.OutputDim)
	b.ops.Free(input)
	b.ops.Free(out)
}

func (b *Benchmark) runSplitLinear() {
	b.requirePositive("rows", b.Rows)
	b.requirePositive("input-dim", b.InputDim)
	b.requirePositive("output-dim", b.OutputDim)
	b.requirePositive("split-k", b.SplitK)
	input := b.ops.Input("split linear", []int{b.Rows, b.InputDim})
	out := b.ops.SplitKLinear(
		"split linear", input, b.Rows, b.InputDim, b.OutputDim, b.SplitK)
	b.ops.Free(input)
	b.ops.Free(out)
}

func (b *Benchmark) runTransfer() {
	byteCount := b.transferByteCount()
	srcGPUs := b.resolveGPUList(b.SrcGPUs, nil, nil)
	dstGPUs := b.resolveGPUList(b.DstGPUs, srcGPUs, nil)
	copyGPUs := b.resolveGPUList(b.CopyGPUs, srcGPUs, dstGPUs)

	log.Printf(
		"LLM transfer bytes=%d src=%v dst=%v copy=%v",
		byteCount, srcGPUs, dstGPUs, copyGPUs)

	b.driver.SelectGPU(b.ctx, srcGPUs[0])
	src := b.driver.AllocateMemory(b.ctx, uint64(byteCount))
	if len(srcGPUs) > 1 {
		b.driver.Distribute(b.ctx, src, uint64(byteCount), srcGPUs)
	}

	b.driver.SelectGPU(b.ctx, dstGPUs[0])
	dst := b.driver.AllocateMemory(b.ctx, uint64(byteCount))
	if len(dstGPUs) > 1 {
		b.driver.Distribute(b.ctx, dst, uint64(byteCount), dstGPUs)
	}

	copyDevice := b.copyDevice(copyGPUs)
	b.driver.SelectGPU(b.ctx, copyDevice)
	b.driver.MemCopyD2D(b.ctx, dst, src, byteCount)

	_ = b.driver.FreeMemory(b.ctx, src)
	_ = b.driver.FreeMemory(b.ctx, dst)
}

func (b *Benchmark) runLayerNorm() {
	b.requirePositive("rows", b.Rows)
	b.requirePositive("hidden", b.Hidden)
	input := b.ops.Input("layernorm", []int{b.Rows, b.Hidden})
	out := b.ops.LayerNorm("layernorm", input, b.Rows, b.Hidden)
	b.ops.Free(input)
	b.ops.Free(out)
}

func (b *Benchmark) runGELU() {
	elements := b.elementCount()
	input := b.ops.Input("gelu", []int{elements})
	out := b.ops.GELU("gelu", input)
	b.ops.Free(input)
	b.ops.Free(out)
}

func (b *Benchmark) runResidualAdd() {
	elements := b.elementCount()
	a := b.ops.Input("residual a", []int{elements})
	c := b.ops.Input("residual b", []int{elements})
	out := b.ops.ResidualAdd("residual", a, c)
	b.ops.Free(a)
	b.ops.Free(c)
	b.ops.Free(out)
}

func (b *Benchmark) runBatchNorm2D() {
	b.requirePositive("rows", b.Rows)
	b.requirePositive("hidden", b.Hidden)
	b.requirePositive("seq-len", b.SeqLen)
	if b.Cols <= 0 {
		b.Cols = b.SeqLen
	}
	b.requirePositive("cols", b.Cols)
	input := b.ops.Input("batchnorm2d", []int{
		b.Rows, b.Hidden, b.SeqLen, b.Cols,
	})
	out := b.ops.BatchNorm2DInference("batchnorm2d", input)
	b.ops.Free(input)
	b.ops.Free(out)
}

func (b *Benchmark) runRowSoftmax() {
	b.requirePositive("rows", b.Rows)
	if b.Cols <= 0 {
		b.Cols = b.Rows
	}
	b.requirePositive("cols", b.Cols)
	input := b.ops.Input("row softmax", []int{b.Rows, b.Cols})
	out := b.ops.RowSoftmax(input, b.Rows, b.Cols)
	b.ops.Free(input)
	b.ops.Free(out)
}

func (b *Benchmark) runCausalMask() {
	b.requirePositive("rows", b.Rows)
	b.requirePositive("seq-len", b.SeqLen)
	b.requirePositive("batch-size", b.BatchSize)
	scores := b.ops.Input("causal mask", []int{b.Rows, b.Rows})
	b.ops.ApplyCausalMask(scores, b.Rows, b.SeqLen, b.BatchSize)
	b.ops.Free(scores)
}

func (b *Benchmark) runAttention(causal bool) {
	b.requirePositive("rows", b.Rows)
	b.requirePositive("hidden", b.Hidden)
	b.requirePositive("seq-len", b.SeqLen)
	b.requirePositive("batch-size", b.BatchSize)
	input := b.ops.Input("attention", []int{b.Rows, b.Hidden})
	out := b.ops.SelfAttention(
		"attention", input, b.Rows, b.Hidden, 1, b.SeqLen, b.BatchSize, causal)
	b.ops.Free(input)
	b.ops.Free(out)
}

func (b *Benchmark) runMLP() {
	b.requirePositive("rows", b.Rows)
	b.requirePositive("hidden", b.Hidden)
	b.requirePositive("output-dim", b.OutputDim)
	input := b.ops.Input("mlp", []int{b.Rows, b.Hidden})
	out := b.ops.MLP("mlp", input, b.Rows, b.Hidden, b.OutputDim)
	b.ops.Free(input)
	b.ops.Free(out)
}

func (b *Benchmark) elementCount() int {
	if b.Elements > 0 {
		return b.Elements
	}
	if b.Rows > 0 && b.Hidden > 0 {
		return b.Rows * b.Hidden
	}
	log.Panic("set -elements, or set both -rows and -hidden")
	return 0
}

func (b *Benchmark) transferByteCount() int {
	if b.TransferBytes > 0 {
		return b.TransferBytes
	}

	elements := b.elementCount()
	byteCount := elements * 4
	if byteCount <= 0 {
		log.Panic("-transfer-bytes or element count must be > 0")
	}
	return byteCount
}

func (b *Benchmark) resolveGPUList(spec string, src, dst []int) []int {
	spec = strings.TrimSpace(strings.ToLower(spec))
	switch spec {
	case "":
		log.Panic("empty GPU placement")
	case "all":
		return b.actualGPUs()
	case "first":
		actual := b.actualGPUs()
		return []int{actual[0]}
	case "src":
		if len(src) == 0 {
			log.Panic("-copy-gpus=src requires source GPUs")
		}
		return append([]int(nil), src...)
	case "dst":
		if len(dst) == 0 {
			log.Panic("-copy-gpus=dst requires destination GPUs")
		}
		return append([]int(nil), dst...)
	}

	gpus, err := parseGPUList(spec)
	if err != nil {
		log.Panic(err)
	}
	b.validateActualGPUs(gpus)
	return gpus
}

func (b *Benchmark) actualGPUs() []int {
	numGPUs := b.driver.GetNumGPUs()
	if numGPUs <= 0 {
		log.Panic("platform has no actual GPUs")
	}

	// The akkalat runner creates its unified GPU from actual device IDs 1..48.
	if numGPUs > 48 {
		numGPUs = 48
	}

	gpus := make([]int, numGPUs)
	for i := range gpus {
		gpus[i] = i + 1
	}
	return gpus
}

func (b *Benchmark) validateActualGPUs(gpus []int) {
	if len(gpus) == 0 {
		log.Panic("GPU placement must not be empty")
	}
	maxGPU := b.driver.GetNumGPUs()
	if maxGPU > 48 {
		maxGPU = 48
	}
	for _, gpu := range gpus {
		if gpu < 1 || gpu > maxGPU {
			log.Panicf("GPU ID %d is outside actual GPU range 1..%d", gpu, maxGPU)
		}
	}
}

func (b *Benchmark) copyDevice(copyGPUs []int) int {
	b.validateActualGPUs(copyGPUs)
	if len(copyGPUs) == 1 {
		return copyGPUs[0]
	}
	if len(b.gpus) == 1 && sameGPUList(copyGPUs, b.actualGPUs()) {
		return b.gpus[0]
	}
	return b.driver.CreateUnifiedGPU(b.ctx, copyGPUs)
}

func parseGPUList(spec string) ([]int, error) {
	gpus := make([]int, 0)
	seen := make(map[int]bool)
	for _, token := range strings.Split(spec, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}

		parts := strings.Split(token, "-")
		if len(parts) > 2 {
			return nil, fmt.Errorf("invalid GPU range %q", token)
		}

		start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, fmt.Errorf("invalid GPU ID %q", token)
		}
		end := start
		if len(parts) == 2 {
			end, err = strconv.Atoi(strings.TrimSpace(parts[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid GPU range %q", token)
			}
			if end < start {
				return nil, fmt.Errorf("invalid descending GPU range %q", token)
			}
		}

		for gpu := start; gpu <= end; gpu++ {
			if !seen[gpu] {
				gpus = append(gpus, gpu)
				seen[gpu] = true
			}
		}
	}

	if len(gpus) == 0 {
		return nil, fmt.Errorf("GPU placement %q is empty", spec)
	}
	return gpus, nil
}

func sameGPUList(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (b *Benchmark) requirePositive(name string, value int) {
	if value <= 0 {
		log.Panicf("-%s must be > 0", name)
	}
}
