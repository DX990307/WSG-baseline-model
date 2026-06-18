package runner

import (
	"github.com/sarchlab/akita/v3/mem/vm/gmmu"
	"github.com/sarchlab/akita/v3/mem/vm/mmu"
	"github.com/sarchlab/akita/v3/mem/vm/tlb_gmmu"
	"github.com/sarchlab/akita/v3/sim"
	"github.com/sarchlab/akita/v3/tracing"
	"github.com/sarchlab/mgpusim/v3/driver"
	"github.com/sarchlab/mgpusim/v3/timing/pagemigrationcontroller"
	"github.com/sarchlab/mgpusim/v3/timing/rdma"
)

// TraceableComponent is a component that can accept traces
type TraceableComponent interface {
	sim.Component
	tracing.NamedHookable
}

// A Platform is a collection of the hardware under simulation.
type Platform struct {
	Engine sim.Engine
	Driver *driver.Driver
	GPUs   []*GPU
}

// A GPU is a collection of GPU internal Components
type GPU struct {
	GPUID  uint64
	Domain *sim.Domain
	// CommandProcessor *cp.CommandProcessor
	CommandProcessor TraceableComponent
	RDMAEngine       *rdma.Comp
	MMUEngine        *mmu.MMU
	GMMUEngine       *gmmu.GMMU
	GMMUTLB          *tlb_gmmu.GMMUTLB
	// GMMUTLB *tlb.TLB

	PMC            *pagemigrationcontroller.PageMigrationController
	CUs            []TraceableComponent
	L1VCaches      []TraceableComponent
	L1SCaches      []TraceableComponent
	L1ICaches      []TraceableComponent
	L2Caches       []TraceableComponent
	L1VTLBs        []TraceableComponent
	L1STLBs        []TraceableComponent
	L1ITLBs        []TraceableComponent
	L2TLBs         []TraceableComponent
	MemControllers []TraceableComponent
	// GMMUCache      []TraceableComponent
	// MMUEngine      []TraceableComponent
	// GMMUEngine     []TraceableComponent

}
