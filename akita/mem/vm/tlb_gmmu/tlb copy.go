package tlb_gmmu

// import (
// 	"fmt"
// 	"log"
// 	"reflect"

// 	"github.com/sarchlab/akita/v3/mem/mem"
// 	"github.com/sarchlab/akita/v3/mem/vm"
// 	"github.com/sarchlab/akita/v3/mem/vm/tlb_gmmu/internal"
// 	"github.com/sarchlab/akita/v3/sim"
// 	"github.com/sarchlab/akita/v3/tracing"
// )

// var nilPage = vm.Page{}

// type TimeConsumption struct {
// 	StartTime sim.VTimeInSec
// 	EndTime   sim.VTimeInSec
// }

// // A TLB is a cache that maintains some page information.
// type GMMUTLB struct {
// 	*sim.TickingComponent

// 	topPort     sim.Port
// 	bottomPort  sim.Port
// 	OutsidePort sim.Port
// 	controlPort sim.Port
// 	IOMMUPort   sim.Port

// 	LowModule sim.Port

// 	numSets        int
// 	numWays        int
// 	pageSize       uint64
// 	numReqPerCycle int

// 	Sets []internal.Set

// 	mshr                mshr
// 	respondingMSHREntry []*mshrEntry
// 	setSize             int

// 	outsideSet   []vm.Page
// 	isPaused     bool
// 	DeviceID     uint64
// 	pageTable    vm.PageTable
// 	PageFider    mem.PageFinder
// 	cuckooFilter internal.CuckooFilter
// 	// gmmuCacheTable map[uint64]sim.Port
// 	gmmuCacheTable *mem.MultiPageFinder

// 	TimeConsumption  map[uint64]TimeConsumption
// 	cocucompensation int
// }

// // Reset sets all the entries int he TLB to be invalid
// func (tlb *GMMUTLB) reset() {
// 	tlb.Sets = make([]internal.Set, tlb.numSets)
// 	for i := 0; i < tlb.numSets; i++ {
// 		set := internal.NewSet(tlb.numWays)
// 		tlb.Sets[i] = set
// 	}
// }

// // Tick defines how TLB update states at each cycle
// func (tlb *GMMUTLB) Tick(now sim.VTimeInSec) bool {
// 	madeProgress := false

// 	madeProgress = tlb.performCtrlReq(now) || madeProgress

// 	if !tlb.isPaused {
// 		for i := 0; i < tlb.numReqPerCycle; i++ {
// 			madeProgress = tlb.respondMSHREntry(now) || madeProgress
// 		}

// 		tlb.cocucompensation = 0
// 		for i := 0; i < tlb.numReqPerCycle; i++ {
// 			madeProgress = tlb.lookupFromTopPort(now) || madeProgress
// 			madeProgress = tlb.lookupFromOutsidePort(now) || madeProgress
// 		}

// 		for i := 0; i < tlb.numReqPerCycle; i++ {
// 			madeProgress = tlb.parseBottom(now) || madeProgress
// 		}
// 	}

// 	return madeProgress
// }

// func (tlb *GMMUTLB) respondMSHREntry(now sim.VTimeInSec) bool {
// 	if len(tlb.respondingMSHREntry) == 0 {
// 		return false
// 	}

// 	mshrEntry := tlb.respondingMSHREntry[0]
// 	page := mshrEntry.page
// 	req := mshrEntry.Requests[0]

// 	if req.LocalFlag {
// 		rspToTop := vm.TranslationRspBuilder{}.
// 			WithSendTime(now).
// 			WithSrc(tlb.topPort).
// 			WithDst(req.Request.Src).
// 			WithRspTo(req.Request.ID).
// 			WithPage(page).
// 			WithTaskID(req.Request.TaskID).
// 			WithOriginPort(req.Request.OriginPort).
// 			Build()

// 		err := tlb.topPort.Send(rspToTop)

// 		if err != nil {
// 			return false
// 		}
// 	}

// 	if req.RemoteFlag {
// 		rspToOutside := vm.TranslationRspBuilder{}.
// 			WithSendTime(now).
// 			WithSrc(tlb.OutsidePort).
// 			WithDst(req.Request.OriginPort).
// 			WithRspTo(req.Request.ID).
// 			WithPage(page).
// 			WithTaskID(req.Request.TaskID).
// 			WithOriginPort(req.Request.OriginPort).
// 			Build()

// 		err := tlb.OutsidePort.Send(rspToOutside)

// 		if err != nil {
// 			return false
// 		}
// 	}

// 	mshrEntry.Requests = mshrEntry.Requests[1:]
// 	if len(mshrEntry.Requests) == 0 {
// 		tlb.respondingMSHREntry = tlb.respondingMSHREntry[1:]
// 	}

// 	tracing.TraceReqComplete(req.Request, tlb)
// 	return true
// }

// func (tlb *GMMUTLB) lookupFromTopPort(now sim.VTimeInSec) bool {
// 	msg := tlb.topPort.Peek()
// 	if msg == nil {
// 		return false
// 	}

// 	req := msg.(*vm.TranslationReq)
// 	return tlb.processTranslation(now, req, true, false)
// }

// func (tlb *GMMUTLB) lookupFromOutsidePort(now sim.VTimeInSec) bool {
// 	msg := tlb.OutsidePort.Peek()
// 	if msg == nil {
// 		return false
// 	}

// 	switch msg := msg.(type) {
// 	case *vm.TranslationReq:
// 		return tlb.processTranslation(now, msg, false, true)
// 	case *vm.TranslationRsp:
// 		return tlb.processRsp(now, msg, false)
// 	default:
// 		panic("unexpected message type")
// 	}
// }

// func (tlb *GMMUTLB) handleTranslationHit(
// 	now sim.VTimeInSec,
// 	req *MshrRequest,
// 	setID, wayID int,
// 	page vm.Page,
// ) bool {
// 	if req.LocalFlag {
// 		ok := tlb.sendRspToTop(now, req.Request, page)
// 		if !ok {
// 			return false
// 		}
// 		tlb.topPort.Retrieve(now)
// 	} else if req.RemoteFlag {
// 		ok := tlb.sendRspToOutside(now, req.Request, page)
// 		if !ok {
// 			return false
// 		}
// 		tlb.OutsidePort.Retrieve(now)
// 	}

// 	tlb.visit(setID, wayID)

// 	tracing.TraceReqReceive(req.Request, tlb)
// 	tracing.AddTaskStep(tracing.MsgIDAtReceiver(req.Request, tlb), tlb, "hit")
// 	tracing.TraceReqComplete(req.Request, tlb)
// 	tracing.StartTask(req.Request.TaskID,
// 		tracing.MsgIDAtReceiver(req.Request, tlb),
// 		tlb, "EvictTest", "*vm.TranslationReq", req.Request)

// 	return true
// }

// func (tlb *GMMUTLB) handleTranslationMiss(
// 	now sim.VTimeInSec,
// 	mshrReq *MshrRequest,
// 	doNotAddMshr bool,
// ) bool {
// 	if tlb.mshr.IsFull() {
// 		return false
// 	}

// 	fetched := tlb.fetchBottom(now, mshrReq, doNotAddMshr)
// 	if fetched {
// 		tracing.TraceReqReceive(mshrReq.Request, tlb)
// 		tracing.AddTaskStep(tracing.MsgIDAtReceiver(mshrReq.Request, tlb), tlb, "miss")
// 		tracing.StartTask(mshrReq.Request.TaskID,
// 			tracing.MsgIDAtReceiver(mshrReq.Request, tlb),
// 			tlb, "EvictTest", "*vm.TranslationReq", mshrReq.Request)
// 		return true
// 	}

// 	return false
// }

// func (tlb *GMMUTLB) vAddrToSetID(vAddr uint64) (setID int) {
// 	return int(vAddr / tlb.pageSize % uint64(tlb.numSets))
// }

// func (tlb *GMMUTLB) sendRspToTop(
// 	now sim.VTimeInSec,
// 	req *vm.TranslationReq,
// 	page vm.Page,
// ) bool {
// 	rsp := vm.TranslationRspBuilder{}.
// 		WithSendTime(now).
// 		WithSrc(tlb.topPort).
// 		WithDst(req.Src).
// 		WithRspTo(req.ID).
// 		WithPage(page).
// 		WithTaskID(req.TaskID).
// 		WithOriginPort(req.OriginPort).
// 		Build()

// 	err := tlb.topPort.Send(rsp)
// 	if err != nil {
// 		return false
// 	}

// 	return true
// }

// func (tlb *GMMUTLB) sendRspToOutside(
// 	now sim.VTimeInSec,
// 	req *vm.TranslationReq,
// 	page vm.Page,
// ) bool {
// 	rsp := vm.TranslationRspBuilder{}.
// 		WithSendTime(now).
// 		WithSrc(tlb.OutsidePort).
// 		WithDst(req.Src).
// 		WithRspTo(req.ID).
// 		WithPage(page).
// 		WithOriginPort(req.OriginPort).
// 		Build()

// 	err := tlb.OutsidePort.Send(rsp)
// 	if err != nil {
// 		return false
// 	}

// 	return true
// }

// func (tlb *GMMUTLB) processTLBMSHRHit(
// 	now sim.VTimeInSec,
// 	mshrEntry *mshrEntry,
// 	mshrReq *MshrRequest,
// ) bool {
// 	mshrEntry.Requests = append(mshrEntry.Requests, mshrReq)

// 	if mshrReq.LocalFlag {
// 		tlb.topPort.Retrieve(now)
// 	} else if mshrReq.RemoteFlag {
// 		tlb.OutsidePort.Retrieve(now)
// 	}

// 	tracing.TraceReqReceive(mshrReq.Request, tlb)
// 	tracing.AddTaskStep(tracing.MsgIDAtReceiver(mshrReq.Request, tlb), tlb, "mshr-hit")
// 	tracing.StartTask(mshrReq.Request.TaskID,
// 		tracing.MsgIDAtReceiver(mshrReq.Request, tlb),
// 		tlb, "EvictTest", "*vm.TranslationReq", mshrReq.Request)

// 	return true
// }

// func (tlb *GMMUTLB) fetchBottom(now sim.VTimeInSec, mshrReq *MshrRequest, doNotAddMshr bool /*req *vm.TranslationReq*/) bool {
// 	page, found := tlb.pageTable.Find(mshrReq.Request.PID, mshrReq.Request.VAddr)
// 	if !found {
// 		panic("page not found")
// 	}

// 	if page.DeviceID != tlb.DeviceID {
// 		req, ok := tlb.sendToIOMMU(mshrReq.Request, now, mshrReq.LocalFlag, mshrReq.RemoteFlag)
// 		if !ok {
// 			return false
// 		}

// 		if mshrReq.LocalFlag && !doNotAddMshr {
// 			isPresent := tlb.mshr.IsEntryPresent(mshrReq.Request.PID, mshrReq.Request.VAddr)
// 			if !isPresent {
// 				mshrEntry := tlb.mshr.Add(mshrReq.Request.PID, mshrReq.Request.VAddr)
// 				mshrEntry.Requests = append(mshrEntry.Requests, mshrReq)
// 				mshrEntry.reqToBottom = req
// 			} else {
// 				mshrEntry := tlb.mshr.GetEntry(mshrReq.Request.PID, mshrReq.Request.VAddr)
// 				mshrEntry.Requests = append(mshrEntry.Requests, mshrReq)
// 				mshrEntry.reqToBottom = req
// 			}
// 		}

// 		tracing.TraceReqInitiate(req, tlb,
// 			tracing.MsgIDAtReceiver(mshrReq.Request, tlb))

// 		return true
// 	}

// 	fetchBottom := vm.TranslationReqBuilder{}.
// 		WithSendTime(now).
// 		WithSrc(tlb.bottomPort).
// 		WithDst(tlb.LowModule).
// 		WithPID(mshrReq.Request.PID).
// 		WithVAddr(mshrReq.Request.VAddr).
// 		WithDeviceID(mshrReq.Request.DeviceID).
// 		WithTaskID(mshrReq.Request.TaskID).
// 		WithOriginPort(tlb.bottomPort).
// 		Build()

// 	err := tlb.bottomPort.Send(fetchBottom)
// 	if err != nil {
// 		return false
// 	}

// 	isPresent := tlb.mshr.IsEntryPresent(mshrReq.Request.PID, mshrReq.Request.VAddr)
// 	if !isPresent {
// 		mshrEntry := tlb.mshr.Add(mshrReq.Request.PID, mshrReq.Request.VAddr)
// 		mshrEntry.Requests = append(mshrEntry.Requests, mshrReq)
// 		mshrEntry.reqToBottom = fetchBottom
// 	} else {
// 		mshrEntry := tlb.mshr.GetEntry(mshrReq.Request.PID, mshrReq.Request.VAddr)
// 		mshrEntry.Requests = append(mshrEntry.Requests, mshrReq)
// 		mshrEntry.reqToBottom = fetchBottom
// 	}

// 	if mshrReq.LocalFlag {
// 		tlb.topPort.Retrieve(now)
// 	} else if mshrReq.RemoteFlag {
// 		tlb.OutsidePort.Retrieve(now)
// 	}

// 	tracing.TraceReqInitiate(fetchBottom, tlb,
// 		tracing.MsgIDAtReceiver(mshrReq.Request, tlb))

// 	return true
// }

// func (tlb *GMMUTLB) parseBottom(now sim.VTimeInSec) bool {
// 	if len(tlb.respondingMSHREntry) != 0 {
// 		return false
// 	}

// 	item := tlb.bottomPort.Peek()
// 	if item == nil {
// 		return false
// 	}

// 	switch item := item.(type) {
// 	case *vm.TranslationRsp:
// 		return tlb.processRsp(now, item, true)
// 	default:
// 		panic("unexpected message type")
// 	}
// }

// func (tlb *GMMUTLB) performCtrlReq(now sim.VTimeInSec) bool {
// 	item := tlb.controlPort.Peek()
// 	if item == nil {
// 		return false
// 	}

// 	item = tlb.controlPort.Retrieve(now)

// 	switch req := item.(type) {
// 	case *FlushReq:
// 		return tlb.handleTLBFlush(now, req)
// 	case *RestartReq:
// 		return tlb.handleTLBRestart(now, req)
// 	default:
// 		log.Panicf("cannot process request %s", reflect.TypeOf(req))
// 	}

// 	return true
// }

// func (tlb *GMMUTLB) visit(setID, wayID int) {
// 	set := tlb.Sets[setID]
// 	set.Visit(wayID)
// }

// func (tlb *GMMUTLB) handleTLBFlush(now sim.VTimeInSec, req *FlushReq) bool {
// 	rsp := FlushRspBuilder{}.
// 		WithSrc(tlb.controlPort).
// 		WithDst(req.Src).
// 		WithSendTime(now).
// 		Build()

// 	err := tlb.controlPort.Send(rsp)
// 	if err != nil {
// 		return false
// 	}

// 	for _, vAddr := range req.VAddr {
// 		setID := tlb.vAddrToSetID(vAddr)
// 		set := tlb.Sets[setID]
// 		wayID, page, found := set.Lookup(req.PID, vAddr)
// 		if !found {
// 			continue
// 		}

// 		page.Valid = false
// 		set.Update(wayID, page)
// 	}

// 	tlb.mshr.Reset()
// 	tlb.isPaused = true
// 	return true
// }

// func (tlb *GMMUTLB) handleTLBRestart(now sim.VTimeInSec, req *RestartReq) bool {
// 	rsp := RestartRspBuilder{}.
// 		WithSendTime(now).
// 		WithSrc(tlb.controlPort).
// 		WithDst(req.Src).
// 		Build()

// 	err := tlb.controlPort.Send(rsp)
// 	if err != nil {
// 		return false
// 	}

// 	tlb.isPaused = false

// 	for tlb.topPort.Retrieve(now) != nil {
// 		tlb.topPort.Retrieve(now)
// 	}

// 	for tlb.bottomPort.Retrieve(now) != nil {
// 		tlb.bottomPort.Retrieve(now)
// 	}

// 	return true
// }

// func (tlb *GMMUTLB) processTranslation(now sim.VTimeInSec, req *vm.TranslationReq,
// 	localFlag, remoteFlag bool) bool {

// 	mshrReq := &MshrRequest{
// 		Request:    req,
// 		LocalFlag:  localFlag,
// 		RemoteFlag: remoteFlag,
// 	}

// 	doNotAddToMSHR := false

// 	if mshrReq.RemoteFlag {
// 		if mshrReq.Request.OriginPort == tlb.OutsidePort || mshrReq.Request.OriginPort == tlb.bottomPort {
// 			mshrReq.LocalFlag = true
// 			mshrReq.RemoteFlag = false
// 			doNotAddToMSHR = true
// 		}
// 	}

// 	mshrEntry := tlb.mshr.Query(req.PID, req.VAddr)

// 	if mshrEntry != nil {
// 		if mshrReq.RemoteFlag {
// 			return tlb.handleTranslationMiss(now, mshrReq, doNotAddToMSHR)
// 		}
// 		return tlb.processTLBMSHRHit(now, mshrEntry, mshrReq)
// 	}

// 	setID := tlb.vAddrToSetID(req.VAddr)
// 	set := tlb.Sets[setID]
// 	wayID, page, found := set.Lookup(req.PID, req.VAddr)
// 	if found && page.Valid {
// 		return tlb.handleTranslationHit(now, mshrReq, setID, wayID, page)
// 	}

// 	return tlb.handleTranslationMiss(now, mshrReq, doNotAddToMSHR)
// }

// func (tlb *GMMUTLB) processRsp(now sim.VTimeInSec, rsp *vm.TranslationRsp, bottom bool) bool {
// 	page := rsp.Page
// 	mshrEntryPresent := tlb.mshr.IsEntryPresent(rsp.Page.PID, rsp.Page.VAddr)
// 	if !mshrEntryPresent {
// 		if bottom {
// 			tlb.bottomPort.Retrieve(now)
// 		} else {
// 			tlb.OutsidePort.Retrieve(now)
// 		}

// 		return true
// 	}

// 	setID := tlb.vAddrToSetID(page.VAddr)
// 	set := tlb.Sets[setID]
// 	wayID, ok, _ := tlb.Sets[setID].Evict()

// 	if !ok {
// 		panic("failed to evict")
// 	}

// 	set.Update(wayID, page)
// 	set.Visit(wayID)

// 	mshrEntry := tlb.mshr.GetEntry(rsp.Page.PID, rsp.Page.VAddr)
// 	tlb.respondingMSHREntry = append(tlb.respondingMSHREntry, mshrEntry)
// 	mshrEntry.page = page
// 	tlb.cuckooFilter.Insert(page.VAddr)

// 	tlb.mshr.Remove(rsp.Page.PID, rsp.Page.VAddr)
// 	if bottom {
// 		tlb.bottomPort.Retrieve(now)
// 	} else {
// 		tlb.OutsidePort.Retrieve(now)
// 	}
// 	tracing.TraceReqFinalize(mshrEntry.reqToBottom, tlb)
// 	return true
// }

// func (tlb *GMMUTLB) sendToIOMMU(
// 	req *vm.TranslationReq,
// 	now sim.VTimeInSec,
// 	localFlag, remoteFlag bool,
// ) (*vm.TranslationReq, bool) {
// 	if tlb.IOMMUPort == nil {
// 		log.Panicf("GMMUTLB %s does not have an IOMMU port", tlb.Name())
// 	}

// 	newReq := vm.TranslationReqBuilder{}.
// 		WithSendTime(now).
// 		WithSrc(tlb.OutsidePort).
// 		WithDst(tlb.IOMMUPort).
// 		WithPID(req.PID).
// 		WithVAddr(req.VAddr).
// 		WithDeviceID(req.DeviceID).
// 		WithTaskID(req.TaskID).
// 		WithOriginPort(req.OriginPort).
// 		Build()

// 	fmt.Printf("%d,%s,%d\n", uint64(now*1e9), req.Src.Name(), req.VAddr>>12)

// 	err := tlb.IOMMUPort.Send(newReq)
// 	if err != nil {
// 		return nil, false
// 	}

// 	return newReq, true
// }
