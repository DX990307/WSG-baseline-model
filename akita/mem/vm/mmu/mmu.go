package mmu

import (
	"log"
	"reflect"

	"github.com/sarchlab/akita/v3/mem/mem"
	"github.com/sarchlab/akita/v3/mem/vm"
	"github.com/sarchlab/akita/v3/mem/vm/translationtrace"
	"github.com/sarchlab/akita/v3/sim"
	"github.com/sarchlab/akita/v3/tracing"
)

type transaction struct {
	req       *vm.TranslationReq
	page      vm.Page
	cycleLeft int
	migration *vm.PageMigrationReqToDriver
}

type PWqueue struct {
	req *vm.TranslationReq
}

// MMU is the default mmu implementation. It is also an akita Component.
type MMU struct {
	sim.TickingComponent

	topPort       sim.Port
	migrationPort sim.Port
	TopModule     sim.Port

	MigrationServiceProvider sim.Port

	topSender sim.BufferedSender

	pageTable           vm.PageTable
	latency             int
	maxRequestsInFlight int
	log2PageSize        uint64

	mockBuffer               []*vm.TranslationReq
	walkingTranslations      []transaction
	migrationQueue           []transaction
	migrationQueueSize       int
	currentOnDemandMigration transaction
	isDoingMigration         bool

	toRemoveFromPTW         []int
	PageAccessedByDeviceID  map[uint64][]uint64
	walkCoalescingEnabled   bool
	lastLevelCoalescedCount int
	twoLevelCoalescedCount  int

	// PWqueue        []sim.Msg
	PWqueue        []PWqueue
	GMMUCacheTable *mem.MultiPageFinder
}

// Tick defines how the MMU update state each cycle
func (mmu *MMU) Tick(now sim.VTimeInSec) bool {
	madeProgress := false

	for i := 0; i < 48; i++ {
		madeProgress = mmu.parseFromTop(now) || madeProgress
	}
	madeProgress = mmu.processTranslationReqs(now) || madeProgress
	for i := 0; i < 48; i++ {
		madeProgress = mmu.topSender.Tick(now) || madeProgress
	}
	madeProgress = mmu.sendMigrationToDriver(now) || madeProgress
	madeProgress = mmu.walkPageTable(now) || madeProgress
	madeProgress = mmu.processMigrationReturn(now) || madeProgress

	translationtrace.ObserveSharedMMU(
		now,
		mmu.Name(),
		len(mmu.walkingTranslations),
		mmu.maxRequestsInFlight,
		len(mmu.PWqueue),
		len(mmu.walkingTranslations) >= mmu.maxRequestsInFlight,
	)

	return madeProgress
}

func (mmu *MMU) HasFreePTW() bool {
	return len(mmu.walkingTranslations) < mmu.maxRequestsInFlight
}

func (mmu *MMU) PTWInflight() int {
	return len(mmu.walkingTranslations)
}

func (mmu *MMU) PTWCapacity() int {
	return mmu.maxRequestsInFlight
}

func (mmu *MMU) walkPageTable(now sim.VTimeInSec) bool {
	madeProgress := false
	for i := 0; i < len(mmu.walkingTranslations); i++ {
		if mmu.walkingTranslations[i].cycleLeft > 0 {
			mmu.walkingTranslations[i].cycleLeft--
			madeProgress = true
			continue
		}

		madeProgress = mmu.finalizePageWalk(now, i) || madeProgress
	}

	tmp := mmu.walkingTranslations[:0]
	for i := 0; i < len(mmu.walkingTranslations); i++ {
		if !mmu.toRemove(i) {
			tmp = append(tmp, mmu.walkingTranslations[i])
		}
	}
	mmu.walkingTranslations = tmp
	mmu.toRemoveFromPTW = nil

	return madeProgress
}

func (mmu *MMU) finalizePageWalk(
	now sim.VTimeInSec,
	walkingIndex int,
) bool {
	req := mmu.walkingTranslations[walkingIndex].req
	page, found := mmu.lookupRequestPage(req)

	if !found {
		panic("page not found")
	}

	mmu.walkingTranslations[walkingIndex].page = page

	if page.IsMigrating {
		return mmu.addTransactionToMigrationQueue(walkingIndex)
	}

	if mmu.pageNeedMigrate(mmu.walkingTranslations[walkingIndex]) {
		return mmu.addTransactionToMigrationQueue(walkingIndex)
	}

	return mmu.doPageWalkHit(now, walkingIndex)
}

func (mmu *MMU) doPageWalkHit(
	now sim.VTimeInSec,
	walkingIndex int,
) bool {
	walking := mmu.walkingTranslations[walkingIndex]
	if mmu.hasBitmap(walking.req.BitMap) {
		return mmu.doPTCLPageWalkHit(now, walkingIndex)
	}

	madeProgress := false

	if !mmu.topSender.CanSend(1) {
		return false
	}

	rsp := vm.TranslationRspBuilder{}.
		WithSendTime(now).
		WithSrc(mmu.topPort).
		WithDst(walking.req.Src).
		WithRspTo(walking.req.ID).
		WithPage(walking.page).
		WithTaskID(walking.req.TaskID).
		WithOriginPort(walking.req.OriginPort).
		WithPrefetch(walking.req.IsPrefetch).
		Build()

	if !mmu.topSender.CanSend(1) {
		return false
	}

	mmu.topSender.Send(rsp)

	madeProgress = true
	madeProgress = mmu.sendToGMMU(now, walking) || madeProgress

	mmu.toRemoveFromPTW = append(mmu.toRemoveFromPTW, walkingIndex)

	tracing.TraceReqComplete(walking.req, mmu)

	return madeProgress
}

func (mmu *MMU) doPTCLPageWalkHit(
	now sim.VTimeInSec,
	walkingIndex int,
) bool {
	walking := mmu.walkingTranslations[walkingIndex]
	if !mmu.sendPTCLResponses(now, walking.req) {
		return false
	}

	mmu.toRemoveFromPTW = append(mmu.toRemoveFromPTW, walkingIndex)
	tracing.TraceReqComplete(walking.req, mmu)

	return true
}

func (mmu *MMU) addTransactionToMigrationQueue(walkingIndex int) bool {
	if len(mmu.migrationQueue) >= mmu.migrationQueueSize {
		return false
	}

	mmu.toRemoveFromPTW = append(mmu.toRemoveFromPTW, walkingIndex)
	mmu.migrationQueue = append(mmu.migrationQueue,
		mmu.walkingTranslations[walkingIndex])

	page := mmu.walkingTranslations[walkingIndex].page
	page.IsMigrating = true
	mmu.pageTable.Update(page)

	return true
}

func (mmu *MMU) pageNeedMigrate(walking transaction) bool {
	if walking.req.IsPrefetch {
		return false
	}

	if walking.req.DeviceID == walking.page.DeviceID {
		return false
	}

	if !walking.page.Unified {
		return false
	}

	if walking.page.IsPinned {
		return false
	}

	return true
}

func (mmu *MMU) sendMigrationToDriver(
	now sim.VTimeInSec,
) (madeProgress bool) {
	if len(mmu.migrationQueue) == 0 {
		return false
	}

	trans := mmu.migrationQueue[0]
	req := trans.req
	page := trans.page
	if page == (vm.Page{}) {
		var found bool
		page, found = mmu.lookupRequestPage(req)
		if !found {
			panic("page not found")
		}
	}
	trans.page = page

	if req.DeviceID == page.DeviceID || page.IsPinned {
		mmu.sendTranlationRsp(now, trans)
		mmu.migrationQueue = mmu.migrationQueue[1:]
		mmu.markPageAsNotMigratingIfNotInTheMigrationQueue(page)

		return true
	}

	if mmu.isDoingMigration {
		return false
	}

	migrationInfo := new(vm.PageMigrationInfo)
	migrationInfo.GPUReqToVAddrMap = make(map[uint64][]uint64)
	migrationInfo.GPUReqToVAddrMap[trans.req.DeviceID] =
		append(migrationInfo.GPUReqToVAddrMap[trans.req.DeviceID],
			trans.req.VAddr)

	mmu.PageAccessedByDeviceID[page.VAddr] =
		append(mmu.PageAccessedByDeviceID[page.VAddr], page.DeviceID)

	migrationReq := vm.NewPageMigrationReqToDriver(
		now, mmu.migrationPort, mmu.MigrationServiceProvider)
	migrationReq.PID = page.PID
	migrationReq.PageSize = page.PageSize
	migrationReq.CurrPageHostGPU = page.DeviceID
	migrationReq.MigrationInfo = migrationInfo
	migrationReq.CurrAccessingGPUs = unique(mmu.PageAccessedByDeviceID[page.VAddr])
	migrationReq.RespondToTop = true

	err := mmu.migrationPort.Send(migrationReq)
	if err != nil {
		return false
	}

	trans.page.IsMigrating = true
	mmu.pageTable.Update(trans.page)
	trans.migration = migrationReq
	mmu.isDoingMigration = true
	mmu.currentOnDemandMigration = trans
	mmu.migrationQueue = mmu.migrationQueue[1:]

	return true
}

func (mmu *MMU) markPageAsNotMigratingIfNotInTheMigrationQueue(
	page vm.Page,
) vm.Page {
	inQueue := false
	for _, t := range mmu.migrationQueue {
		if page.PAddr == t.page.PAddr {
			inQueue = true
			break
		}
	}

	if !inQueue {
		page.IsMigrating = false
		mmu.pageTable.Update(page)
		return page
	}

	return page
}

func (mmu *MMU) sendTranlationRsp(
	now sim.VTimeInSec,
	trans transaction,
) (madeProgress bool) {
	req := trans.req
	if mmu.hasBitmap(req.BitMap) {
		return mmu.sendPTCLResponses(now, req)
	}

	page := trans.page

	rsp := vm.TranslationRspBuilder{}.
		WithSendTime(now).
		WithSrc(mmu.topPort).
		WithDst(req.OriginPort).
		WithRspTo(req.ID).
		WithPage(page).
		Build()
	mmu.topSender.Send(rsp)

	return true
}

func (mmu *MMU) processMigrationReturn(now sim.VTimeInSec) bool {
	item := mmu.migrationPort.Peek()
	if item == nil {
		return false
	}

	if !mmu.topSender.CanSend(1) {
		return false
	}

	req := mmu.currentOnDemandMigration.req
	page, found := mmu.lookupRequestPage(req)
	if !found {
		panic("page not found")
	}

	if mmu.hasBitmap(req.BitMap) {
		if !mmu.sendPTCLResponses(now, req) {
			return false
		}
	} else {
		rsp := vm.TranslationRspBuilder{}.
			WithSendTime(now).
			WithSrc(mmu.topPort).
			WithDst(req.OriginPort).
			WithRspTo(req.ID).
			WithPage(page).
			Build()
		mmu.topSender.Send(rsp)
	}

	mmu.isDoingMigration = false

	page = mmu.markPageAsNotMigratingIfNotInTheMigrationQueue(page)
	page.IsPinned = true
	mmu.pageTable.Update(page)

	mmu.migrationPort.Retrieve(now)

	return true
}

func (mmu *MMU) parseFromTop(now sim.VTimeInSec) bool {
	madeProgress := false

	for mmu.topPort.Peek() != nil {
		msg := mmu.topPort.Peek()

		if mmu.topPort.Peek() == nil {
			return false
		}

		switch msg := msg.(type) {
		case *vm.TranslationReq:
			mmu.mockBuffer = append(mmu.mockBuffer, msg)
			mmu.topPort.Retrieve(now)
		default:
			log.Panicf("MMU canot handle request of type %s", reflect.TypeOf(msg))
		}
	}

	for len(mmu.PWqueue) < 64 {
		if len(mmu.mockBuffer) == 0 {
			break
		}

		req := mmu.mockBuffer[0]

		if req == nil {
			break
		}

		mmu.PWqueue = append(mmu.PWqueue, PWqueue{req: req})
		mmu.topPort.Retrieve(now)
		if !req.IsPrefetch {
			translationtrace.BeginStage(req.ID, "shared_mmu_pwqueue_wait", now)
		}

		mmu.mockBuffer = mmu.mockBuffer[1:]
		tracing.TraceReqReceive(req, mmu)

		madeProgress = true
	}
	return madeProgress
}

func (mmu *MMU) processTranslationReqs(now sim.VTimeInSec) bool {
	madeProgress := false

	if len(mmu.walkingTranslations) >= mmu.maxRequestsInFlight {
		// fmt.Printf("inflight translations %d\n", len(mmu.walkingTranslations))
		return false
	}

	// fmt.Printf("inflight translations %d\n", len(mmu.walkingTranslations))

	for i := 0; i < mmu.maxRequestsInFlight; i++ {
		if len(mmu.PWqueue) == 0 {
			break
		}

		pw := mmu.PWqueue[0]
		mmu.PWqueue = mmu.PWqueue[1:]

		if pw.req == nil {
			break
		}

		tracing.TraceReqReceive(pw.req, mmu)

		// switch req := pw.req.(type) {
		// case *vm.TranslationReq:
		// 	mmu.startWalking(req)
		// default:
		// 	log.Panicf("MMU canot handle request of type %s", reflect.TypeOf(req))
		// }

		mmu.startWalking(pw.req, mmu.coalescedUpperLatency(pw.req), now)
		madeProgress = true

		if len(mmu.walkingTranslations) >= mmu.maxRequestsInFlight {
			break
		}
	}

	return madeProgress
}

func (mmu *MMU) startWalking(
	req *vm.TranslationReq,
	upperLatency uint64,
	now sim.VTimeInSec,
) {
	l := upperLatency + 100

	translationInPipeline := transaction{
		req:       req,
		cycleLeft: int(l),
	}

	mmu.walkingTranslations = append(mmu.walkingTranslations, translationInPipeline)
	if req != nil && !req.IsPrefetch {
		translationtrace.EndStage(req.ID, "shared_mmu_pwqueue_wait", now)
		translationtrace.AddStageCycles(
			req.ID,
			"shared_mmu_ptw_service",
			l,
		)
	}
}

func (mmu *MMU) hasBitmap(bitmap [8]bool) bool {
	for i := 0; i < 8; i++ {
		if bitmap[i] {
			return true
		}
	}
	return false
}

func (mmu *MMU) lookupRequestPage(req *vm.TranslationReq) (vm.Page, bool) {
	if !mmu.hasBitmap(req.BitMap) {
		return mmu.pageTable.Find(req.PID, req.VAddr)
	}

	baseVAddr := mmu.getBaseVAddr(req.VAddr)
	for i := 0; i < 8; i++ {
		if !req.BitMap[i] {
			continue
		}

		pageVAddr := baseVAddr + (uint64(i) << mmu.log2PageSize)
		page, found := mmu.pageTable.Find(req.PID, pageVAddr)
		if found {
			return page, true
		}
	}

	return vm.Page{}, false
}

func (mmu *MMU) collectRequestedPages(req *vm.TranslationReq) []vm.Page {
	baseVAddr := mmu.getBaseVAddr(req.VAddr)
	pages := make([]vm.Page, 0, 8)

	for i := 0; i < 8; i++ {
		if !req.BitMap[i] {
			continue
		}

		pageVAddr := baseVAddr + (uint64(i) << mmu.log2PageSize)
		page, found := mmu.pageTable.Find(req.PID, pageVAddr)
		if !found {
			panic("requested ptcl page not found")
		}

		pages = append(pages, page)
	}

	return pages
}

func (mmu *MMU) sendPTCLResponses(now sim.VTimeInSec, req *vm.TranslationReq) bool {
	pages := mmu.collectRequestedPages(req)
	if len(pages) == 0 {
		panic("ptcl walk returned no requested pages")
	}

	if !mmu.topSender.CanSend(len(pages)) {
		return false
	}

	for _, page := range pages {
		rsp := vm.TranslationRspBuilder{}.
			WithSendTime(now).
			WithSrc(mmu.topPort).
			WithDst(req.Src).
			WithRspTo(req.ID).
			WithPage(page).
			WithTaskID(req.TaskID).
			WithOriginPort(req.OriginPort).
			WithPrefetch(req.IsPrefetch).
			Build()

		mmu.topSender.Send(rsp)
	}

	return true
}

func (mmu *MMU) getBaseVAddr(vAddr uint64) uint64 {
	vpn := vAddr >> mmu.log2PageSize
	baseVPN := (vpn >> 3) << 3
	return baseVPN << mmu.log2PageSize
}

func (mmu *MMU) toRemove(index int) bool {
	for i := 0; i < len(mmu.toRemoveFromPTW); i++ {
		remove := mmu.toRemoveFromPTW[i]
		if remove == index {
			return true
		}
	}
	return false
}

func unique(intSlice []uint64) []uint64 {
	keys := make(map[int]bool)
	list := []uint64{}
	for _, entry := range intSlice {
		if _, value := keys[int(entry)]; !value {
			keys[int(entry)] = true
			list = append(list, entry)
		}
	}
	return list
}

func (mmu *MMU) sendToGMMU(now sim.VTimeInSec, walking transaction) bool {
	madeProgress := false

	cacheLine := mmu.getCacheLine(walking.req.VAddr)

	for i := 0; i < 8; i++ {
		vpn := cacheLine[i]
		newVAddr := vpn << mmu.log2PageSize

		page, found := mmu.pageTable.Find(walking.req.PID, newVAddr)
		if !found {
			page = vm.Page{
				PID:      walking.req.PID,
				VAddr:    newVAddr,
				Valid:    false,
				IsPinned: false,
			}
		}

		taskID := sim.GetIDGenerator().Generate()

		rsp := vm.TranslationRspBuilder{}.
			WithSendTime(now).
			WithSrc(mmu.topPort).
			WithDst(mmu.TopModule).
			WithPage(page).
			WithOriginPort(walking.req.OriginPort).
			WithPrefetch(walking.req.IsPrefetch).
			WithTaskID(taskID).
			Build()

		if !mmu.topSender.CanSend(1) {
			return madeProgress
		}
		mmu.topSender.Send(rsp)
		madeProgress = true

		// fmt.Printf("sendToGMMU %d\n", page.VAddr>>12)
	}
	return madeProgress
}

func (mmu *MMU) getCacheLine(vaddr uint64) []uint64 {
	currentVPN := vaddr >> mmu.log2PageSize
	// baseVPN := currentVPN &^ 0x8 // 8 entries per cache line
	baseVPN := currentVPN &^ 0x7 // 8 entries per cache line
	return []uint64{baseVPN, baseVPN + 1, baseVPN + 2, baseVPN + 3, baseVPN + 4, baseVPN + 5, baseVPN + 6, baseVPN + 7}
}

func (mmu *MMU) isInTheSameLastLevel(vAddr1, vAddr2 uint64) bool {
	baseVPN1 := (vAddr1 >> mmu.log2PageSize)
	baseVPN2 := (vAddr2 >> mmu.log2PageSize)

	baseLastLevel1 := baseVPN1 >> 6
	baseLastLevel2 := baseVPN2 >> 6

	return baseLastLevel1 == baseLastLevel2
}

func (mmu *MMU) isInTheSameTwoLevels(vAddr1, vAddr2 uint64) bool {
	baseVPN1 := vAddr1 >> mmu.log2PageSize
	baseVPN2 := vAddr2 >> mmu.log2PageSize

	return (baseVPN1 >> 12) == (baseVPN2 >> 12)
}

func (mmu *MMU) coalescedUpperLatency(req *vm.TranslationReq) uint64 {
	upperLatency := req.TransLatency
	if !mmu.walkCoalescingEnabled {
		return upperLatency
	}

	for _, walking := range mmu.walkingTranslations {
		if walking.req == nil || walking.req.PID != req.PID {
			continue
		}

		if mmu.isInTheSameLastLevel(req.VAddr, walking.req.VAddr) {
			mmu.lastLevelCoalescedCount++
			return 0
		}

		if mmu.isInTheSameTwoLevels(req.VAddr, walking.req.VAddr) &&
			upperLatency > 100 {
			mmu.twoLevelCoalescedCount++
			upperLatency = 100
		}
	}

	return upperLatency
}
