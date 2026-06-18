// Package bert implements synthetic BERT forward benchmarks.
package bert

import (
	"flag"
	"log"

	"github.com/sarchlab/mgpusim/v3/benchmarks"
	"github.com/sarchlab/mgpusim/v3/benchmarks/LLMbenchmarks/operators"
	"github.com/sarchlab/mgpusim/v3/driver"
)

var _ benchmarks.Benchmark = (*Benchmark)(nil)

var (
	modeFlag = flag.String(
		"bert-mode", "block", "BERT mode: block or full.")
	sizeFlag = flag.String(
		"bert-size", "7b-proxy", "BERT size: tiny, base, large, 7b-proxy, or custom.")
	batchSizeFlag = flag.Int(
		"bert-batch-size", 0, "BERT batch size override.")
	seqLenFlag = flag.Int(
		"bert-seq-len", 0, "BERT sequence length override.")
	hiddenSizeFlag = flag.Int(
		"bert-hidden-size", 0, "BERT hidden size override.")
	numHeadsFlag = flag.Int(
		"bert-num-heads", 0, "BERT attention head count override.")
	numLayersFlag = flag.Int(
		"bert-num-layers", 0, "BERT encoder layer count override.")
	intermediateSizeFlag = flag.Int(
		"bert-intermediate-size", 0, "BERT MLP intermediate size override.")
	logSubtasksFlag = flag.Bool(
		"bert-log-subtasks", false, "Print BERT subtask progress.")
)

type config struct {
	mode         string
	size         string
	batchSize    int
	seqLen       int
	hidden       int
	numHeads     int
	numLayers    int
	intermediate int
}

// Benchmark defines a forward-only BERT workload.
type Benchmark struct {
	driver *driver.Driver
	ctx    *driver.Context
	gpus   []int

	to  *operators.GPUOperator
	ops *operators.Operator

	useUnifiedMemory bool
}

// NewBenchmark creates a BERT benchmark.
func NewBenchmark(driver *driver.Driver) *Benchmark {
	return &Benchmark{
		driver: driver,
		ctx:    driver.Init(),
	}
}

// SelectGPU selects GPUs.
func (b *Benchmark) SelectGPU(gpus []int) {
	b.gpus = gpus
}

// SetUnifiedMemory records the unified memory preference.
func (b *Benchmark) SetUnifiedMemory() {
	b.useUnifiedMemory = true
}

// Run executes the workload.
func (b *Benchmark) Run() {
	if len(b.gpus) == 0 {
		log.Panic("bert benchmark requires at least one GPU")
	}
	cfg := bertConfig()
	b.driver.SelectGPU(b.ctx, b.gpus[0])
	b.to = operators.NewGPUOperator(b.driver, b.ctx)
	b.to.ReportTime()
	b.ops = operators.NewOperator(
		b.driver, b.ctx, b.to, "BERT", *logSubtasksFlag)
	b.ops.Log("run mode=%s size=%s batch=%d seq=%d hidden=%d heads=%d layers=%d intermediate=%d",
		cfg.mode, cfg.size, cfg.batchSize, cfg.seqLen, cfg.hidden,
		cfg.numHeads, cfg.numLayers, cfg.intermediate)

	rows := cfg.batchSize * cfg.seqLen
	x := b.ops.Embedding("token+position", rows, cfg.hidden)
	switch cfg.mode {
	case "block":
		x = b.encoderLayer(x, rows, cfg, 0)
	case "full":
		for i := 0; i < cfg.numLayers; i++ {
			x = b.encoderLayer(x, rows, cfg, i)
		}
	default:
		log.Panicf("unknown -bert-mode %q", cfg.mode)
	}
	b.ops.Free(x)
}

// Verify is intentionally not implemented for synthetic forward benchmarks.
func (b *Benchmark) Verify() {
}

func bertConfig() config {
	cfg := config{
		mode:         *modeFlag,
		size:         *sizeFlag,
		batchSize:    1,
		seqLen:       8,
		hidden:       64,
		numHeads:     4,
		numLayers:    1,
		intermediate: 128,
	}

	switch cfg.size {
	case "tiny":
	case "base":
		cfg.seqLen = 128
		cfg.hidden = 768
		cfg.numHeads = 12
		cfg.numLayers = 12
		cfg.intermediate = 3072
	case "large":
		cfg.seqLen = 128
		cfg.hidden = 1024
		cfg.numHeads = 16
		cfg.numLayers = 24
		cfg.intermediate = 4096
	case "7b-proxy":
		cfg.seqLen = 5120
		cfg.hidden = 4096
		cfg.numHeads = 32
		cfg.numLayers = 1
		cfg.intermediate = 11008
	case "custom":
	default:
		log.Panicf("unknown -bert-size %q", cfg.size)
	}

	applyOverrides(&cfg)
	validateTransformerConfig("bert", cfg)
	return cfg
}

func applyOverrides(cfg *config) {
	if *batchSizeFlag > 0 {
		cfg.batchSize = *batchSizeFlag
	}
	if *seqLenFlag > 0 {
		cfg.seqLen = *seqLenFlag
	}
	if *hiddenSizeFlag > 0 {
		cfg.hidden = *hiddenSizeFlag
	}
	if *numHeadsFlag > 0 {
		cfg.numHeads = *numHeadsFlag
	}
	if *numLayersFlag > 0 {
		cfg.numLayers = *numLayersFlag
	}
	if *intermediateSizeFlag > 0 {
		cfg.intermediate = *intermediateSizeFlag
	}
}

func validateTransformerConfig(name string, cfg config) {
	if cfg.batchSize <= 0 || cfg.seqLen <= 0 || cfg.hidden <= 0 ||
		cfg.numHeads <= 0 || cfg.numLayers <= 0 || cfg.intermediate <= 0 {
		log.Panicf("%s config has non-positive values: %+v", name, cfg)
	}
	if cfg.hidden%cfg.numHeads != 0 {
		log.Panicf("%s hidden size must be divisible by num heads: %+v", name, cfg)
	}
}

func (b *Benchmark) encoderLayer(
	input operators.Tensor,
	rows int,
	cfg config,
	layer int,
) operators.Tensor {
	b.ops.Log("encoder layer %d", layer)
	attn := b.ops.SelfAttention(
		"encoder attention",
		input, rows, cfg.hidden, cfg.numHeads, cfg.seqLen, cfg.batchSize, false)
	res1 := b.ops.ResidualAdd("encoder attn", input, attn)
	b.ops.Free(input)
	b.ops.Free(attn)
	norm1 := b.ops.LayerNorm("encoder norm1", res1, rows, cfg.hidden)
	b.ops.Free(res1)

	mlp := b.ops.MLP(
		"encoder", norm1, rows, cfg.hidden, cfg.intermediate)
	res2 := b.ops.ResidualAdd("encoder mlp", norm1, mlp)
	b.ops.Free(norm1)
	b.ops.Free(mlp)
	norm2 := b.ops.LayerNorm("encoder norm2", res2, rows, cfg.hidden)
	b.ops.Free(res2)
	return norm2
}
