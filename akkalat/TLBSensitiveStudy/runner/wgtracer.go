package runner

import (
	"github.com/sarchlab/akita/v3/tracing"
	"github.com/tebeka/atexit"
)

// wgTracer can trace the number of instruction completed.
type wgTracer struct {
	count     uint64
	simdInst  bool
	simdCount uint64
	maxCount  uint64

	inflightInst map[string]tracing.Task
}

// newWGTracer creates a tracer that can count the number of instructions.
func newWGTracer() *wgTracer {
	t := &wgTracer{
		inflightInst: map[string]tracing.Task{},
	}
	return t
}

// newWGStopper with stop the execution after a given number of instructions
// is retired.
func newWGStopper(maxInst uint64) *wgTracer {
	t := &wgTracer{
		maxCount:     maxInst,
		inflightInst: map[string]tracing.Task{},
	}
	return t
}

func (t *wgTracer) StartTask(task tracing.Task) {
	if task.Kind != "req_in" {
		return
	}

	// if task.What == "*protocol.WGCompletionMsg" {
	if task.What == "*protocol.MapWGReq" {
		t.simdInst = true
	} else {
		return
	}

	if _, exists := t.inflightInst[task.ID]; exists {
		return
	}

	// fmt.Printf("SIMD instruction started: %s\n", task.ID)

	t.inflightInst[task.ID] = task
}

func (t *wgTracer) StepTask(task tracing.Task) {
	// Do nothing
}

func (t *wgTracer) EndTask(task tracing.Task) {
	_, found := t.inflightInst[task.ID]
	if !found {
		return
	}

	if t.simdInst {
		t.simdCount++
	}

	delete(t.inflightInst, task.ID)

	t.count++

	if t.maxCount > 0 && t.count >= t.maxCount {
		// fmt.Printf("Reached max instruction count %d, exiting...\n", t.maxCount)
		atexit.Exit(0)
	}
}
