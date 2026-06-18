package subbenchmarks

import (
	"sync"

	"github.com/sarchlab/mgpusim/v3/driver"
	"github.com/sarchlab/mgpusim/v3/insts"
	"github.com/sarchlab/mgpusim/v3/kernels"
)

var prKernelBytes []byte

type PRSubBenchmark struct {
	driver       *driver.Driver
	ctx          *driver.Context
	verification bool
	timerMutex   sync.Mutex
	reportTime   bool
	// vStart, vEnd sim.VTimeInSec
	// start, end   time.Time
	// cpuOperator  *tensor.CPUOperator

	prkernel *insts.HsaCo
}

func NewGPUOperator(
	gpuDriver *driver.Driver,
	ctx *driver.Context,
) *PRSubBenchmark {
	o := &PRSubBenchmark{
		driver: gpuDriver,
		ctx:    ctx,
	}

	o.loadKernels()

	return o
}

func (o *PRSubBenchmark) loadKernels() {
	// loadKernel(&o.sumKernel, operatorKernelBytes, "sum_one_axis")
	loadKernel(&o.prkernel, prKernelBytes, "pr")
}

func loadKernel(hsaco **insts.HsaCo, kernelBytes []byte, name string) {
	*hsaco = kernels.LoadProgramFromMemory(kernelBytes, name)
	if *hsaco == nil {
		panic("Failed to load " + name + "kernel")
	}
}

// func (o *PRSubBenchmark) Create(size []int, gpuid int) tensor.Tensor {
// 	t := &Tensor{
// 		driver: o.driver,
// 		ctx:    o.ctx,
// 		size:   size,
// 	}

// 	t.ptr = o.driver.AllocateMemory(o.ctx, uint64(t.NumElement()*sizeOfFloat32))
// 	// o.driver.Remap(o.ctx, uint64(t.ptr), uint64(t.NumElement()*sizeOfFloat32), gpuid)

// 	return t
}