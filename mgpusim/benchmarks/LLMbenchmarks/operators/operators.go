// Package operators provides shared low-level operators for LLMbenchmarks.
package operators

import (
	_ "embed"
	"fmt"
	"log"

	"github.com/sarchlab/mgpusim/v3/driver"
	"github.com/sarchlab/mgpusim/v3/insts"
	"github.com/sarchlab/mgpusim/v3/kernels"
)

//go:embed native/residual_add.hsaco
var residualAddKernelBytes []byte

//go:embed native/gelu.hsaco
var geluKernelBytes []byte

//go:embed native/layernorm.hsaco
var layerNormKernelBytes []byte

//go:embed native/embedding_synthetic.hsaco
var embeddingKernelBytes []byte

//go:embed native/batchnorm2d_inference.hsaco
var batchNormKernelBytes []byte

//go:embed native/causal_mask.hsaco
var causalMaskKernelBytes []byte

//go:embed native/row_softmax.hsaco
var rowSoftmaxKernelBytes []byte

// Operator wraps the existing GPU tensor operator with model-oriented helpers.
// The package intentionally lives under LLMbenchmarks so ResNet, BERT, and GPT
// can share suboperators without becoming part of the older dnn benchmark tree.
type Operator struct {
	driver      *driver.Driver
	ctx         *driver.Context
	to          *GPUOperator
	logSubtasks bool
	prefix      string

	residualAddKernel *insts.HsaCo
	geluKernel        *insts.HsaCo
	layerNormKernel   *insts.HsaCo
	embeddingKernel   *insts.HsaCo
	batchNormKernel   *insts.HsaCo
	causalMaskKernel  *insts.HsaCo
	rowSoftmaxKernel  *insts.HsaCo
}

// NewOperator creates an LLMbenchmark operator wrapper.
func NewOperator(
	gpuDriver *driver.Driver,
	ctx *driver.Context,
	to *GPUOperator,
	prefix string,
	logSubtasks bool,
) *Operator {
	o := &Operator{
		driver:      gpuDriver,
		ctx:         ctx,
		to:          to,
		prefix:      prefix,
		logSubtasks: logSubtasks,
	}
	o.loadKernels()
	return o
}

// TensorOperator returns the underlying DNN tensor operator.
func (o *Operator) TensorOperator() *GPUOperator {
	return o.to
}

// Log prints a model subtask when logging is enabled.
func (o *Operator) Log(format string, args ...interface{}) {
	if !o.logSubtasks {
		return
	}
	fmt.Printf("[%s] %s\n", o.prefix, fmt.Sprintf(format, args...))
}

// Free releases a tensor if it is non-nil.
func (o *Operator) Free(t Tensor) {
	if t == nil {
		return
	}
	o.to.Free(t)
}

// Input creates a synthetic input tensor.
func (o *Operator) Input(name string, size []int) Tensor {
	o.Log("%s input %v", name, size)
	return o.to.Zeros(size)
}

func (o *Operator) loadKernels() {
	o.residualAddKernel = loadKernel(residualAddKernelBytes, "llm_residual_add")
	o.geluKernel = loadKernel(geluKernelBytes, "llm_gelu")
	o.layerNormKernel = loadKernel(layerNormKernelBytes, "llm_layernorm")
	o.embeddingKernel = loadKernel(embeddingKernelBytes, "llm_embedding_synthetic")
	o.batchNormKernel = loadKernel(batchNormKernelBytes, "llm_batchnorm2d_inference")
	o.causalMaskKernel = loadKernel(causalMaskKernelBytes, "llm_apply_causal_mask")
	o.rowSoftmaxKernel = loadKernel(rowSoftmaxKernelBytes, "llm_row_softmax")
}

func loadKernel(data []byte, name string) *insts.HsaCo {
	kernel := kernels.LoadProgramFromMemory(data, "")
	if kernel == nil {
		log.Panicf("failed to load LLM kernel %s", name)
	}
	return kernel
}

func ptr(t Tensor) driver.Ptr {
	return t.(*GPUTensor).Ptr()
}

func launch1DSize(n int) [3]uint32 {
	return [3]uint32{uint32(n), 1, 1}
}

func ones(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 1
	}
	return out
}

type residualAddArgs struct {
	Out, A, B                 driver.Ptr
	N, Padding                int32
	OffsetX, OffsetY, OffsetZ int64
}

type geluArgs struct {
	Out, In                   driver.Ptr
	N, Padding                int32
	OffsetX, OffsetY, OffsetZ int64
}

type layerNormArgs struct {
	Out, In                   driver.Ptr
	Rows, Hidden              int32
	Epsilon                   float32
	Padding                   int32
	OffsetX, OffsetY, OffsetZ int64
}

type embeddingArgs struct {
	Out, TokenIDs             driver.Ptr
	TokenTable, PositionTable driver.Ptr
	Rows, Hidden, VocabSize   int32
	Padding                   int32
	OffsetX, OffsetY, OffsetZ int64
}

type batchNorm2DInferenceArgs struct {
	Out, In                   driver.Ptr
	Mean, Variance            driver.Ptr
	Gamma, Beta               driver.Ptr
	N, Channels               int32
	Height, Width             int32
	Epsilon                   float32
	Padding                   int32
	OffsetX, OffsetY, OffsetZ int64
}

type causalMaskArgs struct {
	Scores                    driver.Ptr
	Rows, SeqLen, BatchSize   int32
	MaskValue                 float32
	OffsetX, OffsetY, OffsetZ int64
}

type rowSoftmaxArgs struct {
	Out, In                   driver.Ptr
	Rows, Cols                int32
	OffsetX, OffsetY, OffsetZ int64
}

// Embedding gathers token and position vectors, then adds them elementwise.
// The benchmark treats embedding tables as preloaded model weights, so it
// allocates them without timing a large host-side initialization copy.
func (o *Operator) Embedding(name string, rows, hidden int) Tensor {
	o.Log("%s embedding rows=%d hidden=%d", name, rows, hidden)
	vocabSize := embeddingVocabSize(rows)
	out := o.to.Create([]int{rows, hidden})
	tokenTable := o.to.Create([]int{vocabSize, hidden})
	positionTable := o.to.Create([]int{rows, hidden})
	tokenIDs := o.driver.AllocateMemory(o.ctx, uint64(rows*4))
	hTokenIDs := make([]int32, rows)
	for i := range hTokenIDs {
		hTokenIDs[i] = int32(i % vocabSize)
	}
	o.driver.MemCopyH2D(o.ctx, tokenIDs, hTokenIDs)

	args := embeddingArgs{
		Out:           ptr(out),
		TokenIDs:      tokenIDs,
		TokenTable:    ptr(tokenTable),
		PositionTable: ptr(positionTable),
		Rows:          int32(rows),
		Hidden:        int32(hidden),
		VocabSize:     int32(vocabSize),
	}
	o.driver.LaunchKernel(o.ctx, o.embeddingKernel,
		launch1DSize(rows*hidden),
		[3]uint16{64, 1, 1},
		&args)
	o.driver.FreeMemory(o.ctx, tokenIDs)
	o.Free(tokenTable)
	o.Free(positionTable)
	return out
}

func embeddingVocabSize(rows int) int {
	vocabSize := rows
	if vocabSize < 1024 {
		vocabSize = 1024
	}
	if vocabSize > 4096 {
		vocabSize = 4096
	}
	return vocabSize
}

// Linear performs a synthetic dense layer with a newly allocated weight matrix.
func (o *Operator) Linear(
	name string,
	input Tensor,
	rows, inputDim, outputDim int,
) Tensor {
	o.Log("%s linear [%d,%d] x [%d,%d]",
		name, rows, inputDim, inputDim, outputDim)
	input.SetSize([]int{rows, inputDim})
	weight := o.to.Zeros([]int{inputDim, outputDim})
	bias := o.to.Zeros([]int{rows, outputDim})
	out := o.to.Gemm(false, false, 1, 1, input, weight, bias)
	o.Free(weight)
	o.Free(bias)
	return out
}

// SplitKLinear performs a dense layer while splitting the GEMM reduction
// dimension across the kernel grid.
func (o *Operator) SplitKLinear(
	name string,
	input Tensor,
	rows, inputDim, outputDim, splitK int,
) Tensor {
	o.Log("%s split-k linear [%d,%d] x [%d,%d] split_k=%d",
		name, rows, inputDim, inputDim, outputDim, splitK)
	input.SetSize([]int{rows, inputDim})
	weight := o.to.Zeros([]int{inputDim, outputDim})
	bias := o.to.Zeros([]int{rows, outputDim})
	out := o.to.SplitKGemm(false, false, 1, 1, input, weight, bias, splitK)
	o.Free(weight)
	o.Free(bias)
	return out
}

// ResidualAdd performs elementwise a+b.
func (o *Operator) ResidualAdd(
	name string,
	a, b Tensor,
) Tensor {
	o.Log("%s residual add elements=%d", name, a.NumElement())
	if a.NumElement() != b.NumElement() {
		panic("residual add size mismatch")
	}
	out := o.to.Create(a.Size())
	args := residualAddArgs{
		Out: ptr(out),
		A:   ptr(a),
		B:   ptr(b),
		N:   int32(a.NumElement()),
	}
	o.driver.LaunchKernel(o.ctx, o.residualAddKernel,
		launch1DSize(a.NumElement()),
		[3]uint16{64, 1, 1},
		&args)
	return out
}

// BatchNorm2DInference models inference-time batch normalization as an
// elementwise affine operation.
func (o *Operator) BatchNorm2DInference(
	name string,
	input Tensor,
) Tensor {
	o.Log("%s batchnorm inference elements=%d", name, input.NumElement())
	size := input.Size()
	if len(size) != 4 {
		zeros := o.to.Zeros(input.Size())
		out := o.to.ScaleAdd(1, 0, input, zeros)
		o.Free(zeros)
		return out
	}

	channels := size[1]
	mean := o.to.Zeros([]int{channels})
	variance := o.to.Create([]int{channels})
	gamma := o.to.Create([]int{channels})
	beta := o.to.Zeros([]int{channels})
	o.to.Init(variance, ones(channels))
	o.to.Init(gamma, ones(channels))

	out := o.to.Create(input.Size())
	args := batchNorm2DInferenceArgs{
		Out:      ptr(out),
		In:       ptr(input),
		Mean:     ptr(mean),
		Variance: ptr(variance),
		Gamma:    ptr(gamma),
		Beta:     ptr(beta),
		N:        int32(input.NumElement()),
		Channels: int32(channels),
		Height:   int32(size[2]),
		Width:    int32(size[3]),
		Epsilon:  1e-5,
	}
	o.driver.LaunchKernel(o.ctx, o.batchNormKernel,
		launch1DSize(input.NumElement()),
		[3]uint16{64, 1, 1},
		&args)
	o.Free(mean)
	o.Free(variance)
	o.Free(gamma)
	o.Free(beta)
	return out
}

// LayerNorm performs row-wise normalization with workgroup-level reductions.
func (o *Operator) LayerNorm(
	name string,
	input Tensor,
	rows, hidden int,
) Tensor {
	o.Log("%s layernorm rows=%d hidden=%d", name, rows, hidden)
	input.SetSize([]int{rows, hidden})
	out := o.to.Create([]int{rows, hidden})
	args := layerNormArgs{
		Out:     ptr(out),
		In:      ptr(input),
		Rows:    int32(rows),
		Hidden:  int32(hidden),
		Epsilon: 1e-5,
	}
	o.driver.LaunchKernel(o.ctx, o.layerNormKernel,
		launch1DSize(rows*hidden),
		[3]uint16{64, 1, 1},
		&args)
	return out
}

// GELU applies the tanh-form GELU function elementwise.
func (o *Operator) GELU(name string, input Tensor) Tensor {
	o.Log("%s gelu elements=%d", name, input.NumElement())
	out := o.to.Create(input.Size())
	args := geluArgs{
		Out: ptr(out),
		In:  ptr(input),
		N:   int32(input.NumElement()),
	}
	o.driver.LaunchKernel(o.ctx, o.geluKernel,
		launch1DSize(input.NumElement()),
		[3]uint16{64, 1, 1},
		&args)
	return out
}

// SelfAttention performs a forward self-attention block using real GEMM and
// Softmax kernels. The current implementation uses combined-head matrices; the
// numHeads and causal parameters are retained in the interface so BERT/GPT own
// their workload semantics independently.
func (o *Operator) SelfAttention(
	name string,
	input Tensor,
	rows, hidden, numHeads, seqLen, batchSize int,
	causal bool,
) Tensor {
	o.Log("%s attention rows=%d hidden=%d heads=%d causal=%t",
		name, rows, hidden, numHeads, causal)
	q := o.Linear(name+" q", input, rows, hidden, hidden)
	k := o.Linear(name+" k", input, rows, hidden, hidden)
	v := o.Linear(name+" v", input, rows, hidden, hidden)

	o.Log("%s score gemm [%d,%d] x [%d,%d]^T",
		name, rows, hidden, rows, hidden)
	scoreBias := o.to.Zeros([]int{rows, rows})
	scores := o.to.Gemm(false, true, 1, 0, q, k, scoreBias)
	if causal {
		o.ApplyCausalMask(scores, rows, seqLen, batchSize)
	}
	probs := o.RowSoftmax(scores, rows, rows)

	o.Log("%s value gemm [%d,%d] x [%d,%d]",
		name, rows, rows, rows, hidden)
	valueBias := o.to.Zeros([]int{rows, hidden})
	ctx := o.to.Gemm(false, false, 1, 0, probs, v, valueBias)
	out := o.Linear(name+" output", ctx, rows, hidden, hidden)

	o.Free(q)
	o.Free(k)
	o.Free(v)
	o.Free(scoreBias)
	o.Free(scores)
	o.Free(probs)
	o.Free(valueBias)
	o.Free(ctx)

	return out
}

// RowSoftmax performs stable row-wise softmax with workgroup-level reductions.
func (o *Operator) RowSoftmax(t Tensor, rows, cols int) Tensor {
	o.Log("row softmax rows=%d cols=%d", rows, cols)
	t.SetSize([]int{rows, cols})
	out := o.to.Create([]int{rows, cols})
	args := rowSoftmaxArgs{
		Out:  ptr(out),
		In:   ptr(t),
		Rows: int32(rows),
		Cols: int32(cols),
	}
	o.driver.LaunchKernel(o.ctx, o.rowSoftmaxKernel,
		launch1DSize(rows*256),
		[3]uint16{256, 1, 1},
		&args)
	return out
}

// ApplyCausalMask masks future tokens in an attention score matrix.
func (o *Operator) ApplyCausalMask(
	scores Tensor,
	rows, seqLen, batchSize int,
) {
	o.Log("causal mask rows=%d seq=%d batch=%d", rows, seqLen, batchSize)
	args := causalMaskArgs{
		Scores:    ptr(scores),
		Rows:      int32(rows),
		SeqLen:    int32(seqLen),
		BatchSize: int32(batchSize),
		MaskValue: -3.4028234663852886e+38,
	}
	o.driver.LaunchKernel(o.ctx, o.causalMaskKernel,
		[3]uint32{uint32(rows), uint32(rows), 1},
		[3]uint16{16, 16, 1},
		&args)
}

// MLP performs a transformer feed-forward network.
func (o *Operator) MLP(
	name string,
	input Tensor,
	rows, hidden, intermediate int,
) Tensor {
	o.Log("%s mlp hidden=%d intermediate=%d", name, hidden, intermediate)
	h := o.Linear(name+" fc1", input, rows, hidden, intermediate)
	act := o.GELU(name, h)
	out := o.Linear(name+" fc2", act, rows, intermediate, hidden)
	o.Free(h)
	o.Free(act)
	return out
}
