// Package inference implements synthetic LLM inference phase benchmarks.
package inference

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
		"llminfer-mode", "mixed", "LLM inference mode: prefill, decode, mixed.")
	sizeFlag = flag.String(
		"llminfer-size", "7b-proxy", "LLM inference size: tiny, small, 7b-proxy, custom.")
	batchSizeFlag = flag.Int(
		"llminfer-batch-size", 0, "LLM inference batch size override.")
	prefillLenFlag = flag.Int(
		"llminfer-prefill-len", 0, "LLM prefill sequence length override.")
	contextLenFlag = flag.Int(
		"llminfer-context-len", 0, "LLM decode context length override.")
	decodeStepsFlag = flag.Int(
		"llminfer-decode-steps", 0, "LLM decode step count override.")
	hiddenSizeFlag = flag.Int(
		"llminfer-hidden-size", 0, "LLM hidden size override.")
	numHeadsFlag = flag.Int(
		"llminfer-num-heads", 0, "LLM attention head count override.")
	numLayersFlag = flag.Int(
		"llminfer-num-layers", 0, "LLM layer count override.")
	intermediateSizeFlag = flag.Int(
		"llminfer-intermediate-size", 0, "LLM MLP intermediate size override.")
	logSubtasksFlag = flag.Bool(
		"llminfer-log-subtasks", false, "Print LLM inference subtasks.")
)

type config struct {
	mode         string
	size         string
	batchSize    int
	prefillLen   int
	contextLen   int
	decodeSteps  int
	hidden       int
	numHeads     int
	numLayers    int
	intermediate int
}

// Benchmark defines a synthetic forward-only LLM inference workload.
type Benchmark struct {
	driver *driver.Driver
	ctx    *driver.Context
	gpus   []int

	to  *operators.GPUOperator
	ops *operators.Operator

	useUnifiedMemory bool
}

// NewBenchmark creates an LLM inference benchmark.
func NewBenchmark(driver *driver.Driver) *Benchmark {
	return &Benchmark{driver: driver, ctx: driver.Init()}
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
		log.Panic("llminference benchmark requires at least one GPU")
	}
	cfg := inferConfig()
	b.driver.SelectGPU(b.ctx, b.gpus[0])
	b.to = operators.NewGPUOperator(b.driver, b.ctx)
	b.to.ReportTime()
	b.ops = operators.NewOperator(
		b.driver, b.ctx, b.to, "LLMInfer", *logSubtasksFlag)
	b.ops.Log("run mode=%s size=%s batch=%d prefill=%d context=%d decode_steps=%d hidden=%d heads=%d layers=%d intermediate=%d",
		cfg.mode, cfg.size, cfg.batchSize, cfg.prefillLen, cfg.contextLen,
		cfg.decodeSteps, cfg.hidden, cfg.numHeads, cfg.numLayers,
		cfg.intermediate)

	switch cfg.mode {
	case "prefill":
		b.runPrefill(cfg)
	case "decode":
		b.runDecode(cfg)
	case "mixed":
		b.runPrefill(cfg)
		b.runDecode(cfg)
	default:
		log.Panicf("unknown -llminfer-mode %q", cfg.mode)
	}
}

// Verify is intentionally not implemented for synthetic inference benchmarks.
func (b *Benchmark) Verify() {
}

func inferConfig() config {
	cfg := config{
		mode:         *modeFlag,
		size:         *sizeFlag,
		batchSize:    1,
		prefillLen:   16,
		contextLen:   128,
		decodeSteps:  4,
		hidden:       256,
		numHeads:     8,
		numLayers:    1,
		intermediate: 1024,
	}

	switch cfg.size {
	case "tiny":
		cfg.prefillLen = 4
		cfg.contextLen = 16
		cfg.decodeSteps = 2
		cfg.hidden = 64
		cfg.numHeads = 4
		cfg.intermediate = 128
	case "small":
	case "7b-proxy":
		cfg.prefillLen = 5120
		cfg.contextLen = 5120
		cfg.decodeSteps = 1
		cfg.hidden = 4096
		cfg.numHeads = 32
		cfg.numLayers = 1
		cfg.intermediate = 11008
	case "custom":
	default:
		log.Panicf("unknown -llminfer-size %q", cfg.size)
	}

	applyOverrides(&cfg)
	validateConfig(cfg)
	return cfg
}

func applyOverrides(cfg *config) {
	if *batchSizeFlag > 0 {
		cfg.batchSize = *batchSizeFlag
	}
	if *prefillLenFlag > 0 {
		cfg.prefillLen = *prefillLenFlag
	}
	if *contextLenFlag > 0 {
		cfg.contextLen = *contextLenFlag
	}
	if *decodeStepsFlag > 0 {
		cfg.decodeSteps = *decodeStepsFlag
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

func validateConfig(cfg config) {
	if cfg.batchSize <= 0 || cfg.prefillLen <= 0 || cfg.contextLen <= 0 ||
		cfg.decodeSteps <= 0 || cfg.hidden <= 0 || cfg.numHeads <= 0 ||
		cfg.numLayers <= 0 || cfg.intermediate <= 0 {
		log.Panicf("llminference config has non-positive values: %+v", cfg)
	}
	if cfg.hidden%cfg.numHeads != 0 {
		log.Panicf("llminference hidden size must be divisible by heads: %+v", cfg)
	}
}

func (b *Benchmark) runPrefill(cfg config) {
	rows := cfg.batchSize * cfg.prefillLen
	x := b.ops.Embedding("prefill token+position", rows, cfg.hidden)
	for layer := 0; layer < cfg.numLayers; layer++ {
		x = b.prefillLayer(x, rows, cfg, layer)
	}
	b.ops.Free(x)
}

func (b *Benchmark) runDecode(cfg config) {
	for step := 0; step < cfg.decodeSteps; step++ {
		rows := cfg.batchSize
		x := b.ops.Embedding("decode token", rows, cfg.hidden)
		for layer := 0; layer < cfg.numLayers; layer++ {
			kvRows := cfg.batchSize * (cfg.contextLen + step)
			x = b.decodeLayer(x, rows, kvRows, cfg, layer, step)
		}
		b.ops.Free(x)
	}
}

func (b *Benchmark) prefillLayer(
	input operators.Tensor,
	rows int,
	cfg config,
	layer int,
) operators.Tensor {
	b.ops.Log("prefill layer %d", layer)
	norm1 := b.ops.LayerNorm("prefill norm1", input, rows, cfg.hidden)
	attn := b.ops.SelfAttention(
		"prefill causal attention",
		norm1, rows, cfg.hidden, cfg.numHeads, cfg.prefillLen, cfg.batchSize, true)
	b.ops.Free(norm1)
	res1 := b.ops.ResidualAdd("prefill attn", input, attn)
	b.ops.Free(input)
	b.ops.Free(attn)

	norm2 := b.ops.LayerNorm("prefill norm2", res1, rows, cfg.hidden)
	mlp := b.ops.MLP("prefill", norm2, rows, cfg.hidden, cfg.intermediate)
	b.ops.Free(norm2)
	res2 := b.ops.ResidualAdd("prefill mlp", res1, mlp)
	b.ops.Free(res1)
	b.ops.Free(mlp)
	return res2
}

func (b *Benchmark) decodeLayer(
	input operators.Tensor,
	rows, kvRows int,
	cfg config,
	layer, step int,
) operators.Tensor {
	b.ops.Log("decode step %d layer %d", step, layer)
	norm1 := b.ops.LayerNorm("decode norm1", input, rows, cfg.hidden)
	attn := b.ops.DecodeAttention(
		"decode kv attention", norm1, rows, kvRows, cfg.hidden, cfg.numHeads)
	b.ops.Free(norm1)
	res1 := b.ops.ResidualAdd("decode attn", input, attn)
	b.ops.Free(input)
	b.ops.Free(attn)

	norm2 := b.ops.LayerNorm("decode norm2", res1, rows, cfg.hidden)
	mlp := b.ops.MLP("decode", norm2, rows, cfg.hidden, cfg.intermediate)
	b.ops.Free(norm2)
	res2 := b.ops.ResidualAdd("decode mlp", res1, mlp)
	b.ops.Free(res1)
	b.ops.Free(mlp)
	return res2
}
