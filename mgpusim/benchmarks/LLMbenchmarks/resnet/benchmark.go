// Package resnet implements synthetic ResNet forward benchmarks.
package resnet

import (
	"flag"
	"fmt"
	"log"

	"github.com/sarchlab/mgpusim/v3/benchmarks"
	"github.com/sarchlab/mgpusim/v3/benchmarks/LLMbenchmarks/operators"
	"github.com/sarchlab/mgpusim/v3/driver"
)

var _ benchmarks.Benchmark = (*Benchmark)(nil)

var (
	modeFlag = flag.String(
		"resnet-mode", "block", "ResNet mode: block or full.")
	depthFlag = flag.Int(
		"resnet-depth", 18, "ResNet depth: 18, 34, or 50.")
	batchSizeFlag = flag.Int(
		"resnet-batch-size", 1, "ResNet synthetic batch size.")
	imageSizeFlag = flag.Int(
		"resnet-image-size", 528, "ResNet synthetic square image size.")
	logSubtasksFlag = flag.Bool(
		"resnet-log-subtasks", false, "Print ResNet subtask progress.")
)

// Benchmark defines a forward-only ResNet workload.
type Benchmark struct {
	driver *driver.Driver
	ctx    *driver.Context
	gpus   []int

	to  *operators.GPUOperator
	ops *operators.Operator

	useUnifiedMemory bool
}

// NewBenchmark creates a ResNet benchmark.
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
		log.Panic("resnet benchmark requires at least one GPU")
	}
	b.driver.SelectGPU(b.ctx, b.gpus[0])
	b.to = operators.NewGPUOperator(b.driver, b.ctx)
	b.to.ReportTime()
	b.ops = operators.NewOperator(
		b.driver, b.ctx, b.to, "ResNet", *logSubtasksFlag)

	switch *modeFlag {
	case "block":
		b.runBlock()
	case "full":
		b.runFull()
	default:
		log.Panicf("unknown -resnet-mode %q", *modeFlag)
	}
}

// Verify is intentionally not implemented for synthetic forward benchmarks.
func (b *Benchmark) Verify() {
}

func (b *Benchmark) runBlock() {
	b.ops.Log("run block depth=%d batch=%d image=%d",
		*depthFlag, *batchSizeFlag, *imageSizeFlag)
	if *depthFlag == 50 {
		input := b.ops.Input(
			"block", []int{*batchSizeFlag, 64, *imageSizeFlag, *imageSizeFlag})
		out, _, _, _ := b.bottleneckBlock(input, 64, *imageSizeFlag, *imageSizeFlag, 256, 1, 0)
		b.ops.Free(out)
		return
	}

	input := b.ops.Input(
		"block", []int{*batchSizeFlag, 64, *imageSizeFlag, *imageSizeFlag})
	out, _, _, _ := b.basicBlock(input, 64, *imageSizeFlag, *imageSizeFlag, 64, 1, 0)
	b.ops.Free(out)
}

func (b *Benchmark) runFull() {
	b.ops.Log("run full depth=%d batch=%d image=%d",
		*depthFlag, *batchSizeFlag, *imageSizeFlag)
	input := b.ops.Input(
		"image", []int{*batchSizeFlag, 3, *imageSizeFlag, *imageSizeFlag})

	x, c, h, w := b.conv(input, 3, *imageSizeFlag, *imageSizeFlag, 64, 7, 2, 3, 0)
	b.ops.Free(input)
	bn := b.ops.BatchNorm2DInference("stem", x)
	b.ops.Free(x)
	b.ops.Log("stem relu elements=%d", bn.NumElement())
	x = b.to.ReluForward(bn)
	b.ops.Free(bn)
	x, h, w = b.maxPool(x, h, w, 3, 2, 1)

	blocks := resnetBlocks(*depthFlag)
	stageChannels := []int{64, 128, 256, 512}
	if *depthFlag == 50 {
		stageChannels = []int{256, 512, 1024, 2048}
	}

	layerIndex := 1
	for stage, count := range blocks {
		for i := 0; i < count; i++ {
			stride := 1
			if stage > 0 && i == 0 {
				stride = 2
			}
			if *depthFlag == 50 {
				x, c, h, w = b.bottleneckBlock(
					x, c, h, w, stageChannels[stage], stride, layerIndex)
			} else {
				x, c, h, w = b.basicBlock(
					x, c, h, w, stageChannels[stage], stride, layerIndex)
			}
			layerIndex += 3
		}
	}

	b.ops.Log("avgpool [%d,%d]", h, w)
	pool := b.to.AvgPoolingForward(
		x, []int{h, w}, []int{0, 0}, []int{1, 1})
	b.ops.Free(x)
	pool.SetSize([]int{*batchSizeFlag, c})
	out := b.ops.Linear("classifier", pool, *batchSizeFlag, c, 1000)
	b.ops.Free(pool)
	b.ops.Free(out)
}

func resnetBlocks(depth int) []int {
	switch depth {
	case 18:
		return []int{2, 2, 2, 2}
	case 34:
		return []int{3, 4, 6, 3}
	case 50:
		return []int{3, 4, 6, 3}
	default:
		log.Panicf("unsupported -resnet-depth %d", depth)
	}
	return nil
}

func (b *Benchmark) basicBlock(
	input operators.Tensor,
	inC, inH, inW, outC, stride, index int,
) (operators.Tensor, int, int, int) {
	b.ops.Log("basic block index=%d inC=%d outC=%d stride=%d",
		index, inC, outC, stride)
	identity := input
	x, _, h, w := b.conv(input, inC, inH, inW, outC, 3, stride, 1, index)
	bn := b.ops.BatchNorm2DInference("basic conv1", x)
	b.ops.Free(x)
	b.ops.Log("basic conv1 relu elements=%d", bn.NumElement())
	x = b.to.ReluForward(bn)
	b.ops.Free(bn)

	x2, _, h, w := b.conv(x, outC, h, w, outC, 3, 1, 1, index+1)
	b.ops.Free(x)
	x = b.ops.BatchNorm2DInference("basic conv2", x2)
	b.ops.Free(x2)

	if stride != 1 || inC != outC {
		proj, _, _, _ := b.conv(input, inC, inH, inW, outC, 1, stride, 0, index+2)
		identity = proj
		b.ops.Free(input)
	}

	add := b.ops.ResidualAdd("basic", x, identity)
	b.ops.Free(x)
	b.ops.Free(identity)
	b.ops.Log("basic output relu elements=%d", add.NumElement())
	out := b.to.ReluForward(add)
	b.ops.Free(add)
	return out, outC, h, w
}

func (b *Benchmark) bottleneckBlock(
	input operators.Tensor,
	inC, inH, inW, outC, stride, index int,
) (operators.Tensor, int, int, int) {
	b.ops.Log("bottleneck block index=%d inC=%d outC=%d stride=%d",
		index, inC, outC, stride)
	identity := input
	midC := outC / 4

	x, _, h, w := b.conv(input, inC, inH, inW, midC, 1, 1, 0, index)
	bn := b.ops.BatchNorm2DInference("bottleneck conv1", x)
	b.ops.Free(x)
	b.ops.Log("bottleneck conv1 relu elements=%d", bn.NumElement())
	x = b.to.ReluForward(bn)
	b.ops.Free(bn)

	x2, _, h, w := b.conv(x, midC, h, w, midC, 3, stride, 1, index+1)
	b.ops.Free(x)
	bn = b.ops.BatchNorm2DInference("bottleneck conv2", x2)
	b.ops.Free(x2)
	b.ops.Log("bottleneck conv2 relu elements=%d", bn.NumElement())
	x = b.to.ReluForward(bn)
	b.ops.Free(bn)

	x3, _, h, w := b.conv(x, midC, h, w, outC, 1, 1, 0, index+2)
	b.ops.Free(x)
	x = b.ops.BatchNorm2DInference("bottleneck conv3", x3)
	b.ops.Free(x3)

	if stride != 1 || inC != outC {
		proj, _, _, _ := b.conv(input, inC, inH, inW, outC, 1, stride, 0, index+3)
		identity = proj
		b.ops.Free(input)
	}

	add := b.ops.ResidualAdd("bottleneck", x, identity)
	b.ops.Free(x)
	b.ops.Free(identity)
	b.ops.Log("bottleneck output relu elements=%d", add.NumElement())
	out := b.to.ReluForward(add)
	b.ops.Free(add)
	return out, outC, h, w
}

func (b *Benchmark) conv(
	input operators.Tensor,
	inC, inH, inW, outC, kernel, stride, pad, index int,
) (operators.Tensor, int, int, int) {
	outH := (inH-kernel+2*pad)/stride + 1
	outW := (inW-kernel+2*pad)/stride + 1
	if outH <= 0 || outW <= 0 {
		panic(fmt.Sprintf("invalid conv output %dx%d", outH, outW))
	}
	b.ops.Log("conv index=%d [%d,%d,%d] -> [%d,%d,%d] k=%d stride=%d",
		index, inC, inH, inW, outC, outH, outW, kernel, stride)

	numWeight := outC * inC * kernel * kernel
	numBias := outC
	params := b.to.Create([]int{numWeight + numBias})
	weights := b.to.Slice(params, 0, numWeight)
	bias := b.to.Slice(params, numWeight, numWeight+numBias)
	b.to.Clear(params)

	im2ColMatrix := b.to.Im2Col(
		input,
		[]int{kernel, kernel},
		[]int{pad, pad},
		[]int{stride, stride},
		[]int{1, 1},
	)
	weightMatrix := b.to.Reshape(weights, []int{outC, im2ColMatrix.Size()[0]})

	biasMatrix := b.to.Repeat(bias, im2ColMatrix.Size()[1])
	biasMatrix.SetSize([]int{im2ColMatrix.Size()[1], outC})
	biasMatrixTranspose := b.to.Transpose(biasMatrix, []int{1, 0})

	outputMatrix := b.to.Gemm(
		false, false, 1.0, 1.0,
		weightMatrix, im2ColMatrix, biasMatrixTranspose,
	)
	outputMatrix.SetSize([]int{outC, input.Size()[0], outH, outW})
	outputTranspose := b.to.Transpose(outputMatrix, []int{1, 0, 2, 3})
	outputTranspose.SetDescriptor("NCHW")

	b.to.Free(im2ColMatrix)
	b.to.Free(weightMatrix)
	b.to.Free(biasMatrix)
	b.to.Free(biasMatrixTranspose)
	b.to.Free(outputMatrix)
	b.to.Free(params)

	return outputTranspose, outC, outH, outW
}

func (b *Benchmark) maxPool(
	input operators.Tensor,
	inH, inW, kernel, stride, pad int,
) (operators.Tensor, int, int) {
	outH := (inH-kernel+2*pad)/stride + 1
	outW := (inW-kernel+2*pad)/stride + 1
	b.ops.Log("maxpool [%d,%d] -> [%d,%d]", inH, inW, outH, outW)
	out, mask := b.to.MaxPoolingForward(
		input, []int{kernel, kernel}, []int{pad, pad}, []int{stride, stride})
	b.ops.Free(input)
	b.ops.Free(mask)
	return out, outH, outW
}
