package mmuCache

import (
	"log"
	"reflect"

	"github.com/sarchlab/akita/v3/mem/vm"
	"github.com/sarchlab/akita/v3/mem/vm/mmuCache/internal"
	"github.com/sarchlab/akita/v3/mem/vm/translationtrace"
	"github.com/sarchlab/akita/v3/sim"
)

type subPage struct {
	subPage internal.Set
}

// A TLB is a cache that maintains some page information.
type MMUCache struct {
	*sim.TickingComponent

	topPort     sim.Port
	bottomPort  sim.Port
	controlPort sim.Port

	UpModule  sim.Port
	LowModule sim.Port

	numSets         int
	numWays         int
	numLevels       int
	pageSize        uint64
	log2PageSize    uint64
	numReqPerCycle  int
	latencyPerLevel uint64

	table []subPage

	reqBuffer []*vm.TranslationReq
}

// Reset sets all the entries int he TLB to be invalid
func (cache *MMUCache) reset() {
	cache.table = make([]subPage, cache.numLevels)
	for i := 0; i < cache.numLevels; i++ {
		cache.table[i].subPage = internal.NewSet(cache.numSets)
	}
}

// Tick defines how TLB update states at each cycle
func (cache *MMUCache) Tick(now sim.VTimeInSec) bool {
	madeProgress := false

	for i := 0; i < cache.numReqPerCycle; i++ {
		madeProgress = cache.lookup(now) || madeProgress
	}

	for i := 0; i < cache.numReqPerCycle; i++ {
		madeProgress = cache.parseBottom(now) || madeProgress
	}

	return madeProgress
}

func (cache *MMUCache) lookup(now sim.VTimeInSec) bool {
	if cache.bottomPort.CanSend() == false {
		return false
	}

	msg := cache.topPort.Peek()

	if msg == nil {
		return false
	}

	req := msg.(*vm.TranslationReq)

	if req == nil {
		return false
	}

	madeProgress := false

	madeProgress = cache.handleTranslationHits(now, req) || madeProgress

	return madeProgress
}

func (cache *MMUCache) handleTranslationHits(now sim.VTimeInSec, req *vm.TranslationReq) bool {
	totalLatency := cache.latencyPerLevel * uint64(cache.numLevels)

	for level := cache.numLevels - 1; level >= 0; level-- {
		found := cache.subPageTableWalk(level, req)
		if !found {
			break
		}
		totalLatency -= cache.latencyPerLevel
	}

	ok := cache.sendReqToBottom(now, req, totalLatency)
	if !ok {
		return false
	}
	return true
}

func (cache *MMUCache) subPageTableWalk(level int, req *vm.TranslationReq) bool {
	vAddr := req.VAddr
	pid := req.PID

	vpn := vAddr >> cache.log2PageSize
	levelwidth := (64 - cache.log2PageSize) / uint64(cache.numLevels)
	seg := (vpn >> (uint64(level) * levelwidth)) & ((1 << levelwidth) - 1)

	subTable := cache.table[level]
	wayID, found := subTable.subPage.Lookup(pid, seg)

	if found {
		subTable.subPage.Visit(wayID)
		return true
	}
	return false
}

func (cache *MMUCache) sendReqToBottom(
	now sim.VTimeInSec,
	req *vm.TranslationReq,
	latency uint64) bool {

	if cache.bottomPort.CanSend() == false {
		return false
	}

	reqToBottom := vm.TranslationReqBuilder{}.
		WithSendTime(now).
		WithSrc(cache.bottomPort).
		WithDst(cache.LowModule).
		WithPID(req.PID).
		WithVAddr(req.VAddr).
		WithDeviceID(req.DeviceID).
		WithTaskID(req.TaskID).
		WithOriginPort(req.OriginPort).
		WithBitMap(req.BitMap).
		WithTransLatency(latency).
		WithPrefetch(req.IsPrefetch).
		Build()
	reqToBottom.StartGPUID = req.StartGPUID

	// fmt.Printf("ToIOMMU VAddr %d\n", req.VAddr)

	err := cache.bottomPort.Send(reqToBottom)
	if err != nil {
		return false
	}

	translationtrace.LinkRequest(reqToBottom.ID, req.ID)
	if !req.IsPrefetch {
		translationtrace.AddStageCycles(
			req.ID,
			"iommucache_upper_level_latency",
			latency,
		)
	}
	cache.topPort.Retrieve(now)

	return true
}

func (cache *MMUCache) parseBottom(now sim.VTimeInSec) bool {
	madeProgress := false

	item := cache.bottomPort.Peek()
	if item == nil {
		return false
	}

	switch rsp := item.(type) {
	case *vm.TranslationRsp:
		madeProgress = cache.handleRsp(now, rsp) || madeProgress
	default:
		log.Panicf("cannot process request %s", reflect.TypeOf(item))
	}
	return madeProgress
}

func (cache *MMUCache) handleRsp(now sim.VTimeInSec, rsp *vm.TranslationRsp) bool {

	if !cache.topPort.CanSend() {
		return false
	}

	cache.saveVPNToTable(rsp)

	rspToTop := vm.TranslationRspBuilder{}.
		WithSendTime(now).
		WithSrc(cache.topPort).
		WithDst(cache.UpModule).
		WithRspTo(rsp.RespondTo).
		WithPage(rsp.Page).
		WithTaskID(rsp.TaskID).
		WithOriginPort(rsp.OriginPort).
		WithPrefetch(rsp.IsPrefetch).
		Build()

	// fmt.Printf("ToTLB VAddr %d\n", rsp.Page.VAddr)

	err := cache.topPort.Send(rspToTop)
	if err != nil {
		return false
	}

	cache.bottomPort.Retrieve(now)

	return true
}

func (cache *MMUCache) segToSetID(seg uint64) (setID int) {
	return int(seg % uint64(cache.numSets))
}

func (cache *MMUCache) saveVPNToTable(rsp *vm.TranslationRsp) bool {
	page := rsp.Page
	vAddr := page.VAddr
	pid := page.PID

	vpn := vAddr >> cache.log2PageSize
	levelwidth := (64 - cache.log2PageSize) / uint64(cache.numLevels)
	for level := cache.numLevels - 1; level >= 0; level-- {
		seg := (vpn >> (uint64(level) * levelwidth)) & ((1 << levelwidth) - 1)

		subTable := cache.table[level]
		wayID := cache.segToSetID(seg)

		_, found := subTable.subPage.Lookup(pid, seg)
		if found {
			subTable.subPage.Evict()
		}

		subTable.subPage.Update(wayID, pid, seg)
	}

	return true
}
