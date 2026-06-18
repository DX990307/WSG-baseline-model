package tlb_cf

import (
	"log"
	"reflect"

	"github.com/sarchlab/akita/v3/mem/vm"
	"github.com/sarchlab/akita/v3/mem/vm/tlb_cf/internal"
	"github.com/sarchlab/akita/v3/sim"
	"github.com/sarchlab/akita/v3/tracing"
)

type TLB struct {
	*sim.TickingComponent

	topPort     sim.Port
	bottomPort  sim.Port
	outsidePort sim.Port
	controlPort sim.Port
	cmpPort     sim.Port

	LowModule sim.Port
	CmpModule sim.Port

	numSets        int
	numWays        int
	pageSize       uint64
	numReqPerCycle int

	Sets []internal.Set

	mshr                mshr
	respondingMSHREntry *mshrEntry

	isPaused   bool
	filterFlag bool
	cf         *CuckooFilter
	deviceID   uint64
	pageTable  vm.PageTable
}

// Tick defines how TLB update states at each cycle
func (tlb *TLB) Tick(now sim.VTimeInSec) bool {
	madeProgress := false

	madeProgress = tlb.performCtrlReq(now) || madeProgress

	if !tlb.isPaused {
		for i := 0; i < tlb.numReqPerCycle; i++ {
			madeProgress = tlb.respondMSHREntry(now) || madeProgress
		}

		for i := 0; i < tlb.numReqPerCycle; i++ {
			madeProgress = tlb.lookupFromTopPort(now) || madeProgress
			madeProgress = tlb.lookupFromOutsidePort(now) || madeProgress
		}

		for i := 0; i < tlb.numReqPerCycle; i++ {
			madeProgress = tlb.parseBottom(now) || madeProgress
		}
	}

	// return madeProgress
	return madeProgress
}

// performCtrlReq handles control requests
func (tlb *TLB) performCtrlReq(now sim.VTimeInSec) bool {
	item := tlb.controlPort.Peek()
	if item == nil {
		return false
	}

	item = tlb.controlPort.Retrieve(now)

	switch req := item.(type) {
	case *FlushReq:
		return tlb.handleTLBFlush(now, req)
	case *RestartReq:
		return tlb.handleTLBRestart(now, req)
	default:
		log.Panicf("cannot process request %s", reflect.TypeOf(req))
	}

	return true
}

func (tlb *TLB) vAddrToSetID(vAddr uint64) (setID int) {
	return int(vAddr / tlb.pageSize % uint64(tlb.numSets))
}

func (tlb *TLB) handleTLBFlush(now sim.VTimeInSec, req *FlushReq) bool {
	rsp := FlushRspBuilder{}.
		WithSrc(tlb.controlPort).
		WithDst(req.Src).
		WithSendTime(now).
		Build()

	err := tlb.controlPort.Send(rsp)
	if err != nil {
		return false
	}

	for _, vAddr := range req.VAddr {
		setID := tlb.vAddrToSetID(vAddr)
		set := tlb.Sets[setID]
		wayID, page, found := set.Lookup(req.PID, vAddr)
		if !found {
			continue
		}

		page.Valid = false
		set.Update(wayID, page)
	}

	tlb.mshr.Reset()
	tlb.isPaused = true
	return true
}

func (tlb *TLB) handleTLBRestart(now sim.VTimeInSec, req *RestartReq) bool {
	rsp := RestartRspBuilder{}.
		WithSendTime(now).
		WithSrc(tlb.controlPort).
		WithDst(req.Src).
		Build()

	err := tlb.controlPort.Send(rsp)
	if err != nil {
		return false
	}

	tlb.isPaused = false

	for tlb.topPort.Retrieve(now) != nil {
		tlb.topPort.Retrieve(now)
	}

	for tlb.bottomPort.Retrieve(now) != nil {
		tlb.bottomPort.Retrieve(now)
	}

	return true
}

//End performCtrlReq

// respondMSHREntry
func (tlb *TLB) respondMSHREntry(now sim.VTimeInSec) bool {
	if tlb.respondingMSHREntry == nil {
		return false
	}

	mshrEntry := tlb.respondingMSHREntry
	page := mshrEntry.page
	req := mshrEntry.Requests[0]

	if req.LocalFlag {
		rspToTop := vm.TranslationRspBuilder{}.
			WithSendTime(now).
			WithSrc(tlb.topPort).
			WithDst(req.Request.Src).
			WithRspTo(req.Request.ID).
			WithPage(page).
			WithTaskID(req.Request.TaskID).
			Build()
		// fmt.Printf("%0.9f,%s,RspFromBottom,%s,%d\n", float64(now), tlb.bottomPort.Name(), rsp.TaskID, page.VAddr)
		// fmt.Printf("%0.9f,%s,RspToTop,%s,%d\n", float64(now), tlb.topPort.Name(), rspToTop.TaskID, page.VAddr)
		err := tlb.topPort.Send(rspToTop)
		if err != nil {
			return false
		}
	}

	if req.RemoteFlag {
		rspToOutside := vm.TranslationRspBuilder{}.
			WithSendTime(now).
			WithSrc(tlb.outsidePort).
			WithDst(req.Request.Src).
			WithRspTo(req.Request.ID).
			WithPage(page).
			WithTaskID(req.Request.TaskID).
			Build()

		// fmt.Printf("%0.9f,%s,RspToOutside,%s,%d\n", float64(now), tlb.outsidePort.Name(), rspToOutside.TaskID, page.VAddr)
		err := tlb.outsidePort.Send(rspToOutside)
		if err != nil {
			return false
		}
	}

	mshrEntry.Requests = mshrEntry.Requests[1:]
	if len(mshrEntry.Requests) == 0 {
		tlb.respondingMSHREntry = nil
	}

	tracing.TraceReqComplete(req.Request, tlb)
	return true
}

//End respondMSHREntry

// lookupFromTopPort
func (tlb *TLB) lookupFromTopPort(now sim.VTimeInSec) bool {
	msg := tlb.topPort.Peek()
	if msg == nil {
		return false
	}

	req := msg.(*vm.TranslationReq)

	if tlb.filterFlag {
		page, _ := tlb.pageTable.Find(req.PID, req.VAddr)
		if !tlb.cf.Lookup(req.VAddr) || !(page.DeviceID == tlb.deviceID) {
			return tlb.handleLocalCF(now, req, true, false)
		}
	}

	return tlb.processTranslation(now, req, true, false)
}

func (tlb *TLB) handleLocalCF(now sim.VTimeInSec, req *vm.TranslationReq,
	localFlag bool, remoteFlag bool) bool {
	mshrReq := &MshrRequest{
		Request:    req,
		LocalFlag:  localFlag,
		RemoteFlag: remoteFlag,
	}

	mshrEntry := tlb.mshr.Query(req.PID, req.VAddr)
	if mshrEntry == nil {
		return tlb.processTLBMSHRHit(now, mshrEntry, mshrReq)
	}

	return tlb.processTLBMSHRMiss(now, mshrEntry, mshrReq)
}

func (tlb *TLB) processTLBMSHRHit(
	now sim.VTimeInSec,
	mshrEntry *mshrEntry,
	mshrReq *MshrRequest,
) bool {
	mshrEntry.Requests = append(mshrEntry.Requests, mshrReq)

	if mshrReq.LocalFlag {
		tlb.topPort.Retrieve(now)
		// fmt.Printf("%0.9f,%s,FetchReqFromTop,%s\n", float64(now), tlb.topPort.Name(), mshrReq.Request.TaskID)
	} else if mshrReq.RemoteFlag {
		tlb.outsidePort.Retrieve(now)
		// fmt.Printf("%0.9f,%s,FetchReqFromOutside,%s\n", float64(now), tlb.outsidePort.Name(), mshrReq.Request.TaskID)
	}

	tracing.TraceReqReceive(mshrReq.Request, tlb)
	tracing.AddTaskStep(tracing.MsgIDAtReceiver(mshrReq.Request, tlb), tlb, "mshr-hit")

	return true
}

func (tlb *TLB) processTLBMSHRMiss(
	now sim.VTimeInSec,
	mshrEntry *mshrEntry,
	mshrReq *MshrRequest,
) bool {
	if tlb.mshr.IsFull() {
		return false
	}

	fetched := tlb.fetchLocalCmp(now, mshrReq)
	if fetched {
		if mshrReq.LocalFlag {
			tlb.topPort.Retrieve(now)
			// fmt.Printf("%0.9f,%s,FetchReqFromTop,%s,%d\n", float64(now), tlb.topPort.Name(), mshrReq.Request.TaskID, mshrReq.Request.VAddr)
		} else if mshrReq.RemoteFlag {
			tlb.outsidePort.Retrieve(now)
			// fmt.Printf("%0.9f,%s,FetchReqFromOutside,%s,%d\n", float64(now), tlb.outsidePort.Name(), mshrReq.Request.TaskID, mshrReq.Request.VAddr)
		}
		tracing.TraceReqReceive(mshrReq.Request, tlb)
		tracing.AddTaskStep(tracing.MsgIDAtReceiver(mshrReq.Request, tlb), tlb, "miss")
		return true
	}

	return false
}

func (tlb *TLB) fetchLocalCmp(now sim.VTimeInSec, mshrReq *MshrRequest) bool {
	fetchCMP := vm.TranslationReqBuilder{}.
		WithSendTime(now).
		WithSrc(tlb.bottomPort).
		WithDst(tlb.LowModule).
		WithPID(mshrReq.Request.PID).
		WithVAddr(mshrReq.Request.VAddr).
		WithDeviceID(mshrReq.Request.DeviceID).
		WithTaskID(mshrReq.Request.TaskID).
		WithOriginPort(tlb.outsidePort).
		Build()

	err := tlb.cmpPort.Send(fetchCMP)
	if err != nil {
		return false
	}
	// fmt.Printf("%0.9f,%s,SendReq,%s, %d\n", float64(now), tlb.bottomPort.Name(), fetchBottom.TaskID, mshrReq.Request.VAddr)

	mshrEntry := tlb.mshr.Add(mshrReq.Request.PID, mshrReq.Request.VAddr)
	mshrEntry.Requests = append(mshrEntry.Requests, mshrReq)
	mshrEntry.reqToBottom = fetchCMP

	tracing.TraceReqInitiate(fetchCMP, tlb,
		tracing.MsgIDAtReceiver(mshrReq.Request, tlb))

	return true
}

func (tlb *TLB) processTranslation(now sim.VTimeInSec, req *vm.TranslationReq,
	localFlag, remoteFlag bool) bool {

	mshrReq := &MshrRequest{
		Request:    req,
		LocalFlag:  localFlag,
		RemoteFlag: remoteFlag,
	}

	mshrEntry := tlb.mshr.Query(req.PID, req.VAddr)
	if mshrEntry != nil {
		return tlb.processTLBMSHRHit(now, mshrEntry, mshrReq)
	}

	setID := tlb.vAddrToSetID(req.VAddr)
	set := tlb.Sets[setID]
	wayID, page, found := set.Lookup(req.PID, req.VAddr)
	if found && page.Valid {
		return tlb.handleTranslationHit(now, mshrReq, setID, wayID, page)
	}

	return tlb.handleTranslationMiss(now, mshrReq)
}

func (tlb *TLB) handleTranslationHit(
	now sim.VTimeInSec,
	req *MshrRequest,
	setID, wayID int,
	page vm.Page,
) bool {

	if req.LocalFlag {
		ok := tlb.sendRspToTop(now, req.Request, page)
		// fmt.Printf(" sendRspToTop: %0.9f,%s,RspToTop,%s,%d\n", float64(now), tlb.topPort.Name(), req.Request.TaskID, page.VAddr)
		if !ok {
			return false
		}
		tlb.topPort.Retrieve(now)
	}

	if req.RemoteFlag {
		ok := tlb.sendRspToOutside(now, req.Request, page)
		// fmt.Printf(" sendRspToOutside: %0.9f,%s,RspToOutside,%s,%d\n", float64(now), tlb.outsidePort.Name(), req.Request.TaskID, page.VAddr)
		if !ok {
			return false
		}
		tlb.outsidePort.Retrieve(now)
	}

	tlb.visit(setID, wayID)

	tracing.TraceReqReceive(req.Request, tlb)
	tracing.AddTaskStep(tracing.MsgIDAtReceiver(req.Request, tlb), tlb, "hit")
	tracing.TraceReqComplete(req.Request, tlb)

	return true
}

func (tlb *TLB) visit(setID, wayID int) {
	set := tlb.Sets[setID]
	set.Visit(wayID)
}

func (tlb *TLB) handleTranslationMiss(
	now sim.VTimeInSec,
	// req *vm.TranslationReq,
	mshrReq *MshrRequest,
) bool {
	if tlb.mshr.IsFull() {
		return false
	}

	fetched := tlb.fetchBottom(now, mshrReq)
	if fetched {
		if mshrReq.LocalFlag {
			tlb.topPort.Retrieve(now)
			// fmt.Printf("%0.9f,%s,FetchReqFromTop,%s,%d\n", float64(now), tlb.topPort.Name(), mshrReq.Request.TaskID, mshrReq.Request.VAddr)
		} else if mshrReq.RemoteFlag {
			tlb.outsidePort.Retrieve(now)
			// fmt.Printf("%0.9f,%s,FetchReqFromOutside,%s,%d\n", float64(now), tlb.outsidePort.Name(), mshrReq.Request.TaskID, mshrReq.Request.VAddr)
		}
		tracing.TraceReqReceive(mshrReq.Request, tlb)
		tracing.AddTaskStep(tracing.MsgIDAtReceiver(mshrReq.Request, tlb), tlb, "miss")
		return true
	}

	return false
}

func (tlb *TLB) fetchBottom(now sim.VTimeInSec, mshrReq *MshrRequest /*req *vm.TranslationReq*/) bool {
	fetchBottom := vm.TranslationReqBuilder{}.
		WithSendTime(now).
		WithSrc(tlb.bottomPort).
		WithDst(tlb.LowModule).
		WithPID(mshrReq.Request.PID).
		WithVAddr(mshrReq.Request.VAddr).
		WithDeviceID(mshrReq.Request.DeviceID).
		WithTaskID(mshrReq.Request.TaskID).
		Build()
	err := tlb.bottomPort.Send(fetchBottom)
	if err != nil {
		return false
	}

	// fmt.Printf("%0.9f,%s,SendReq,%s, %d\n", float64(now), tlb.bottomPort.Name(), fetchBottom.TaskID, mshrReq.Request.VAddr)

	mshrEntry := tlb.mshr.Add(mshrReq.Request.PID, mshrReq.Request.VAddr)
	mshrEntry.Requests = append(mshrEntry.Requests, mshrReq)
	mshrEntry.reqToBottom = fetchBottom

	tracing.TraceReqInitiate(fetchBottom, tlb,
		tracing.MsgIDAtReceiver(mshrReq.Request, tlb))

	return true
}

func (tlb *TLB) sendRspToTop(
	now sim.VTimeInSec,
	req *vm.TranslationReq,
	page vm.Page,
) bool {
	rsp := vm.TranslationRspBuilder{}.
		WithSendTime(now).
		WithSrc(tlb.topPort).
		WithDst(req.Src).
		WithRspTo(req.ID).
		WithPage(page).
		Build()

	err := tlb.topPort.Send(rsp)
	if err != nil {
		return false
	}

	// fmt.Printf("%0.9f,%s,RspToTop,%s\n",
	// 	float64(now), tlb.topPort.Name(), rsp.TaskID)

	return true
}

func (tlb *TLB) sendRspToOutside(
	now sim.VTimeInSec,
	req *vm.TranslationReq,
	page vm.Page,
) bool {
	rsp := vm.TranslationRspBuilder{}.
		WithSendTime(now).
		WithSrc(tlb.outsidePort).
		WithDst(req.Src).
		WithRspTo(req.ID).
		WithPage(page).
		Build()

	err := tlb.outsidePort.Send(rsp)
	if err != nil {
		return false
	}

	// fmt.Printf("%0.9f,%s,RspToTop,%s\n",
	// 	float64(now), tlb.topPort.Name(), rsp.TaskID)

	return true
}

func (tlb *TLB) parseBottom(now sim.VTimeInSec) bool {
	if tlb.respondingMSHREntry != nil {
		return false
	}

	item := tlb.bottomPort.Peek()
	if item == nil {
		return false
	}

	switch item := item.(type) {
	case *vm.TranslationRsp:
		return tlb.processRsp(now, item)
	case *vm.RemoveMSHRReq:
		return tlb.processRemoveMSHRReq(now, item)
	default:
		panic("unexpected message type")
	}
}

func (tlb *TLB) processRsp(now sim.VTimeInSec, rsp *vm.TranslationRsp) bool {
	// rsp := item.(*vm.TranslationRsp)
	page := rsp.Page

	mshrEntryPresent := tlb.mshr.IsEntryPresent(rsp.Page.PID, rsp.Page.VAddr)
	if !mshrEntryPresent {
		tlb.bottomPort.Retrieve(now)
		return true
	}

	setID := tlb.vAddrToSetID(page.VAddr)
	set := tlb.Sets[setID]
	wayID, ok := tlb.Sets[setID].Evict()

	if !ok {
		panic("failed to evict")
	}
	set.Update(wayID, page)
	set.Visit(wayID)

	mshrEntry := tlb.mshr.GetEntry(rsp.Page.PID, rsp.Page.VAddr)
	tlb.respondingMSHREntry = mshrEntry
	mshrEntry.page = page

	tlb.mshr.Remove(rsp.Page.PID, rsp.Page.VAddr)
	tlb.bottomPort.Retrieve(now)
	tracing.TraceReqFinalize(mshrEntry.reqToBottom, tlb)

	if tlb.filterFlag {
		tlb.cf.Insert(page.VAddr)
	}

	return true
}

//End lookupFromTopPort

// lookupFromOutsidePort
func (tlb *TLB) lookupFromOutsidePort(now sim.VTimeInSec) bool {
	msg := tlb.outsidePort.Peek()
	if msg == nil {
		return false
	}

	switch msg := msg.(type) {
	case *vm.TranslationReq:
		return tlb.processRemoteTranslation(now, msg)
		// return tlb.processTranslation(now, msg, false, true)
	case *vm.TranslationRsp:
		return tlb.processRsp(now, msg)
	default:
		panic("unexpected message type")
	}
	// fmt.Printf("%0.9f,%s,FetchReqFromOutside,%s, %d\n",
	//float64(now), tlb.outsidePort.Name(), req.TaskID, req.VAddr)
}

func (tlb *TLB) processRemoteTranslation(
	now sim.VTimeInSec, req *vm.TranslationReq) bool {
	madeProgress := false

	msg := tlb.topPort.Peek()
	if msg == nil {
		return false
	}

	if tlb.filterFlag {
		page, _ := tlb.pageTable.Find(req.PID, req.VAddr)
		if !tlb.cf.Lookup(req.VAddr) || !(page.DeviceID == tlb.deviceID) {
			return tlb.handleRemoteCFMiss(now, req)
		}
	}

	madeProgress = tlb.processRemoteTranslationReq(now, req, false, true) || madeProgress

	return madeProgress
}

func (tlb *TLB) handleRemoteCFMiss(now sim.VTimeInSec, req *vm.TranslationReq) bool {
	newReq := vm.TranslationReqBuilder{}.
		WithSendTime(now).
		WithSrc(tlb.cmpPort).
		WithDst(tlb.CmpModule).
		WithPID(req.PID).
		WithVAddr(req.VAddr).
		WithDeviceID(req.DeviceID).
		WithTaskID(req.TaskID).
		WithOriginPort(req.OriginPort).
		Build()

	err := tlb.cmpPort.Send(newReq)
	if err != nil {
		return false
	}

	tracing.TraceReqInitiate(req, tlb, tracing.MsgIDAtReceiver(req, tlb))
	return true
}

func (tlb *TLB) processRemoteTranslationReq(now sim.VTimeInSec, req *vm.TranslationReq,
	localFlag, remoteFlag bool) bool {
	mshrReq := &MshrRequest{
		Request:    req,
		LocalFlag:  localFlag,
		RemoteFlag: remoteFlag,
	}

	mshrEntry := tlb.mshr.Query(req.PID, req.VAddr)
	if mshrEntry != nil {
		return tlb.processTLBMSHRHit(now, mshrEntry, mshrReq)
	}

	return tlb.processTLBMSHRMiss(now, mshrEntry, mshrReq)
}

func (tlb *TLB) processRemoveMSHRReq(
	now sim.VTimeInSec,
	req *vm.RemoveMSHRReq,
) bool {
	mshrEntry := tlb.mshr.GetEntry(req.PID, req.VAddr)
	if mshrEntry == nil {
		tlb.bottomPort.Retrieve(now)
		return true
	}

	tlb.mshr.Remove(req.PID, req.VAddr)
	tlb.bottomPort.Retrieve(now)

	return true
}

// Others
func (tlb *TLB) reset() {
	tlb.Sets = make([]internal.Set, tlb.numSets)
	for i := 0; i < tlb.numSets; i++ {
		set := internal.NewSet(tlb.numWays)
		tlb.Sets[i] = set
	}
}

//End Others
