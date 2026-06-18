package translationtrace

import (
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/sarchlab/akita/v3/sim"
)

const defaultWindowCycles = uint64(10000)

type componentSample struct {
	initialized bool
	lastCycle   uint64
	lastValue   float64
	lastFull    bool
}

type windowStat struct {
	start uint64
	end   uint64

	iommuMSHRUtilCycles float64
	iommuMSHRCycles     uint64
	iommuMSHRFullCycles uint64

	gmmuMSHRUtilCycles float64
	gmmuMSHRCycles     uint64
	gmmuMSHRFullCycles uint64

	sharedPTWUtilCycles float64
	sharedPTWCycles     uint64
	sharedPTWFullCycles uint64
	sharedPWQueueCycles uint64
	sharedPWQueueTime   uint64

	localPTWUtilCycles float64
	localPTWCycles     uint64
	localPTWFullCycles uint64

	gmmuPTELookupWaitingCycles  uint64
	gmmuPTELookupWaitingTime    uint64
	gmmuPTELookupInflightCycles uint64
	gmmuPTELookupInflightTime   uint64

	gmmuLocalIssue int
	gmmuIOMMUIssue int
	iommuIncoming  int
	iommuToMMU     int
}

type requestState struct {
	path        string
	children    []string
	activeStage map[string]uint64
	stageCycles map[string]uint64
}

type breakdownKey struct {
	path  string
	stage string
}

type breakdownStat struct {
	count int
	total uint64
}

type Tracer struct {
	sync.Mutex

	enabled      bool
	windowCycles uint64
	filePrefix   string

	samples map[string]*componentSample
	windows map[uint64]*windowStat

	parent     map[string]string
	requests   map[string]*requestState
	breakdowns map[breakdownKey]*breakdownStat
}

var global = &Tracer{}

func Configure(enabled bool, filePrefix string, windowCycles uint64) {
	global.Lock()
	defer global.Unlock()

	if windowCycles == 0 {
		windowCycles = defaultWindowCycles
	}

	global.enabled = enabled
	global.windowCycles = windowCycles
	global.filePrefix = filePrefix
	global.samples = make(map[string]*componentSample)
	global.windows = make(map[uint64]*windowStat)
	global.parent = make(map[string]string)
	global.requests = make(map[string]*requestState)
	global.breakdowns = make(map[breakdownKey]*breakdownStat)
}

func Enabled() bool {
	global.Lock()
	defer global.Unlock()
	return global.enabled
}

func cycle(now sim.VTimeInSec) uint64 {
	if now <= 0 {
		return 0
	}
	return uint64(float64(now) * 1e9)
}

func (t *Tracer) window(start uint64) *windowStat {
	windowStart := (start / t.windowCycles) * t.windowCycles
	stat := t.windows[windowStart]
	if stat == nil {
		stat = &windowStat{
			start: windowStart,
			end:   windowStart + t.windowCycles,
		}
		t.windows[windowStart] = stat
	}
	return stat
}

func (t *Tracer) integrate(
	start, end uint64,
	value float64,
	full bool,
	add func(*windowStat, uint64, float64, bool),
) {
	if end <= start {
		return
	}

	for start < end {
		stat := t.window(start)
		next := stat.end
		if next > end {
			next = end
		}
		delta := next - start
		add(stat, delta, value, full)
		start = next
	}
}

func (t *Tracer) observeValue(
	key string,
	now sim.VTimeInSec,
	value float64,
	full bool,
	add func(*windowStat, uint64, float64, bool),
) {
	if !t.enabled {
		return
	}

	nowCycle := cycle(now)
	sample := t.samples[key]
	if sample == nil {
		sample = &componentSample{}
		t.samples[key] = sample
	}

	if sample.initialized {
		t.integrate(sample.lastCycle, nowCycle, sample.lastValue, sample.lastFull, add)
	}

	sample.initialized = true
	sample.lastCycle = nowCycle
	sample.lastValue = value
	sample.lastFull = full
}

func ObserveGMMUTLB(
	now sim.VTimeInSec,
	name string,
	occupied int,
	capacity int,
	full bool,
	pteWaiting int,
	pteInflight int,
) {
	global.Lock()
	defer global.Unlock()

	util := 0.0
	if capacity > 0 {
		util = float64(occupied) / float64(capacity)
	}

	global.observeValue("gmmu-mshr:"+name, now, util, full,
		func(stat *windowStat, delta uint64, value float64, full bool) {
			stat.gmmuMSHRUtilCycles += value * float64(delta)
			stat.gmmuMSHRCycles += delta
			if full {
				stat.gmmuMSHRFullCycles += delta
			}
		})

	global.observeValue("gmmu-pte-wait:"+name, now, float64(pteWaiting), false,
		func(stat *windowStat, delta uint64, value float64, _ bool) {
			stat.gmmuPTELookupWaitingCycles += uint64(value * float64(delta))
			stat.gmmuPTELookupWaitingTime += delta
		})

	global.observeValue("gmmu-pte-inflight:"+name, now, float64(pteInflight), false,
		func(stat *windowStat, delta uint64, value float64, _ bool) {
			stat.gmmuPTELookupInflightCycles += uint64(value * float64(delta))
			stat.gmmuPTELookupInflightTime += delta
		})
}

func ObserveIOMMUTLB(
	now sim.VTimeInSec,
	name string,
	occupied int,
	capacity int,
	full bool,
) {
	global.Lock()
	defer global.Unlock()

	util := 0.0
	if capacity > 0 {
		util = float64(occupied) / float64(capacity)
	}

	global.observeValue("iommu-mshr:"+name, now, util, full,
		func(stat *windowStat, delta uint64, value float64, full bool) {
			stat.iommuMSHRUtilCycles += value * float64(delta)
			stat.iommuMSHRCycles += delta
			if full {
				stat.iommuMSHRFullCycles += delta
			}
		})
}

func ObserveSharedMMU(
	now sim.VTimeInSec,
	name string,
	inflight int,
	capacity int,
	pwQueueLen int,
	full bool,
) {
	global.Lock()
	defer global.Unlock()

	util := 0.0
	if capacity > 0 {
		util = float64(inflight) / float64(capacity)
	}

	global.observeValue("shared-ptw:"+name, now, util, full,
		func(stat *windowStat, delta uint64, value float64, full bool) {
			stat.sharedPTWUtilCycles += value * float64(delta)
			stat.sharedPTWCycles += delta
			if full {
				stat.sharedPTWFullCycles += delta
			}
		})

	global.observeValue("shared-pwqueue:"+name, now, float64(pwQueueLen), false,
		func(stat *windowStat, delta uint64, value float64, _ bool) {
			stat.sharedPWQueueCycles += uint64(value * float64(delta))
			stat.sharedPWQueueTime += delta
		})
}

func ObserveLocalGMMU(
	now sim.VTimeInSec,
	name string,
	inflight int,
	capacity int,
	full bool,
) {
	global.Lock()
	defer global.Unlock()

	util := 0.0
	if capacity > 0 {
		util = float64(inflight) / float64(capacity)
	}

	global.observeValue("local-ptw:"+name, now, util, full,
		func(stat *windowStat, delta uint64, value float64, full bool) {
			stat.localPTWUtilCycles += value * float64(delta)
			stat.localPTWCycles += delta
			if full {
				stat.localPTWFullCycles += delta
			}
		})
}

func event(now sim.VTimeInSec, add func(*windowStat)) {
	global.Lock()
	defer global.Unlock()
	if !global.enabled {
		return
	}

	add(global.window(cycle(now)))
}

func RecordGMMULocalIssue(now sim.VTimeInSec) {
	event(now, func(stat *windowStat) { stat.gmmuLocalIssue++ })
}

func RecordGMMUIOMMUIssue(now sim.VTimeInSec) {
	event(now, func(stat *windowStat) { stat.gmmuIOMMUIssue++ })
}

func RecordIOMMUIncoming(now sim.VTimeInSec) {
	event(now, func(stat *windowStat) { stat.iommuIncoming++ })
}

func RecordIOMMUToMMU(now sim.VTimeInSec) {
	event(now, func(stat *windowStat) { stat.iommuToMMU++ })
}

func StartRequest(id string) {
	global.Lock()
	defer global.Unlock()
	global.ensureRequestLocked(id)
}

func LinkRequest(childID, parentID string) {
	global.Lock()
	defer global.Unlock()
	if !global.enabled || childID == "" || parentID == "" {
		return
	}

	root := global.rootLocked(parentID)
	global.parent[childID] = root
	req := global.ensureRequestLocked(root)
	req.children = append(req.children, childID)
}

func SetPath(id string, path string) {
	global.Lock()
	defer global.Unlock()
	if !global.enabled || id == "" || path == "" {
		return
	}

	req := global.ensureRequestLocked(global.rootLocked(id))
	if req.path == "" || req.path == "unknown" {
		req.path = path
	}
}

func BeginStage(id string, stage string, now sim.VTimeInSec) {
	global.Lock()
	defer global.Unlock()
	if !global.enabled || id == "" || stage == "" {
		return
	}

	req := global.ensureRequestLocked(global.rootLocked(id))
	if _, active := req.activeStage[stage]; active {
		return
	}
	req.activeStage[stage] = cycle(now)
}

func EndStage(id string, stage string, now sim.VTimeInSec) {
	global.Lock()
	defer global.Unlock()
	if !global.enabled || id == "" || stage == "" {
		return
	}

	req := global.ensureRequestLocked(global.rootLocked(id))
	start, active := req.activeStage[stage]
	if !active {
		return
	}
	delete(req.activeStage, stage)

	nowCycle := cycle(now)
	if nowCycle > start {
		req.stageCycles[stage] += nowCycle - start
	}
}

func AddStageCycles(id string, stage string, cycles uint64) {
	global.Lock()
	defer global.Unlock()
	if !global.enabled || id == "" || stage == "" || cycles == 0 {
		return
	}

	req := global.ensureRequestLocked(global.rootLocked(id))
	req.stageCycles[stage] += cycles
}

func CompleteRequest(id string, now sim.VTimeInSec) {
	global.Lock()
	defer global.Unlock()
	if !global.enabled || id == "" {
		return
	}

	root := global.rootLocked(id)
	req := global.requests[root]
	if req == nil {
		return
	}

	nowCycle := cycle(now)
	for stage, start := range req.activeStage {
		if nowCycle > start {
			req.stageCycles[stage] += nowCycle - start
		}
	}

	path := req.path
	if path == "" {
		path = "unknown"
	}

	for stage, cycles := range req.stageCycles {
		if cycles == 0 {
			continue
		}
		key := breakdownKey{path: path, stage: stage}
		stat := global.breakdowns[key]
		if stat == nil {
			stat = &breakdownStat{}
			global.breakdowns[key] = stat
		}
		stat.count++
		stat.total += cycles
	}

	for _, child := range req.children {
		delete(global.parent, child)
	}
	delete(global.requests, root)
}

func (t *Tracer) ensureRequestLocked(id string) *requestState {
	if id == "" {
		id = "unknown"
	}

	root := t.rootLocked(id)
	req := t.requests[root]
	if req == nil {
		req = &requestState{
			path:        "unknown",
			activeStage: make(map[string]uint64),
			stageCycles: make(map[string]uint64),
		}
		t.requests[root] = req
	}
	return req
}

func (t *Tracer) rootLocked(id string) string {
	if id == "" {
		return "unknown"
	}

	root := id
	seen := make(map[string]struct{})
	for {
		parent, ok := t.parent[root]
		if !ok || parent == "" {
			return root
		}
		if _, loop := seen[parent]; loop {
			return root
		}
		seen[parent] = struct{}{}
		root = parent
	}
}

func Dump() error {
	global.Lock()
	defer global.Unlock()
	if !global.enabled || global.filePrefix == "" {
		return nil
	}

	if err := global.dumpPressureLocked(); err != nil {
		return err
	}
	return global.dumpBreakdownLocked()
}

func (t *Tracer) dumpPressureLocked() error {
	name := t.filePrefix + "_translation_pressure.csv"
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintln(f, "cycle_start,cycle_end,iommutlb_mshr_util,gmmutlb_avg_mshr_util,shared_mmu_ptw_util,local_gmmu_avg_ptw_util,iommutlb_mshr_full_cycles,gmmutlb_mshr_full_component_cycles,shared_mmu_ptw_full_cycles,local_gmmu_ptw_full_component_cycles,shared_mmu_pwqueue_avg_len,gmmu_pte_lookup_waiting_avg_len,gmmu_pte_lookup_inflight_avg_len,gmmu_local_issue,gmmu_iommu_issue,iommu_incoming,iommu_to_mmu")

	starts := make([]uint64, 0, len(t.windows))
	for start := range t.windows {
		starts = append(starts, start)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })

	for _, start := range starts {
		stat := t.windows[start]
		fmt.Fprintf(
			f,
			"%d,%d,%.6f,%.6f,%.6f,%.6f,%d,%d,%d,%d,%.6f,%.6f,%.6f,%d,%d,%d,%d\n",
			stat.start,
			stat.end,
			avgFloat(stat.iommuMSHRUtilCycles, stat.iommuMSHRCycles),
			avgFloat(stat.gmmuMSHRUtilCycles, stat.gmmuMSHRCycles),
			avgFloat(stat.sharedPTWUtilCycles, stat.sharedPTWCycles),
			avgFloat(stat.localPTWUtilCycles, stat.localPTWCycles),
			stat.iommuMSHRFullCycles,
			stat.gmmuMSHRFullCycles,
			stat.sharedPTWFullCycles,
			stat.localPTWFullCycles,
			avgUint(stat.sharedPWQueueCycles, stat.sharedPWQueueTime),
			avgUint(stat.gmmuPTELookupWaitingCycles, stat.gmmuPTELookupWaitingTime),
			avgUint(stat.gmmuPTELookupInflightCycles, stat.gmmuPTELookupInflightTime),
			stat.gmmuLocalIssue,
			stat.gmmuIOMMUIssue,
			stat.iommuIncoming,
			stat.iommuToMMU,
		)
	}

	return nil
}

func (t *Tracer) dumpBreakdownLocked() error {
	name := t.filePrefix + "_translation_breakdown.csv"
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintln(f, "path,stage,count,total_cycles,avg_cycles")

	keys := make([]breakdownKey, 0, len(t.breakdowns))
	for key := range t.breakdowns {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].path == keys[j].path {
			return keys[i].stage < keys[j].stage
		}
		return keys[i].path < keys[j].path
	})

	for _, key := range keys {
		stat := t.breakdowns[key]
		avg := 0.0
		if stat.count > 0 {
			avg = float64(stat.total) / float64(stat.count)
		}
		fmt.Fprintf(f, "%s,%s,%d,%d,%.6f\n",
			key.path, key.stage, stat.count, stat.total, avg)
	}

	return nil
}

func avgFloat(total float64, count uint64) float64 {
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

func avgUint(total uint64, count uint64) float64 {
	if count == 0 {
		return 0
	}
	return float64(total) / float64(count)
}
