// Package gpt implements synthetic GPT forward benchmarks.
package gpt

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
		"gpt-mode", "block", "GPT mode: block or full.")
	sizeFlag = flag.String(
		"gpt-size", "7b-proxy", "GPT size: tiny, gpt2-small, gpt2-medium, 7b-proxy, or custom.")
	batchSizeFlag = flag.Int(
		"gpt-batch-size", 0, "GPT batch size override.")
	seqLenFlag = flag.Int(
		"gpt-seq-len", 0, "GPT sequence length override.")
	hiddenSizeFlag = flag.Int(
		"gpt-hidden-size", 0, "GPT hidden size override.")
	numHeadsFlag = flag.Int(
		"gpt-num-heads", 0, "GPT attention head count override.")
	numLayersFlag = flag.Int(
		"gpt-num-layers", 0, "GPT decoder layer count override.")
	intermediateSizeFlag = flag.Int(
		"gpt-intermediate-size", 0, "GPT MLP intermediate size override.")
	logSubtasksFlag = flag.Bool(
		"gpt-log-subtasks", false, "Print GPT subtask progress.")
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

// Benchmark defines a forward-only GPT workload.
type Benchmark struct {
	driver *driver.Driver
	ctx    *driver.Context
	gpus   []int

	to  *operators.GPUOperator
	ops *operators.Operator

	useUnifiedMemory bool
}

// NewBenchmark creates a GPT benchmark.
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
		log.Panic("gpt benchmark requires at least one GPU")
	}
	cfg := gptConfig()
	b.driver.SelectGPU(b.ctx, b.gpus[0])
	b.to = operators.NewGPUOperator(b.driver, b.ctx)
	b.to.ReportTime()
	b.ops = operators.NewOperator(
		b.driver, b.ctx, b.to, "GPT", *logSubtasksFlag)
	b.ops.Log("run mode=%s size=%s batch=%d seq=%d hidden=%d heads=%d layers=%d intermediate=%d",
		cfg.mode, cfg.size, cfg.batchSize, cfg.seqLen, cfg.hidden,
		cfg.numHeads, cfg.numLayers, cfg.intermediate)

	rows := cfg.batchSize * cfg.seqLen
	x := b.ops.Embedding("token+position", rows, cfg.hidden)
	switch cfg.mode {
	case "block":
		x = b.decoderLayer(x, rows, cfg, 0)
	case "full":
		for i := 0; i < cfg.numLayers; i++ {
			x = b.decoderLayer(x, rows, cfg, i)
		}
	default:
		log.Panicf("unknown -gpt-mode %q", cfg.mode)
	}
	b.ops.Free(x)
}

// Verify is intentionally not implemented for synthetic forward benchmarks.
func (b *Benchmark) Verify() {
}

func gptConfig() config {
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
	case "gpt2-small":
		cfg.seqLen = 128
		cfg.hidden = 768
		cfg.numHeads = 12
		cfg.numLayers = 12
		cfg.intermediate = 3072
	case "gpt2-medium":
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
		log.Panicf("unknown -gpt-size %q", cfg.size)
	}

	applyOverrides(&cfg)
	validateTransformerConfig("gpt", cfg)
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

func (b *Benchmark) decoderLayer(
	input operators.Tensor,
	rows int,
	cfg config,
	layer int,
) operators.Tensor {
	b.ops.Log("decoder layer %d", layer)
	norm1 := b.ops.LayerNorm("decoder norm1", input, rows, cfg.hidden)
	attn := b.ops.SelfAttention(
		"decoder causal attention",
		norm1, rows, cfg.hidden, cfg.numHeads, cfg.seqLen, cfg.batchSize, true)
	b.ops.Free(norm1)
	res1 := b.ops.ResidualAdd("decoder attn", input, attn)
	b.ops.Free(input)
	b.ops.Free(attn)

	norm2 := b.ops.LayerNorm("decoder norm2", res1, rows, cfg.hidden)
	mlp := b.ops.MLP(
		"decoder", norm2, rows, cfg.hidden, cfg.intermediate)
	b.ops.Free(norm2)
	res2 := b.ops.ResidualAdd("decoder mlp", res1, mlp)
	b.ops.Free(res1)
	b.ops.Free(mlp)
	return res2
}
