package mmuTLB

import (
	"fmt"
	"log"
	"reflect"
	"sort"

	"github.com/sarchlab/akita/v3/mem/mem"
	"github.com/sarchlab/akita/v3/mem/vm"
	"github.com/sarchlab/akita/v3/mem/vm/mmuTLB/internal"
	"github.com/sarchlab/akita/v3/mem/vm/translationtrace"
	"github.com/sarchlab/akita/v3/sim"
	"github.com/sarchlab/akita/v3/tracing"
)

var log2PageSize uint64 = 12

// A TLB is a cache that maintains some page information.
type TLB struct {
	*sim.TickingComponent

	topPort     sim.Port
	bottomPort  sim.Port
	controlPort sim.Port

	LowModule sim.Port

	numSets        int
	numWays        int
	pageSize       uint64
	numReqPerCycle int
	TopPortBuffer  []sim.Msg

	Sets []internal.Set

	mshr                mshr
	respondingMSHREntry []*mshrEntry
	gmmuCacheTable      *mem.MultiPageFinder

	isPrediction bool
	BloomFilter  *BloomFilter
	log2PageSize uint64

	InnerLoop  map[uint64]uint64
	MiddleLoop map[uint64]uint64
	isPaused   bool
	pageTable  vm.PageTable

	reqBuffer                  []*vm.TranslationReq
	incomingReqCount           int
	downstreamReqCount         int
	vpnMSHRBaseline            bool
	demandPTEOnly              bool
	setAsLineTLBEnabled        bool
	lookupLatencyCycles        int
	setLookupJobs              int
	setLookupRequestedBits     int
	setLookupHitBits           int
	setLookupMissBits          int
	setLookupSavedJobs         int
	setFills                   int
	setConflictEvictions       int
	prefetcher                 *translationPrefetcher
	inflightPrefetches         map[prefetchTargetKey]struct{}
	prefetchReqStates          map[string]*prefetchReqState
	lookupReadyTimes           map[string]sim.VTimeInSec
	knownGPMIDs                []uint64
	knownGPMsReady             bool
	completedPrefetches        map[prefetchTargetKey]*completedPrefetchState
	inflightPrefetchStateByKey map[prefetchTargetKey]*prefetchReqState
	prefetchOutcomeByBlock     map[uint64]*prefetchOutcomeCounts
	prefetchFeedbackByTarget   map[prefetchFeedbackKey]*prefetchFeedbackCounters
	prefetchCompletedCount     int
	prefetchUsefulHitCount     int
	prefetchLateDemandCount    int
	prefetchLostBeforeUseCount int

	// translationRequests     map[uint64]map[vm.PID]*vm.TranslationReq
}

type prefetchTargetKey struct {
	pid       vm.PID
	targetGPM uint64
	baseVAddr uint64
}

type prefetchReqState struct {
	key                prefetchTargetKey
	req                *vm.TranslationReq
	remainingPages     int
	pageBlock          uint64
	targetPTCL         uint64
	triggerGPM         uint64
	issueTime          sim.VTimeInSec
	lateDemandObserved bool
}

type completedPrefetchState struct {
	key          prefetchTargetKey
	pageBlock    uint64
	targetPTCL   uint64
	triggerGPM   uint64
	issueTime    sim.VTimeInSec
	completeTime sim.VTimeInSec
}

type prefetchOutcomeCounts struct {
	Enqueued      int
	Completed     int
	Useful        int
	Late          int
	LostBeforeUse int
}

type prefetchFeedbackKey struct {
	pageBlock uint64
	targetGPM uint64
}

type prefetchFeedbackCounters struct {
	disabled      bool
	usefulHit     int
	late          int
	lostBeforeUse int
}

const prefetchFeedbackDisableThreshold = 8

// Reset sets all the entries int he TLB to be invalid
func (tlb *TLB) reset() {
	tlb.Sets = make([]internal.Set, tlb.numSets)
	for i := 0; i < tlb.numSets; i++ {
		set := internal.NewSet(tlb.numWays)
		tlb.Sets[i] = set
	}

	clear(tlb.inflightPrefetches)
	clear(tlb.prefetchReqStates)
	clear(tlb.completedPrefetches)
	clear(tlb.inflightPrefetchStateByKey)
	clear(tlb.prefetchOutcomeByBlock)
	clear(tlb.lookupReadyTimes)
	clear(tlb.prefetchFeedbackByTarget)
	tlb.prefetchCompletedCount = 0
	tlb.prefetchUsefulHitCount = 0
	tlb.prefetchLateDemandCount = 0
	tlb.prefetchLostBeforeUseCount = 0
	tlb.setLookupJobs = 0
	tlb.setLookupRequestedBits = 0
	tlb.setLookupHitBits = 0
	tlb.setLookupMissBits = 0
	tlb.setLookupSavedJobs = 0
	tlb.setFills = 0
	tlb.setConflictEvictions = 0
}

// Tick defines how TLB update states at each cycle
func (tlb *TLB) Tick(now sim.VTimeInSec) bool {
	madeProgress := false

	if !tlb.isPaused {
		for i := 0; i < tlb.numReqPerCycle; i++ {
			madeProgress = tlb.respondMSHREntry(now) || madeProgress
		}

		for i := 0; i < tlb.numReqPerCycle; i++ {
			madeProgress = tlb.pushReqBuffer(now) || madeProgress
		}

		for i := 0; i < tlb.numReqPerCycle; i++ {
			madeProgress = tlb.lookup(now) || madeProgress
		}

		for i := 0; i < tlb.numReqPerCycle; i++ {
			madeProgress = tlb.parseBottom(now) || madeProgress
		}
	}

	if tlb.mshr != nil {
		entries, capacity := tlb.mshr.Occupancy()
		translationtrace.ObserveIOMMUTLB(
			now,
			tlb.Name(),
			entries,
			capacity,
			entries >= capacity,
		)
	}

	return madeProgress
}
func (tlb *TLB) pushReqBuffer(now sim.VTimeInSec) bool {
	msg := tlb.topPort.Peek()
	if msg == nil {
		return false
	}

	switch msg := msg.(type) {
	case *vm.TranslationReq:
		req := msg
		req.BitMap = tlb.effectiveBitmap(req)
		req.BitMap = tlb.filterMappedBitmap(req.PID, req.VAddr, req.BitMap)

		tlb.reqBuffer = append(tlb.reqBuffer, req)
		tlb.setLookupReadyTime(now, req)
		tlb.incomingReqCount++
		translationtrace.RecordIOMMUIncoming(now)
		tlb.maybeEnqueuePrefetches(now, req)
		tlb.topPort.Retrieve(now)

		tracing.TraceReqReceive(req, tlb)
		tracing.AddTaskStep(tracing.MsgIDAtReceiver(req, tlb), tlb, "buffered")
		return true
	case *vm.PrefetchFeedbackMsg:
		tlb.handlePrefetchFeedback(msg)
		tlb.topPort.Retrieve(now)
		return true
	default:
		panic(fmt.Sprintf("cannot process top-port message %s", reflect.TypeOf(msg)))
	}
}

func (tlb *TLB) respondMSHREntry(now sim.VTimeInSec) bool {
	if len(tlb.respondingMSHREntry) == 0 {
		return false
	}

	mshrEntry := tlb.respondingMSHREntry[0]
	req := mshrEntry.Requests[0]
	bitmap := req.BitMap

	pages := mshrEntry.Pages

	for i := 0; i < 8; i++ {
		page := pages[i]

		if !bitmap[i] {
			continue
		}

		if req.Src == nil {
			panic(fmt.Sprintf(
				"mmutlb responding request has nil Src pid=%d baseVAddr=%#x device=%d prefetch=%t task=%s",
				req.PID, tlb.getBaseVaddr(req.VAddr), req.DeviceID, req.IsPrefetch, req.TaskID,
			))
		}

		rspToTop := vm.TranslationRspBuilder{}.
			WithSendTime(now).
			WithSrc(tlb.topPort).
			WithDst(req.Src).
			WithRspTo(req.ID).
			WithPage(page).
			WithTaskID(req.TaskID).
			WithOriginPort(req.OriginPort).
			Build()

		err := tlb.topPort.Send(rspToTop)
		if err != nil {
			// fmt.Printf("Failed to send response to top for page %d, error: %s\n", page.VAddr, err)
			return false
		}

		bitmap[i] = false
		req.BitMap = bitmap
		break
	}

	for i := 0; i < 8; i++ {
		if bitmap[i] {
			return true
		}
	}

	mshrEntry.Requests = mshrEntry.Requests[1:]

	if len(mshrEntry.Requests) == 0 {
		tlb.respondingMSHREntry = tlb.respondingMSHREntry[1:]

	}

	tracing.TraceReqComplete(req, tlb)
	return true
}

func (tlb *TLB) lookup(now sim.VTimeInSec) bool {
	// msg := tlb.topPort.Peek()
	if len(tlb.reqBuffer) == 0 {
		return false
	}

	req := tlb.reqBuffer[0]
	// tlb.reqBuffer = tlb.reqBuffer[1:]

	if req == nil {
		return false
	}

	if !tlb.isLookupReady(now, req) {
		return false
	}

	if tlb.handleTranslationHits(now, req) {
		return true
	}

	if !req.IsPrefetch {
		tlb.observePrefetchDemandMiss(req)
	}

	mshrEntry := tlb.mshr.GetEntry(req.PID, req.VAddr)
	if mshrEntry != nil {
		return tlb.processTLBMSHRHit(now, mshrEntry, req)
	}

	return tlb.handleTranslationMiss(now, req)
}

func (tlb *TLB) handleTranslationHits(now sim.VTimeInSec, req *vm.TranslationReq) bool {
	if tlb.usePTCLSetLookup(req) {
		return tlb.handlePTCLSetTranslationHits(now, req)
	}

	pages := [8]vm.Page{}
	BaseVaddr := tlb.getBaseVaddr(req.VAddr)
	BaseVPN := BaseVaddr >> tlb.log2PageSize

	bitmap := tlb.normalizeBitmap(req)

	for i := 0; i < 8; i++ {

		if !bitmap[i] {
			continue
		}

		VPN := BaseVPN + uint64(i)
		newVaddr := VPN << tlb.log2PageSize

		setID := tlb.vAddrToSetID(newVaddr)
		set := tlb.Sets[setID]
		wayID, page, found := set.Lookup(req.PID, newVaddr)

		if found && page.Valid {
			pages[i] = page
			tlb.visit(setID, wayID)
		} else {
			return false
		}
	}

	if !req.IsPrefetch {
		tlb.observePrefetchUsefulHit(req)
	}

	for i := 0; i < 8; i++ {

		if !bitmap[i] {
			continue
		}

		ok := tlb.sendRspToTop(now, req, pages[i])

		if !ok {
			return false
		}

	}

	// tlb.topPort.Retrieve(now)
	tlb.reqBuffer = tlb.reqBuffer[1:]

	return true
}

func (tlb *TLB) handlePTCLSetTranslationHits(
	now sim.VTimeInSec,
	req *vm.TranslationReq,
) bool {
	bitmap := tlb.normalizeBitmap(req)
	baseVAddr := tlb.getBaseVaddr(req.VAddr)
	setID := tlb.ptclVAddrToSetID(req.PID, baseVAddr)
	ptclSet, ok := tlb.Sets[setID].(internal.PTCLSet)
	if !ok {
		return false
	}

	result := ptclSet.LookupPTCL(req.PID, baseVAddr, bitmap, tlb.pageSize)
	requestedBits := tlb.bitmapCount(bitmap)
	hitBits := tlb.bitmapCount(result.HitBitmap)
	missBits := tlb.bitmapCount(result.MissBitmap)
	tlb.setLookupJobs++
	tlb.setLookupRequestedBits += requestedBits
	tlb.setLookupHitBits += hitBits
	tlb.setLookupMissBits += missBits
	if requestedBits > 1 {
		tlb.setLookupSavedJobs += requestedBits - 1
	}
	if missBits > 0 {
		return false
	}

	if !req.IsPrefetch {
		tlb.observePrefetchUsefulHit(req)
	}

	for i := 0; i < 8; i++ {
		if !bitmap[i] {
			continue
		}
		if !tlb.sendRspToTop(now, req, result.Pages[i]) {
			return false
		}
	}

	tlb.reqBuffer = tlb.reqBuffer[1:]
	return true
}

func (tlb *TLB) handleTranslationMiss(
	now sim.VTimeInSec,
	req *vm.TranslationReq,
) bool {
	if req.IsPrefetch {
		return tlb.handlePrefetchMiss(now, req)
	}

	if tlb.mshr.IsFull() {
		translationtrace.BeginStage(req.ID, "iommutlb_mshr_wait", now)
		return false
	}
	translationtrace.EndStage(req.ID, "iommutlb_mshr_wait", now)

	if tlb.mshr.IsEntryFull(req.PID, req.VAddr) {
		translationtrace.BeginStage(req.ID, "iommutlb_mshr_entry_wait", now)
		return false
	}
	translationtrace.EndStage(req.ID, "iommutlb_mshr_entry_wait", now)

	fetched := tlb.fetchBottom(now, req)
	if fetched {
		// tlb.topPort.Retrieve(now)
		tlb.reqBuffer = tlb.reqBuffer[1:]

		tracing.TraceReqReceive(req, tlb)
		tracing.AddTaskStep(tracing.MsgIDAtReceiver(req, tlb), tlb, "miss")
		return true
	}

	return false
}

func (tlb *TLB) handlePrefetchMiss(
	now sim.VTimeInSec,
	req *vm.TranslationReq,
) bool {
	reqToBottom, ok := tlb.issueBottomReqs(now, req, tlb.normalizeBitmap(req))
	if !ok {
		return false
	}

	pageBlock := uint64(0)
	if page, found := tlb.lookupRequestPage(req); found {
		pageBlock = page.PageBlock
	}
	triggerGPM := req.DeviceID
	if req.StartGPUID >= 0 {
		triggerGPM = uint64(req.StartGPUID)
	}
	tlb.registerInflightPrefetch(
		req,
		reqToBottom,
		pageBlock,
		tlb.ptclID(req.VAddr),
		triggerGPM,
		now,
	)
	tlb.reqBuffer = tlb.reqBuffer[1:]
	tracing.TraceReqReceive(req, tlb)
	tracing.AddTaskStep(tracing.MsgIDAtReceiver(req, tlb), tlb, "prefetch-miss")
	return true
}

func (tlb *TLB) vAddrToSetID(vAddr uint64) (setID int) {
	return int(vAddr / tlb.pageSize % uint64(tlb.numSets))
}

func (tlb *TLB) ptclVAddrToSetID(pid vm.PID, baseVAddr uint64) (setID int) {
	if tlb.numSets <= 0 {
		return 0
	}

	ptclID := baseVAddr >> (tlb.log2PageSize + 3)
	shift := uint(0)
	for (1 << shift) < tlb.numSets {
		shift++
	}

	pidHash := uint64(pid) ^ (uint64(pid) >> shift)
	hashed := ptclID ^ (ptclID >> shift) ^ pidHash
	if tlb.numSets&(tlb.numSets-1) == 0 {
		return int(hashed & uint64(tlb.numSets-1))
	}

	return int(hashed % uint64(tlb.numSets))
}

func (tlb *TLB) usePTCLSetLookup(req *vm.TranslationReq) bool {
	return req != nil &&
		tlb.setAsLineTLBEnabled &&
		!tlb.vpnMSHRBaseline &&
		!tlb.demandPTEOnly
}

func (tlb *TLB) sendRspToTop(
	now sim.VTimeInSec,
	req *vm.TranslationReq,
	page vm.Page,
) bool {
	if req.Src == nil {
		panic(fmt.Sprintf(
			"mmutlb hit response has nil Src pid=%d vAddr=%#x device=%d prefetch=%t task=%s",
			req.PID, req.VAddr, req.DeviceID, req.IsPrefetch, req.TaskID,
		))
	}

	rsp := vm.TranslationRspBuilder{}.
		WithSendTime(now).
		WithSrc(tlb.topPort).
		WithDst(req.Src).
		WithRspTo(req.ID).
		WithPage(page).
		WithOriginPort(req.OriginPort).
		Build()

	err := tlb.topPort.Send(rsp)
	if err != nil {
		// fmt.Printf("Failed to send response to top for page %d, error: %s\n", page.VAddr, err)
		return false
	}

	return true
}

func (tlb *TLB) processTLBMSHRHit(
	now sim.VTimeInSec,
	mshrEntry *mshrEntry,
	req *vm.TranslationReq,
) bool {
	if tlb.mshr.IsEntryFull(req.PID, req.VAddr) {
		translationtrace.BeginStage(req.ID, "iommutlb_mshr_entry_wait", now)
		return false
	}
	translationtrace.EndStage(req.ID, "iommutlb_mshr_entry_wait", now)

	requestBitmap := tlb.normalizeBitmap(req)
	toIssue := tlb.subtractBitmaps(requestBitmap, mshrEntry.IssuedBitMap)
	if !tlb.isBitmapZero(toIssue) {
		reqToBottom, ok := tlb.issueBottomReqs(now, req, toIssue)
		if !ok {
			return false
		}
		mshrEntry.reqToBottom = reqToBottom
		mshrEntry.IssuedBitMap = mergeBitmaps(mshrEntry.IssuedBitMap, toIssue)
	}

	mshrEntry.UplevelBitMap = mergeBitmaps(mshrEntry.UplevelBitMap, requestBitmap)
	mshrEntry.Requests = append(mshrEntry.Requests, req)
	tlb.reqBuffer = tlb.reqBuffer[1:]

	tracing.TraceReqReceive(req, tlb)
	tracing.AddTaskStep(tracing.MsgIDAtReceiver(req, tlb), tlb, "mshr-hit")

	return true
}

func (tlb *TLB) fetchBottom(now sim.VTimeInSec, req *vm.TranslationReq) bool {
	requestBitmap := tlb.normalizeBitmap(req)
	reqToBottom, ok := tlb.issueBottomReqs(now, req, requestBitmap)
	if !ok {
		return false
	}

	mshrEntry := tlb.mshr.Add(req.PID, req.VAddr, requestBitmap)
	mshrEntry.Requests = append(mshrEntry.Requests, req)
	mshrEntry.reqToBottom = reqToBottom
	mshrEntry.IssuedBitMap = mergeBitmaps(mshrEntry.IssuedBitMap, requestBitmap)

	return true
}

func (tlb *TLB) parseBottom(now sim.VTimeInSec) bool {
	item := tlb.bottomPort.Peek()
	if item == nil {
		return false
	}

	switch rsp := item.(type) {
	case *vm.TranslationRsp:
		if tlb.handleRsp(now, rsp) {
			tlb.bottomPort.Retrieve(now)
			return true
		}
		return false
	default:
		log.Panicf("cannot process request %s", reflect.TypeOf(item))
	}

	return false
}

func (tlb *TLB) handleRsp(now sim.VTimeInSec, rsp *vm.TranslationRsp) bool {
	page := rsp.Page

	// fmt.Printf("Received from %s VAddr %d\n", rsp.Src.Name(), page.VAddr)

	if rsp.IsPrefetch {
		return tlb.handlePrefetchRsp(now, rsp)
	}

	mshrEntryPresent := tlb.mshr.IsEntryPresent(rsp.Page.PID, rsp.Page.VAddr)
	if !mshrEntryPresent {
		if !tlb.installPage(page) {
			panic("failed to evict")
		}
		return true
	}

	if !tlb.installPage(page) {
		panic("failed to evict")
	}

	tlb.mshr.UpdatePage(rsp.Page.PID, rsp.Page.VAddr, page)
	tlb.mshr.UpdateResponseBitMap(rsp.Page.PID, rsp.Page.VAddr)

	mshrEntry := tlb.mshr.GetEntry(page.PID, page.VAddr)
	if mshrEntry == nil {
		return true
	}

	if mshrEntry.IsReady() {
		tlb.respondingMSHREntry = append(tlb.respondingMSHREntry, mshrEntry)
		tlb.mshr.Remove(page.PID, page.VAddr)
	}

	return true
}

func (tlb *TLB) handlePrefetchRsp(
	now sim.VTimeInSec,
	rsp *vm.TranslationRsp,
) bool {
	page := rsp.Page
	if !tlb.installPage(page) {
		panic("failed to evict")
	}

	if rsp.OriginPort == nil {
		panic("prefetch response has nil origin port")
	}

	if !tlb.topPort.CanSend() {
		return false
	}

	respondTo := rsp.RespondTo
	if state, found := tlb.prefetchReqStates[rsp.RespondTo]; found && state.req != nil {
		respondTo = state.req.ID
	}

	forwardRsp := vm.TranslationRspBuilder{}.
		WithSendTime(now).
		WithSrc(tlb.topPort).
		WithDst(rsp.OriginPort).
		WithRspTo(respondTo).
		WithPage(page).
		WithTaskID(rsp.TaskID).
		WithOriginPort(rsp.OriginPort).
		WithPrefetch(true).
		Build()

	err := tlb.topPort.Send(forwardRsp)
	if err != nil {
		return false
	}

	tlb.completeInflightPrefetchRsp(rsp)
	return true
}

func (tlb *TLB) visit(setID, wayID int) {
	set := tlb.Sets[setID]
	set.Visit(wayID)
}

func (tlb *TLB) installPage(page vm.Page) bool {
	if tlb.setAsLineTLBEnabled && !tlb.vpnMSHRBaseline && !tlb.demandPTEOnly {
		baseVAddr := tlb.getBaseVaddr(page.VAddr)
		bit := int((page.VAddr - baseVAddr) >> tlb.log2PageSize)
		if bit < 0 || bit >= 8 {
			return false
		}

		pages := [8]vm.Page{}
		bitmap := [8]bool{}
		pages[bit] = page
		bitmap[bit] = true
		return tlb.installBitmapPages(page.PID, baseVAddr, pages, bitmap)
	}

	setID := tlb.vAddrToSetID(page.VAddr)
	set := tlb.Sets[setID]
	wayID, ok := tlb.Sets[setID].Evict()
	if !ok {
		return false
	}
	set.Update(wayID, page)
	set.Visit(wayID)
	return true
}

func (tlb *TLB) installBitmapPages(
	pid vm.PID,
	baseVAddr uint64,
	pages [8]vm.Page,
	bitmap [8]bool,
) bool {
	if tlb.isBitmapZero(bitmap) {
		return false
	}

	if tlb.setAsLineTLBEnabled && !tlb.vpnMSHRBaseline && !tlb.demandPTEOnly {
		setID := tlb.ptclVAddrToSetID(pid, baseVAddr)
		ptclSet, ok := tlb.Sets[setID].(internal.PTCLSet)
		if !ok {
			return false
		}

		result := ptclSet.FillPTCL(pid, baseVAddr, pages, bitmap, tlb.pageSize)
		if result.Installed {
			tlb.setFills++
		}
		if result.ConflictEvicted {
			tlb.setConflictEvictions++
		}
		return result.Installed
	}

	installed := false
	for i := 0; i < 8; i++ {
		if !bitmap[i] || !pages[i].Valid {
			continue
		}
		installed = tlb.installPage(pages[i]) || installed
	}
	return installed
}

func (tlb *TLB) prefetchOutcomeState(pageBlock uint64) *prefetchOutcomeCounts {
	state, found := tlb.prefetchOutcomeByBlock[pageBlock]
	if !found {
		state = &prefetchOutcomeCounts{}
		tlb.prefetchOutcomeByBlock[pageBlock] = state
	}

	return state
}

func (tlb *TLB) prefetchFeedbackState(pageBlock, targetGPM uint64) *prefetchFeedbackCounters {
	key := prefetchFeedbackKey{pageBlock: pageBlock, targetGPM: targetGPM}
	state, found := tlb.prefetchFeedbackByTarget[key]
	if !found {
		state = &prefetchFeedbackCounters{}
		tlb.prefetchFeedbackByTarget[key] = state
	}

	return state
}

func (tlb *TLB) handlePrefetchFeedback(msg *vm.PrefetchFeedbackMsg) {
	if msg == nil {
		return
	}

	state := tlb.prefetchFeedbackState(msg.PageBlock, msg.TargetGPM)
	switch msg.State {
	case vm.PrefetchFeedbackStateEnabled:
		state.disabled = false
		state.usefulHit = 0
		state.late = 0
		state.lostBeforeUse = 0
	case vm.PrefetchFeedbackStateDisabled:
		state.disabled = true
	default:
		return
	}
}

func (tlb *TLB) maybeDisablePrefetchByFeedback(state *prefetchFeedbackCounters) {
	if state == nil {
		return
	}

	bad := state.lostBeforeUse
	resolved := bad + state.usefulHit
	if resolved < prefetchFeedbackDisableThreshold {
		return
	}

	if bad > state.usefulHit {
		state.disabled = true
		return
	}

	if state.usefulHit > bad {
		state.disabled = false
	}
}

func (tlb *TLB) resetPrefetchFeedbackForBlock(pageBlock uint64) {
	for key := range tlb.prefetchFeedbackByTarget {
		if key.pageBlock == pageBlock {
			delete(tlb.prefetchFeedbackByTarget, key)
		}
	}
}

func (tlb *TLB) recordPrefetchUsefulFeedback(pageBlock, targetGPM uint64) {
	state := tlb.prefetchFeedbackState(pageBlock, targetGPM)
	state.usefulHit++
	tlb.maybeDisablePrefetchByFeedback(state)
}

func (tlb *TLB) recordPrefetchLateFeedback(pageBlock, targetGPM uint64) {
	state := tlb.prefetchFeedbackState(pageBlock, targetGPM)
	state.late++
	tlb.maybeDisablePrefetchByFeedback(state)
}

func (tlb *TLB) recordPrefetchLostFeedback(pageBlock, targetGPM uint64) {
	state := tlb.prefetchFeedbackState(pageBlock, targetGPM)
	state.lostBeforeUse++
	tlb.maybeDisablePrefetchByFeedback(state)
}

func (tlb *TLB) prefetchDisabledByFeedback(pageBlock, targetGPM uint64) bool {
	key := prefetchFeedbackKey{pageBlock: pageBlock, targetGPM: targetGPM}
	state, found := tlb.prefetchFeedbackByTarget[key]
	if !found {
		return false
	}

	return state.disabled
}

func (tlb *TLB) prefetchKeyForReq(req *vm.TranslationReq) prefetchTargetKey {
	return prefetchTargetKey{
		pid:       req.PID,
		targetGPM: req.DeviceID,
		baseVAddr: tlb.getBaseVaddr(req.VAddr),
	}
}

func (tlb *TLB) recordPrefetchEnqueued(pageBlock uint64) {
	tlb.prefetchOutcomeState(pageBlock).Enqueued++
}

func (tlb *TLB) recordPrefetchCompleted(
	state *prefetchReqState,
	now sim.VTimeInSec,
) {
	if state == nil {
		return
	}

	tlb.prefetchCompletedCount++
	tlb.prefetchOutcomeState(state.pageBlock).Completed++
	if state.lateDemandObserved {
		return
	}

	tlb.completedPrefetches[state.key] = &completedPrefetchState{
		key:          state.key,
		pageBlock:    state.pageBlock,
		targetPTCL:   state.targetPTCL,
		triggerGPM:   state.triggerGPM,
		issueTime:    state.issueTime,
		completeTime: now,
	}
}

func (tlb *TLB) observePrefetchUsefulHit(req *vm.TranslationReq) {
	if req == nil {
		return
	}

	key := tlb.prefetchKeyForReq(req)
	state, found := tlb.completedPrefetches[key]
	if !found {
		return
	}

	tlb.prefetchUsefulHitCount++
	tlb.prefetchOutcomeState(state.pageBlock).Useful++
	tlb.recordPrefetchUsefulFeedback(state.pageBlock, state.key.targetGPM)
	delete(tlb.completedPrefetches, key)
}

func (tlb *TLB) observePrefetchDemandMiss(req *vm.TranslationReq) {
	if req == nil {
		return
	}

	key := tlb.prefetchKeyForReq(req)
	if state, found := tlb.completedPrefetches[key]; found {
		tlb.prefetchLostBeforeUseCount++
		tlb.prefetchOutcomeState(state.pageBlock).LostBeforeUse++
		tlb.recordPrefetchLostFeedback(state.pageBlock, state.key.targetGPM)
		delete(tlb.completedPrefetches, key)
		return
	}

	state, found := tlb.inflightPrefetchStateByKey[key]
	if !found || state.lateDemandObserved {
		return
	}

	state.lateDemandObserved = true
	tlb.prefetchLateDemandCount++
	tlb.prefetchOutcomeState(state.pageBlock).Late++
	tlb.recordPrefetchLateFeedback(state.pageBlock, state.key.targetGPM)
}

func (tlb *TLB) printPrefetcherGateEvent(
	now sim.VTimeInSec,
	pageBlock uint64,
	action string,
	reason string,
	learner *boPrefetchLearner,
) {
	confirmed := false
	state := prefetchPatternStateCold
	baseGPM := uint64(0)
	baseMinPTCL := uint64(0)
	intraStride := int64(0)
	interStride := int64(0)
	footprint := 0

	if learner != nil {
		confirmed = learner.confirmed
		state = learner.state
		baseGPM = learner.baseGPM
		baseMinPTCL = learner.baseMinPTCL
		intraStride = learner.intraStride
		interStride = learner.interStride
		footprint = learner.footprint()
	}

	fmt.Printf("[PF][gate] cycle=%d component=%s page_block=%d action=%s reason=%s confirmed=%t state=%d base_gpm=%d base_min_ptcl=%d intra_stride=%d inter_stride=%d footprint=%d\n",
		uint64(now*1e9), tlb.Name(), pageBlock, action, reason, confirmed, state,
		baseGPM, baseMinPTCL, intraStride, interStride, footprint)
}

func (tlb *TLB) getBaseVaddr(vAddr uint64) uint64 {
	VPN := vAddr >> tlb.log2PageSize
	BaseVPN := (VPN >> 3) << 3 // Clear the lower 3 bits to get the base VPN
	return BaseVPN << tlb.log2PageSize
}

func (tlb *TLB) getMSHREntryVAddr(vAddr uint64) uint64 {
	if tlb.vpnMSHRBaseline {
		return (vAddr >> tlb.log2PageSize) << tlb.log2PageSize
	}

	return tlb.getBaseVaddr(vAddr)
}

func (tlb *TLB) isInRespondingMSHREntry(pid vm.PID, vAddr uint64) bool {
	baseVAddr := tlb.getMSHREntryVAddr(vAddr)
	for _, e := range tlb.respondingMSHREntry {
		if e.pid == pid && e.baseVAddr == baseVAddr {
			return true
		}
	}
	return false
}

func (tlb *TLB) addToExistingRespondingMSHREntry(req *vm.TranslationReq) bool {
	baseVAddr := tlb.getMSHREntryVAddr(req.VAddr)
	for _, e := range tlb.respondingMSHREntry {
		if e.pid == req.PID && e.baseVAddr == baseVAddr {
			ok := tlb.mergedAndAppendToMSHREntries(e, req)
			if !ok {
				e.Requests = append(e.Requests, req)
			}
			return true
		}
	}
	return false
}

func mergeBitmaps(oldBitmap, newBitmap [8]bool) [8]bool {
	mergedBitmap := [8]bool{}
	for i := 0; i < 8; i++ {
		mergedBitmap[i] = oldBitmap[i] || newBitmap[i]
	}
	return mergedBitmap
}

func (tlb *TLB) mergedAndAppendToMSHREntries(mshr *mshrEntry, req *vm.TranslationReq) bool {
	for _, oldReq := range mshr.Requests {
		if oldReq.DeviceID == req.DeviceID {
			oldBitmap := oldReq.BitMap
			newBitmap := req.BitMap
			mergedBitmap := mergeBitmaps(oldBitmap, newBitmap)
			oldReq.BitMap = mergedBitmap
			return true
		}

	}
	return false
}

func (tlb *TLB) normalizeBitmap(req *vm.TranslationReq) [8]bool {
	if !tlb.isBitmapZero(req.BitMap) {
		return req.BitMap
	}

	return tlb.singlePageBitmap(req.VAddr)
}

func (tlb *TLB) effectiveBitmap(req *vm.TranslationReq) [8]bool {
	if req.IsPrefetch {
		return tlb.normalizeBitmap(req)
	}

	if tlb.demandPTEOnly {
		return tlb.singlePageBitmap(req.VAddr)
	}

	return tlb.normalizeBitmap(req)
}

func (tlb *TLB) singlePageBitmap(vAddr uint64) [8]bool {
	bitmap := [8]bool{}
	vpn := vAddr >> tlb.log2PageSize
	bitmap[vpn%8] = true
	return bitmap
}

func (tlb *TLB) fullBitmap() [8]bool {
	bitmap := [8]bool{}
	for i := 0; i < 8; i++ {
		bitmap[i] = true
	}
	return bitmap
}

func (tlb *TLB) subtractBitmaps(a, b [8]bool) [8]bool {
	result := [8]bool{}
	for i := 0; i < 8; i++ {
		result[i] = a[i] && !b[i]
	}
	return result
}

func (tlb *TLB) isBitmapZero(bitmap [8]bool) bool {
	for i := 0; i < 8; i++ {
		if bitmap[i] {
			return false
		}
	}
	return true
}

func (tlb *TLB) bitmapCount(bitmap [8]bool) int {
	count := 0
	for i := 0; i < 8; i++ {
		if bitmap[i] {
			count++
		}
	}

	return count
}

func (tlb *TLB) issueBottomReqs(
	now sim.VTimeInSec,
	req *vm.TranslationReq,
	bitmap [8]bool,
) (*vm.TranslationReq, bool) {
	bitmap = tlb.filterMappedBitmap(req.PID, req.VAddr, bitmap)
	if tlb.isBitmapZero(bitmap) {
		return nil, false
	}

	baseVAddr := tlb.getBaseVaddr(req.VAddr)
	reqToBottom := vm.TranslationReqBuilder{}.
		WithSendTime(now).
		WithSrc(tlb.bottomPort).
		WithDst(tlb.LowModule).
		WithPID(req.PID).
		WithVAddr(baseVAddr).
		WithDeviceID(req.DeviceID).
		WithTaskID(req.TaskID).
		WithOriginPort(req.OriginPort).
		WithBitMap(bitmap).
		WithPrefetch(req.IsPrefetch).
		Build()
	reqToBottom.StartGPUID = req.StartGPUID

	err := tlb.bottomPort.Send(reqToBottom)
	if err != nil {
		return nil, false
	}

	translationtrace.LinkRequest(reqToBottom.ID, req.ID)
	translationtrace.RecordIOMMUToMMU(now)
	tlb.downstreamReqCount++

	return reqToBottom, true
}

func (tlb *TLB) filterMappedBitmap(
	pid vm.PID,
	vAddr uint64,
	bitmap [8]bool,
) [8]bool {
	baseVAddr := tlb.getBaseVaddr(vAddr)
	filtered := [8]bool{}

	for i := 0; i < 8; i++ {
		if !bitmap[i] {
			continue
		}

		pageVAddr := baseVAddr + (uint64(i) << tlb.log2PageSize)
		if _, found := tlb.pageTable.Find(pid, pageVAddr); found {
			filtered[i] = true
		}
	}

	return filtered
}

func (tlb *TLB) maybeEnqueuePrefetches(
	now sim.VTimeInSec,
	req *vm.TranslationReq,
) {
	if tlb.prefetcher == nil || !tlb.prefetcher.enabled || req.IsPrefetch {
		return
	}

	page, found := tlb.lookupRequestPage(req)
	if !found {
		return
	}

	previousLearner, hadLearner := tlb.prefetcher.learners[page.PageBlock]
	beforeClear := hadLearner && previousLearner != nil && previousLearner.hasClearPattern()
	beforeNoClear := hadLearner && previousLearner != nil && previousLearner.hasNoClearPattern()
	beforeBaseGPM := uint64(0)
	beforeBaseMinPTCL := uint64(0)
	beforeIntraStride := int64(0)
	beforeInterStride := int64(0)
	if hadLearner && previousLearner != nil {
		beforeBaseGPM = previousLearner.baseGPM
		beforeBaseMinPTCL = previousLearner.baseMinPTCL
		beforeIntraStride = previousLearner.intraStride
		beforeInterStride = previousLearner.interStride
	}

	learner := tlb.prefetcher.observe(page.PageBlock, req.DeviceID, tlb.ptclID(req.VAddr))
	if learner == nil {
		return
	}

	afterClear := learner.hasClearPattern()
	afterNoClear := learner.hasNoClearPattern()
	patternParametersChanged := beforeClear && afterClear && (beforeBaseGPM != learner.baseGPM ||
		beforeBaseMinPTCL != learner.baseMinPTCL ||
		beforeIntraStride != learner.intraStride ||
		beforeInterStride != learner.interStride)
	switch {
	case !beforeClear && afterClear:
		tlb.resetPrefetchFeedbackForBlock(page.PageBlock)
		tlb.printPrefetcherGateEvent(now, page.PageBlock, "enable", "pattern-confirmed", learner)
	case !beforeNoClear && afterNoClear:
		tlb.printPrefetcherGateEvent(now, page.PageBlock, "disable", "no-clear-pattern", learner)
	case patternParametersChanged:
		tlb.resetPrefetchFeedbackForBlock(page.PageBlock)
		tlb.printPrefetcherGateEvent(now, page.PageBlock, "update", "pattern-parameters-changed", learner)
	}

	if !afterClear {
		if afterNoClear {
			tlb.prefetcher.noClearPatternSkips++
		}
		return
	}

	tlb.maybePromoteDemandToPTCL(req)

	candidates := learner.predict(
		req.DeviceID,
		tlb.ptclID(req.VAddr),
		tlb.knownGPMs(),
		tlb.prefetcher.lookahead,
	)

	selected := 0
	for _, candidate := range candidates {
		if selected >= tlb.prefetcher.maxCandidatesPerReq {
			break
		}

		tlb.prefetcher.generatedCandidates++

		if !tlb.sharedFinePrefix(req.VAddr, tlb.ptclBaseVAddr(candidate.ptclID)) {
			tlb.prefetcher.droppedCandidates++
			tlb.prefetcher.rejectedByPrefix++
			continue
		}

		reason := tlb.prefetchRejectReason(req.PID, candidate.targetGPM, candidate.ptclID)
		if reason != prefetchRejectNone {
			tlb.prefetcher.droppedCandidates++
			switch reason {
			case prefetchRejectDuplicate:
				tlb.prefetcher.rejectedByDuplicate++
			default:
				tlb.prefetcher.rejectedByInvalid++
			}
			continue
		}

		// if prefetchgate.Disabled(page.PageBlock, candidate.targetGPM) {
		// 	tlb.prefetcher.droppedCandidates++
		// 	continue
		// }

		if tlb.prefetchDisabledByFeedback(page.PageBlock, candidate.targetGPM) {
			tlb.prefetcher.droppedCandidates++
			continue
		}

		if tlb.enqueuePrefetchRequest(now, req, page.PageBlock, candidate) {
			tlb.prefetcher.enqueuedCandidates++
			selected++
			continue
		}

		tlb.prefetcher.droppedCandidates++
	}
}

func (tlb *TLB) maybePromoteDemandToPTCL(req *vm.TranslationReq) {
	if req.IsPrefetch || tlb.prefetcher == nil || !tlb.prefetcher.promoteDemandToPTCL {
		return
	}

	promoted := tlb.filterMappedBitmap(req.PID, req.VAddr, tlb.fullBitmap())
	if tlb.isBitmapZero(promoted) || tlb.bitmapsEqual(promoted, req.BitMap) {
		return
	}

	req.BitMap = promoted
	tlb.prefetcher.promotedDemandRequests++
}

type prefetchRejectReason int

const (
	prefetchRejectNone prefetchRejectReason = iota
	prefetchRejectInvalid
	prefetchRejectDuplicate
)

func (tlb *TLB) prefetchRejectReason(
	pid vm.PID,
	targetGPM uint64,
	ptclID uint64,
) prefetchRejectReason {
	if tlb.gmmuCacheTable == nil {
		return prefetchRejectInvalid
	}

	targetPort := tlb.gmmuCacheTable.LowModules[targetGPM]
	if targetPort == nil {
		return prefetchRejectInvalid
	}

	baseVAddr := tlb.ptclBaseVAddr(ptclID)
	bitmap := tlb.filterMappedBitmap(pid, baseVAddr, tlb.fullBitmap())
	if tlb.isBitmapZero(bitmap) {
		return prefetchRejectInvalid
	}

	if tlb.hasBufferedRequestForTarget(pid, targetGPM, baseVAddr) {
		return prefetchRejectDuplicate
	}

	if tlb.hasPendingMSHRRequestForTarget(pid, targetGPM, baseVAddr) {
		return prefetchRejectDuplicate
	}

	if tlb.hasRespondingRequestForTarget(pid, targetGPM, baseVAddr) {
		return prefetchRejectDuplicate
	}

	if tlb.hasInflightPrefetchForTarget(pid, targetGPM, baseVAddr) {
		return prefetchRejectDuplicate
	}

	return prefetchRejectNone
}

func (tlb *TLB) hasBufferedRequestForTarget(
	pid vm.PID,
	targetGPM uint64,
	baseVAddr uint64,
) bool {
	for _, req := range tlb.reqBuffer {
		if tlb.requestMatchesTarget(req, pid, targetGPM, baseVAddr) {
			return true
		}
	}

	return false
}

func (tlb *TLB) hasPendingMSHRRequestForTarget(
	pid vm.PID,
	targetGPM uint64,
	baseVAddr uint64,
) bool {
	for _, entry := range tlb.mshr.AllEntries() {
		if entry.pid != pid {
			continue
		}

		for _, req := range entry.Requests {
			if tlb.requestMatchesTarget(req, pid, targetGPM, baseVAddr) {
				return true
			}
		}
	}

	return false
}

func (tlb *TLB) hasRespondingRequestForTarget(
	pid vm.PID,
	targetGPM uint64,
	baseVAddr uint64,
) bool {
	for _, entry := range tlb.respondingMSHREntry {
		if entry.pid != pid {
			continue
		}

		for _, req := range entry.Requests {
			if tlb.requestMatchesTarget(req, pid, targetGPM, baseVAddr) {
				return true
			}
		}
	}

	return false
}

func (tlb *TLB) hasInflightPrefetchForTarget(
	pid vm.PID,
	targetGPM uint64,
	baseVAddr uint64,
) bool {
	_, found := tlb.inflightPrefetches[prefetchTargetKey{
		pid:       pid,
		targetGPM: targetGPM,
		baseVAddr: baseVAddr,
	}]
	return found
}

func (tlb *TLB) requestMatchesTarget(
	req *vm.TranslationReq,
	pid vm.PID,
	targetGPM uint64,
	baseVAddr uint64,
) bool {
	if req == nil {
		return false
	}

	return req.PID == pid &&
		req.DeviceID == targetGPM &&
		tlb.getBaseVaddr(req.VAddr) == baseVAddr
}

func (tlb *TLB) enqueuePrefetchRequest(
	now sim.VTimeInSec,
	demandReq *vm.TranslationReq,
	pageBlock uint64,
	candidate prefetchCandidate,
) bool {
	targetPort := tlb.gmmuCacheTable.LowModules[candidate.targetGPM]
	if targetPort == nil {
		return false
	}

	baseVAddr := tlb.ptclBaseVAddr(candidate.ptclID)
	bitmap := tlb.filterMappedBitmap(demandReq.PID, baseVAddr, tlb.fullBitmap())
	if tlb.isBitmapZero(bitmap) {
		return false
	}

	prefetchReq := vm.TranslationReqBuilder{}.
		WithSendTime(now).
		WithSrc(targetPort).
		WithDst(tlb.topPort).
		WithPID(demandReq.PID).
		WithVAddr(baseVAddr).
		WithDeviceID(candidate.targetGPM).
		WithTaskID(sim.GetIDGenerator().Generate()).
		WithOriginPort(targetPort).
		WithBitMap(bitmap).
		WithPrefetch(true).
		Build()
	prefetchReq.StartGPUID = int(demandReq.DeviceID)

	reqToBottom, ok := tlb.issueBottomReqs(now, prefetchReq, tlb.normalizeBitmap(prefetchReq))
	if !ok {
		return false
	}

	tlb.registerInflightPrefetch(
		prefetchReq,
		reqToBottom,
		pageBlock,
		candidate.ptclID,
		demandReq.DeviceID,
		now,
	)
	tracing.TraceReqReceive(prefetchReq, tlb)
	tracing.AddTaskStep(tracing.MsgIDAtReceiver(prefetchReq, tlb), tlb, "prefetch-miss")
	return true
}

func (tlb *TLB) setLookupReadyTime(
	now sim.VTimeInSec,
	req *vm.TranslationReq,
) {
	if req == nil {
		return
	}

	if tlb.lookupLatencyCycles <= 0 {
		delete(tlb.lookupReadyTimes, req.ID)
		return
	}

	lookupBits := tlb.bitmapCount(req.BitMap)
	if lookupBits <= 0 {
		lookupBits = 1
	}
	if tlb.usePTCLSetLookup(req) {
		lookupBits = 1
	}

	tlb.lookupReadyTimes[req.ID] = tlb.Freq.NCyclesLater(
		tlb.lookupLatencyCycles*lookupBits,
		now,
	)
	if !req.IsPrefetch {
		translationtrace.AddStageCycles(
			req.ID,
			"iommutlb_lookup_service",
			uint64(tlb.lookupLatencyCycles*lookupBits),
		)
	}
}

func (tlb *TLB) isLookupReady(
	now sim.VTimeInSec,
	req *vm.TranslationReq,
) bool {
	if req == nil || tlb.lookupLatencyCycles <= 0 {
		return true
	}

	readyTime, found := tlb.lookupReadyTimes[req.ID]
	if !found {
		return true
	}

	if readyTime > now {
		tlb.TickNow(readyTime)
		return false
	}

	delete(tlb.lookupReadyTimes, req.ID)
	return true
}

func (tlb *TLB) registerInflightPrefetch(
	req *vm.TranslationReq,
	reqToBottom *vm.TranslationReq,
	pageBlock uint64,
	targetPTCL uint64,
	triggerGPM uint64,
	now sim.VTimeInSec,
) {
	if req == nil || reqToBottom == nil || !req.IsPrefetch {
		return
	}

	remainingPages := tlb.bitmapCount(reqToBottom.BitMap)
	if remainingPages == 0 {
		return
	}

	key := prefetchTargetKey{
		pid:       reqToBottom.PID,
		targetGPM: reqToBottom.DeviceID,
		baseVAddr: tlb.getBaseVaddr(reqToBottom.VAddr),
	}

	state := &prefetchReqState{
		key:            key,
		req:            req,
		remainingPages: remainingPages,
		pageBlock:      pageBlock,
		targetPTCL:     targetPTCL,
		triggerGPM:     triggerGPM,
		issueTime:      now,
	}
	tlb.inflightPrefetches[key] = struct{}{}
	tlb.prefetchReqStates[reqToBottom.ID] = state
	tlb.inflightPrefetchStateByKey[key] = state
	tlb.recordPrefetchEnqueued(pageBlock)
}

func (tlb *TLB) completeInflightPrefetchRsp(rsp *vm.TranslationRsp) {
	if rsp == nil || !rsp.IsPrefetch {
		return
	}

	state, found := tlb.prefetchReqStates[rsp.RespondTo]
	if !found {
		return
	}

	state.remainingPages--
	if state.remainingPages > 0 {
		return
	}

	delete(tlb.prefetchReqStates, rsp.RespondTo)
	delete(tlb.inflightPrefetches, state.key)
	delete(tlb.inflightPrefetchStateByKey, state.key)
	tlb.recordPrefetchCompleted(state, rsp.SendTime)

	if state.req != nil {
		tracing.TraceReqComplete(state.req, tlb)
	}
}

func (tlb *TLB) lookupRequestPage(req *vm.TranslationReq) (vm.Page, bool) {
	if tlb.isBitmapZero(req.BitMap) {
		return tlb.pageTable.Find(req.PID, req.VAddr)
	}

	baseVAddr := tlb.getBaseVaddr(req.VAddr)
	for i := 0; i < 8; i++ {
		if !req.BitMap[i] {
			continue
		}

		pageVAddr := baseVAddr + (uint64(i) << tlb.log2PageSize)
		page, found := tlb.pageTable.Find(req.PID, pageVAddr)
		if found {
			return page, true
		}
	}

	return vm.Page{}, false
}

func (tlb *TLB) ptclID(vAddr uint64) uint64 {
	return tlb.getBaseVaddr(vAddr) >> (tlb.log2PageSize + 3)
}

func (tlb *TLB) ptclBaseVAddr(ptclID uint64) uint64 {
	return ptclID << (tlb.log2PageSize + 3)
}

func (tlb *TLB) knownGPMs() []uint64 {
	if tlb.knownGPMsReady {
		return tlb.knownGPMIDs
	}

	tlb.knownGPMsReady = true
	if tlb.gmmuCacheTable == nil {
		return nil
	}

	ids := make([]uint64, 0, len(tlb.gmmuCacheTable.LowModules))
	for gpmID := range tlb.gmmuCacheTable.LowModules {
		ids = append(ids, gpmID)
	}

	sort.Slice(ids, func(i, j int) bool {
		return ids[i] < ids[j]
	})

	tlb.knownGPMIDs = ids
	return tlb.knownGPMIDs
}

func (tlb *TLB) sharedFinePrefix(vAddr1, vAddr2 uint64) bool {
	vpn1 := vAddr1 >> tlb.log2PageSize
	vpn2 := vAddr2 >> tlb.log2PageSize
	return (vpn1 >> 6) == (vpn2 >> 6)
}

func (tlb *TLB) bitmapsEqual(a, b [8]bool) bool {
	for i := 0; i < 8; i++ {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
