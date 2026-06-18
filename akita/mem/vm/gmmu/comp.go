package gmmu

import (
	"log"
	"reflect"

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

// gmmu is the default gmmu implementation. It is also an akita Component.
type GMMU struct {
	sim.TickingComponent

	deviceID uint64

	topPort    sim.Port
	bottomPort sim.Port

	LowModule sim.Port

	log2PageSize uint64

	MigrationServiceProvider sim.Port

	topSender    sim.BufferedSender
	bottomSender sim.BufferedSender

	pageTable           vm.PageTable
	latency             int
	maxRequestsInFlight int

	walkingTranslations []transaction
	remoteMemReqs       map[uint64]transaction

	toRemoveFromPTW        []int
	PageAccessedByDeviceID map[uint64][]uint64

	isRecording  bool
	gpuIDList    []uint64
	isPrediction bool
}

func (gmmu *GMMU) HasFreePTW() bool {
	return len(gmmu.walkingTranslations) < gmmu.maxRequestsInFlight
}

func (gmmu *GMMU) PTWInflight() int {
	return len(gmmu.walkingTranslations)
}

func (gmmu *GMMU) PTWCapacity() int {
	return gmmu.maxRequestsInFlight
}

// Tick defines how the gmmu update state each cycle
func (gmmu *GMMU) Tick(now sim.VTimeInSec) bool {
	madeProgress := false

	for i := 0; i < 8; i++ {
		madeProgress = gmmu.topSender.Tick(now) || madeProgress
	}
	madeProgress = gmmu.parseFromTop(now) || madeProgress
	madeProgress = gmmu.walkPageTable(now) || madeProgress
	madeProgress = gmmu.fetchFromBottom(now) || madeProgress

	translationtrace.ObserveLocalGMMU(
		now,
		gmmu.Name(),
		len(gmmu.walkingTranslations),
		gmmu.maxRequestsInFlight,
		len(gmmu.walkingTranslations) >= gmmu.maxRequestsInFlight,
	)

	return madeProgress
}

func (gmmu *GMMU) parseFromTop(now sim.VTimeInSec) bool {
	if len(gmmu.walkingTranslations) >= gmmu.maxRequestsInFlight {
		msg := gmmu.topPort.Peek()
		if req, ok := msg.(*vm.TranslationReq); ok && req != nil && !req.IsPrefetch {
			translationtrace.BeginStage(req.ID, "local_gmmu_ptw_queue_wait", now)
		}
		return false
	}

	req := gmmu.topPort.Retrieve(now)
	if req == nil {
		return false
	}

	tracing.TraceReqReceive(req, gmmu)

	switch req := req.(type) {
	case *vm.TranslationReq:
		if !req.IsPrefetch {
			translationtrace.EndStage(req.ID, "local_gmmu_ptw_queue_wait", now)
		}
		gmmu.startWalking(req, now)
		tracing.StartTask(req.TaskID, "", gmmu, "GMMU", "TranslationReq", req)

	default:
		log.Panicf("gmmu canot handle request of type %s", reflect.TypeOf(req))
	}

	return true
}

func (gmmu *GMMU) startWalking(req *vm.TranslationReq, now sim.VTimeInSec) {
	translationInPipeline := transaction{
		req:       req,
		cycleLeft: gmmu.latency,
	}

	gmmu.walkingTranslations = append(gmmu.walkingTranslations, translationInPipeline)
	if req != nil && !req.IsPrefetch {
		translationtrace.AddStageCycles(
			req.ID,
			"local_gmmu_ptw_service",
			uint64(gmmu.latency),
		)
	}
}

func (gmmu *GMMU) sendReqToBottomPort(now sim.VTimeInSec) {
	lastTranslation := gmmu.walkingTranslations[len(gmmu.walkingTranslations)-1]
	req := lastTranslation.req

	page, _ := gmmu.pageTable.Find(req.PID, req.VAddr)

	if page.DeviceID == gmmu.deviceID {
		return
	}

	gmmu.processRemoteMemReq(now, len(gmmu.walkingTranslations)-1)

	tmp := gmmu.walkingTranslations[:0]
	for i := 0; i < len(gmmu.walkingTranslations); i++ {
		if !gmmu.toRemove(i) {
			tmp = append(tmp, gmmu.walkingTranslations[i])
		}
	}
	gmmu.walkingTranslations = tmp
	gmmu.toRemoveFromPTW = nil
}

func (gmmu *GMMU) walkPageTable(now sim.VTimeInSec) bool {
	madeProgress := false
	for i := 0; i < len(gmmu.walkingTranslations); i++ {
		if gmmu.walkingTranslations[i].cycleLeft > 0 {
			gmmu.walkingTranslations[i].cycleLeft--
			madeProgress = true
			continue
		}
		// req := gmmu.walkingTranslations[i].req

		// page, _ := gmmu.pageTable.Find(req.PID, req.VAddr)

		// if page.DeviceID == gmmu.deviceID {
		madeProgress = gmmu.finalizePageWalk(now, i) || madeProgress
		// } else {
		// 	madeProgress = gmmu.processRemoteMemReq(now, i) || madeProgress
		// }
	}

	tmp := gmmu.walkingTranslations[:0]
	for i := 0; i < len(gmmu.walkingTranslations); i++ {
		if !gmmu.toRemove(i) {
			tmp = append(tmp, gmmu.walkingTranslations[i])
		}
	}
	gmmu.walkingTranslations = tmp
	gmmu.toRemoveFromPTW = nil

	return madeProgress
}

func (gmmu *GMMU) processRemoteMemReq(now sim.VTimeInSec, walkingIndex int) bool {
	walking := gmmu.walkingTranslations[walkingIndex].req

	gmmu.remoteMemReqs[uint64(walking.VAddr)] = gmmu.walkingTranslations[walkingIndex]

	req := vm.TranslationReqBuilder{}.
		WithSendTime(now).
		WithSrc(gmmu.bottomPort).
		WithDst(gmmu.LowModule).
		WithPID(walking.PID).
		WithVAddr(walking.VAddr).
		WithDeviceID(walking.DeviceID).
		WithTaskID(walking.TaskID).
		WithOriginPort(walking.OriginPort).
		WithPrefetch(walking.IsPrefetch).
		Build()
	translationtrace.LinkRequest(req.ID, walking.ID)

	err := gmmu.bottomPort.Send(req)

	if err != nil {
		return false
	}

	gmmu.toRemoveFromPTW = append(gmmu.toRemoveFromPTW, walkingIndex)

	return true
}

func (gmmu *GMMU) finalizePageWalk(
	now sim.VTimeInSec,
	walkingIndex int,
) bool {
	req := gmmu.walkingTranslations[walkingIndex].req
	page, _ := gmmu.pageTable.Find(req.PID, req.VAddr)

	gmmu.walkingTranslations[walkingIndex].page = page

	return gmmu.doPageWalkHit(now, walkingIndex)
}

func (gmmu *GMMU) pageNeedMigrate(walking transaction) bool {
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

func (gmmu *GMMU) doPageWalkHit(
	now sim.VTimeInSec,
	walkingIndex int,
) bool {
	if !gmmu.topSender.CanSend(1) {
		return false
	}
	walking := gmmu.walkingTranslations[walkingIndex]

	rsp := vm.TranslationRspBuilder{}.
		WithSendTime(now).
		WithSrc(gmmu.topPort).
		WithDst(walking.req.Src).
		WithRspTo(walking.req.ID).
		WithPage(walking.page).
		WithTaskID(walking.req.TaskID).
		WithOriginPort(walking.req.OriginPort).
		WithPrefetch(walking.req.IsPrefetch).
		Build()

	gmmu.topSender.Send(rsp)

	gmmu.toRemoveFromPTW = append(gmmu.toRemoveFromPTW, walkingIndex)

	if !gmmu.sendToGMMU(now, walking) {
		return false
	}

	tracing.TraceReqComplete(walking.req, gmmu)

	return true
}

func (gmmu *GMMU) toRemove(index int) bool {
	for i := 0; i < len(gmmu.toRemoveFromPTW); i++ {
		remove := gmmu.toRemoveFromPTW[i]
		if remove == index {
			return true
		}
	}
	return false
}

func (gmmu *GMMU) fetchFromBottom(now sim.VTimeInSec) bool {
	if !gmmu.topSender.CanSend(1) {
		return false
	}

	rsp := gmmu.bottomPort.Retrieve(now)
	if rsp == nil {
		return false
	}

	tracing.TraceReqReceive(rsp, gmmu)

	switch rsp := rsp.(type) {
	case *vm.TranslationRsp:
		return gmmu.handleTranslationRsp(now, rsp)
	default:
		log.Panicf("gmmu canot handle request of type %s", reflect.TypeOf(rsp))
	}

	return true
}

func (gmmu *GMMU) handleTranslationRsp(now sim.VTimeInSec, rsponse *vm.TranslationRsp) bool {
	reqTransaction := gmmu.remoteMemReqs[uint64(rsponse.Page.VAddr)]

	rsp := vm.TranslationRspBuilder{}.
		WithSendTime(now).
		WithSrc(gmmu.topPort).
		WithDst(reqTransaction.req.Src).
		WithRspTo(reqTransaction.req.ID).
		WithPage(rsponse.Page).
		WithTaskID(reqTransaction.req.TaskID).
		WithOriginPort(reqTransaction.req.OriginPort).
		WithPrefetch(reqTransaction.req.IsPrefetch || rsponse.IsPrefetch).
		Build()

	gmmu.topSender.Send(rsp)

	delete(gmmu.remoteMemReqs, uint64(rsponse.Page.VAddr))
	return true
}

func (gmmu *GMMU) GetDeviceID() uint64 {
	return gmmu.deviceID
}

func (gmmu *GMMU) sendToGMMU(now sim.VTimeInSec, walking transaction) bool {
	madeProgress := false

	cacheLine := gmmu.getCacheLine(walking.req.VAddr)

	for i := 0; i < 8; i++ {
		vpn := cacheLine[i]
		newVAddr := vpn << gmmu.log2PageSize

		page, found := gmmu.pageTable.Find(walking.req.PID, newVAddr)
		if !found {
			page = vm.Page{
				PID:      walking.req.PID,
				VAddr:    newVAddr,
				Valid:    true,
				IsPinned: false,
			}
		}

		taskID := sim.GetIDGenerator().Generate()

		req := walking.req
		Rsp := vm.TranslationRspBuilder{}.
			WithSendTime(now).
			WithSrc(gmmu.topPort).
			WithDst(req.Src).
			WithRspTo(req.ID).
			WithPage(page).
			WithOriginPort(walking.req.OriginPort).
			WithTaskID(taskID).
			WithPrefetch(walking.req.IsPrefetch).
			Build()

		if !gmmu.topSender.CanSend(1) {
			return madeProgress
		}
		gmmu.topSender.Send(Rsp)
		madeProgress = true

	}
	return madeProgress
}

func (gmmu *GMMU) getCacheLine(vaddr uint64) []uint64 {
	currentVPN := vaddr >> gmmu.log2PageSize
	baseVPN := currentVPN &^ 0x7 // 8 entries per cache line
	return []uint64{baseVPN, baseVPN + 1, baseVPN + 2, baseVPN + 3, baseVPN + 4, baseVPN + 5, baseVPN + 6, baseVPN + 7}
}
