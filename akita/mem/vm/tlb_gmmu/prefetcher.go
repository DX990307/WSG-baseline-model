package tlb_gmmu

import (
	"fmt"
	"sort"

	"github.com/sarchlab/akita/v3/mem/vm"
	"github.com/sarchlab/akita/v3/sim"
	"github.com/sarchlab/akita/v3/tracing"
)

type prefetchPatternState int

const (
	prefetchPatternStateCold prefetchPatternState = iota
	prefetchPatternStateClear
	prefetchPatternStateNoClear
)

type prefetchObservation struct {
	gpmID  uint64
	ptclID uint64
}

type timedPrefetchObservation struct {
	ptclID uint64
	cycle  int
}

type prefetchCandidate struct {
	targetGPM uint64
	ptclID    uint64
}

const minPrefetchIssuesBeforeIOMMUFallback = 16
const prefetchProbeCompletionThreshold = 4
const prefetchZeroUsefulCompletionThreshold = 8

// PTWStateProvider exposes the downstream page-walk capacity that the L2 TLB
// prefetcher uses to keep prefetches behind demand translations.
type PTWStateProvider interface {
	HasFreePTW() bool
	PTWInflight() int
	PTWCapacity() int
}

type coldObservationState struct {
	buffered []prefetchObservation
	seen     map[prefetchObservation]struct{}
}

type boPrefetchLearner struct {
	boID        uint64
	perGPMPTCLs map[uint64]map[uint64]struct{}
	uniquePTCLs map[uint64]struct{}
	confirmed   bool
	state       prefetchPatternState
	baseGPM     uint64
	baseMinPTCL uint64
	intraStride int64
	interStride int64

	lastObservationByGPM map[uint64]timedPrefetchObservation
	streamIntervalWindow cycleMovingAverage
	streamIntervalCycles int
}

type translationPrefetcher struct {
	enabled                     bool
	admissionThreshold          int
	maxLearners                 int
	lookahead                   int
	maxCandidatesPerReq         int
	minLookahead                int
	lookaheadMargin             int
	demandLatencyCycles         int
	localPrefetchLatencyCycles  int
	remotePrefetchLatencyCycles int
	currentLookahead            int
	demandLatencyWindow         cycleMovingAverage
	localPrefetchLatencyWindow  cycleMovingAverage
	remotePrefetchLatencyWindow cycleMovingAverage
	coldBOs                     map[uint64]*coldObservationState
	learners                    map[uint64]*boPrefetchLearner
	generatedCandidates         int
	enqueuedCandidates          int
	issuedCandidates            int
	droppedCandidates           int
	rejectedByPrefix            int
	rejectedByDuplicate         int
	rejectedByInvalid           int
	rejectedByFeedback          int
	rejectedByIOMMUFallbackGate int
	noClearPatternSkips         int
	admittedLearnersCount       int
	blockedByNoFreePTW          int
	iommuFallbackCandidates     int
}

type prefetchTargetKey struct {
	pid       vm.PID
	targetGPM uint64
	baseVAddr uint64
}

type prefetchedResidentKey struct {
	pid   vm.PID
	vAddr uint64
}

type prefetchReqState struct {
	key            prefetchTargetKey
	req            *vm.TranslationReq
	remainingPages int
	pageBlock      uint64
	targetPTCL     uint64
	issueTime      sim.VTimeInSec
	remote         bool
}

type prefetchQueueEntry struct {
	pid        vm.PID
	pageBlock  uint64
	candidate  prefetchCandidate
	bitmap     [8]bool
	startGPUID int
}

type downstreamReqState struct {
	issueTime sim.VTimeInSec
	remote    bool
	prefetch  bool
}

type cycleMovingAverage struct {
	samples      []int
	next         int
	sum          int
	maxSamples   int
	defaultValue int
}

const prefetchLatencyWindowSize = 16

func newCycleMovingAverage(defaultValue, maxSamples int) cycleMovingAverage {
	if defaultValue <= 0 {
		defaultValue = 1
	}
	if maxSamples <= 0 {
		maxSamples = prefetchLatencyWindowSize
	}

	return cycleMovingAverage{
		maxSamples:   maxSamples,
		defaultValue: defaultValue,
	}
}

func (w *cycleMovingAverage) Reset() {
	w.samples = nil
	w.next = 0
	w.sum = 0
}

func (w *cycleMovingAverage) Add(sample int) int {
	if sample <= 0 {
		sample = 1
	}
	if w.maxSamples <= 0 {
		w.maxSamples = prefetchLatencyWindowSize
	}
	if w.defaultValue <= 0 {
		w.defaultValue = 1
	}

	if len(w.samples) < w.maxSamples {
		w.samples = append(w.samples, sample)
		w.sum += sample
		return w.Average()
	}

	w.sum -= w.samples[w.next]
	w.samples[w.next] = sample
	w.sum += sample
	w.next = (w.next + 1) % w.maxSamples
	return w.Average()
}

func (w *cycleMovingAverage) Average() int {
	if len(w.samples) == 0 {
		if w.defaultValue <= 0 {
			return 1
		}
		return w.defaultValue
	}

	avg := w.sum / len(w.samples)
	if avg <= 0 {
		return 1
	}
	return avg
}

type prefetchOutcomeCounts struct {
	Enqueued      int
	Completed     int
	Useful        int
	Late          int
	LostBeforeUse int
}

type prefetchRejectReason int

const (
	prefetchRejectNone prefetchRejectReason = iota
	prefetchRejectInvalid
	prefetchRejectDuplicate
)

func newTranslationPrefetcher(
	enabled bool,
	admissionThreshold int,
	maxLearners int,
	lookahead int,
	maxCandidatesPerReq int,
) *translationPrefetcher {
	if admissionThreshold <= 0 {
		admissionThreshold = 3
	}
	if maxLearners <= 0 {
		maxLearners = 4
	}
	if lookahead <= 0 {
		lookahead = 64
	}
	if maxCandidatesPerReq <= 0 {
		maxCandidatesPerReq = 4
	}

	return &translationPrefetcher{
		enabled:                     enabled,
		admissionThreshold:          admissionThreshold,
		maxLearners:                 maxLearners,
		lookahead:                   lookahead,
		maxCandidatesPerReq:         maxCandidatesPerReq,
		minLookahead:                2,
		lookaheadMargin:             2,
		demandLatencyCycles:         500,
		localPrefetchLatencyCycles:  500,
		remotePrefetchLatencyCycles: 800,
		currentLookahead:            4,
		demandLatencyWindow:         newCycleMovingAverage(500, prefetchLatencyWindowSize),
		localPrefetchLatencyWindow:  newCycleMovingAverage(500, prefetchLatencyWindowSize),
		remotePrefetchLatencyWindow: newCycleMovingAverage(800, prefetchLatencyWindowSize),
		coldBOs:                     make(map[uint64]*coldObservationState),
		learners:                    make(map[uint64]*boPrefetchLearner),
	}
}

func (p *translationPrefetcher) observe(
	boID, gpmID, ptclID uint64,
	cycle int,
) *boPrefetchLearner {
	if !p.enabled {
		return nil
	}

	if learner, found := p.learners[boID]; found {
		learner.observe(gpmID, ptclID, cycle)
		return learner
	}

	state, found := p.coldBOs[boID]
	if !found {
		state = &coldObservationState{
			seen: make(map[prefetchObservation]struct{}),
		}
		p.coldBOs[boID] = state
	}

	obs := prefetchObservation{gpmID: gpmID, ptclID: ptclID}
	if _, found := state.seen[obs]; !found {
		state.seen[obs] = struct{}{}
		state.buffered = append(state.buffered, obs)
	}

	if len(state.seen) < p.admissionThreshold {
		return nil
	}

	if !p.canAdmit(boID, len(state.seen)) {
		return nil
	}

	learner := p.admit(boID)
	for _, buffered := range state.buffered {
		learner.observe(buffered.gpmID, buffered.ptclID, 0)
	}
	learner.observe(gpmID, ptclID, cycle)
	delete(p.coldBOs, boID)
	return learner
}

func (p *translationPrefetcher) canAdmit(boID uint64, coldFootprint int) bool {
	if len(p.learners) < p.maxLearners {
		return true
	}

	evictID, evictFootprint := p.smallestLearner()
	if evictID == boID {
		return false
	}

	return coldFootprint > evictFootprint
}

func (p *translationPrefetcher) admit(boID uint64) *boPrefetchLearner {
	if len(p.learners) >= p.maxLearners {
		evictID, _ := p.smallestLearner()
		delete(p.learners, evictID)
	}

	learner := &boPrefetchLearner{
		boID:                 boID,
		perGPMPTCLs:          make(map[uint64]map[uint64]struct{}),
		uniquePTCLs:          make(map[uint64]struct{}),
		lastObservationByGPM: make(map[uint64]timedPrefetchObservation),
		streamIntervalWindow: newCycleMovingAverage(128, prefetchLatencyWindowSize),
	}
	p.learners[boID] = learner
	p.admittedLearnersCount++
	return learner
}

func (p *translationPrefetcher) smallestLearner() (uint64, int) {
	var minID uint64
	minFootprint := -1

	for boID, learner := range p.learners {
		footprint := learner.footprint()
		if minFootprint == -1 || footprint < minFootprint {
			minID = boID
			minFootprint = footprint
		}
	}

	return minID, minFootprint
}

func (l *boPrefetchLearner) footprint() int {
	return len(l.uniquePTCLs)
}

func (l *boPrefetchLearner) observe(gpmID, ptclID uint64, cycle int) {
	l.observeStreamProgress(gpmID, ptclID, cycle)

	row, found := l.perGPMPTCLs[gpmID]
	if !found {
		row = make(map[uint64]struct{})
		l.perGPMPTCLs[gpmID] = row
	}

	row[ptclID] = struct{}{}
	l.uniquePTCLs[ptclID] = struct{}{}
	l.refreshPattern()
}

func (l *boPrefetchLearner) observeStreamProgress(
	gpmID, ptclID uint64,
	cycle int,
) {
	if cycle <= 0 {
		return
	}

	last, found := l.lastObservationByGPM[gpmID]
	if found && cycle > last.cycle && ptclID != last.ptclID {
		distance := absInt64(int64(ptclID) - int64(last.ptclID))
		if distance <= 0 {
			distance = 1
		}

		stride := absInt64(l.intraStride)
		if stride > 0 && distance >= stride {
			if distance%stride == 0 {
				distance = distance / stride
			} else {
				distance = 1
			}
		}

		sample := (cycle - last.cycle) / int(distance)
		if sample > 0 {
			l.streamIntervalCycles = l.streamIntervalWindow.Add(sample)
		}
	}

	l.lastObservationByGPM[gpmID] = timedPrefetchObservation{
		ptclID: ptclID,
		cycle:  cycle,
	}
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}

	return v
}

func (l *boPrefetchLearner) refreshPattern() {
	gpmIDs := make([]uint64, 0, len(l.perGPMPTCLs))
	for gpmID, ptcls := range l.perGPMPTCLs {
		if len(ptcls) >= 3 {
			gpmIDs = append(gpmIDs, gpmID)
		}
	}

	sort.Slice(gpmIDs, func(i, j int) bool {
		return gpmIDs[i] < gpmIDs[j]
	})

	l.confirmed = false
	l.state = prefetchPatternStateCold
	l.baseGPM = 0
	l.baseMinPTCL = 0
	l.intraStride = 0
	l.interStride = 0

	for i := 0; i+2 < len(gpmIDs); i++ {
		row0 := gpmIDs[i]
		row1 := gpmIDs[i+1]
		row2 := gpmIDs[i+2]
		if row1 != row0+1 || row2 != row1+1 {
			continue
		}

		table0 := firstNPTCLs(l.perGPMPTCLs[row0], 3)
		table1 := firstNPTCLs(l.perGPMPTCLs[row1], 3)
		table2 := firstNPTCLs(l.perGPMPTCLs[row2], 3)

		intra0, ok := consistentRowStride(table0)
		if !ok {
			continue
		}
		intra1, ok := consistentRowStride(table1)
		if !ok || intra1 != intra0 {
			continue
		}
		intra2, ok := consistentRowStride(table2)
		if !ok || intra2 != intra0 {
			continue
		}

		inter0 := int64(table1[0]) - int64(table0[0])
		inter1 := int64(table2[0]) - int64(table1[0])
		if inter0 != inter1 {
			continue
		}

		l.confirmed = true
		l.state = prefetchPatternStateClear
		l.baseGPM = row0
		l.baseMinPTCL = table0[0]
		l.intraStride = intra0
		l.interStride = inter0
		return
	}

	for _, row := range gpmIDs {
		table := firstNPTCLs(l.perGPMPTCLs[row], 3)
		intra, ok := consistentRowStride(table)
		if !ok || intra == 0 {
			continue
		}

		l.confirmed = true
		l.state = prefetchPatternStateClear
		l.baseGPM = row
		l.baseMinPTCL = table[0]
		l.intraStride = intra
		l.interStride = 0
		return
	}

	if l.hasSufficientNoClearEvidence(gpmIDs) {
		l.state = prefetchPatternStateNoClear
	}
}

func (l *boPrefetchLearner) hasClearPattern() bool {
	if l == nil {
		return false
	}

	return l.state == prefetchPatternStateClear
}

func (l *boPrefetchLearner) hasNoClearPattern() bool {
	if l == nil {
		return false
	}

	return l.state == prefetchPatternStateNoClear
}

func (l *boPrefetchLearner) hasSufficientNoClearEvidence(gpmIDs []uint64) bool {
	if len(gpmIDs) < 3 {
		return false
	}

	runLength := 1
	bestRunLength := 1
	for i := 1; i < len(gpmIDs); i++ {
		if gpmIDs[i] == gpmIDs[i-1]+1 {
			runLength++
		} else {
			runLength = 1
		}

		if runLength > bestRunLength {
			bestRunLength = runLength
		}
	}

	return bestRunLength >= 3
}

func (l *boPrefetchLearner) predict(
	currentGPM uint64,
	currentPTCL uint64,
	allGPMs []uint64,
	lookahead int,
) []prefetchCandidate {
	if !l.hasClearPattern() || lookahead <= 0 || l.intraStride == 0 {
		return nil
	}

	candidates := make([]prefetchCandidate, 0, lookahead+len(allGPMs))
	seen := make(map[prefetchCandidate]struct{})

	for k := 1; k <= lookahead; k++ {
		predicted := int64(currentPTCL) + int64(k)*l.intraStride
		if predicted < 0 {
			continue
		}

		candidate := prefetchCandidate{
			targetGPM: currentGPM,
			ptclID:    uint64(predicted),
		}
		if _, found := seen[candidate]; !found {
			seen[candidate] = struct{}{}
			candidates = append(candidates, candidate)
		}
	}

	currentOffset, ok := l.patternOffset(currentGPM, currentPTCL)
	if !ok {
		return candidates
	}

	for _, gpmID := range allGPMs {
		if gpmID == currentGPM {
			continue
		}

		startPTCL := l.predictStart(gpmID)
		predicted := startPTCL + int64(currentOffset)*l.intraStride
		if predicted < 0 {
			continue
		}

		candidate := prefetchCandidate{
			targetGPM: gpmID,
			ptclID:    uint64(predicted),
		}
		if _, found := seen[candidate]; found {
			continue
		}

		seen[candidate] = struct{}{}
		candidates = append(candidates, candidate)
	}

	return candidates
}

func (l *boPrefetchLearner) predictAhead(
	currentGPM uint64,
	currentPTCL uint64,
	startDistance int,
	count int,
) []prefetchCandidate {
	if !l.hasClearPattern() || startDistance <= 0 || count <= 0 || l.intraStride == 0 {
		return nil
	}

	candidates := make([]prefetchCandidate, 0, count)
	for offset := 0; offset < count; offset++ {
		distance := startDistance + offset
		predicted := int64(currentPTCL) + int64(distance)*l.intraStride
		if predicted < 0 {
			continue
		}

		candidates = append(candidates, prefetchCandidate{
			targetGPM: currentGPM,
			ptclID:    uint64(predicted),
		})
	}

	return candidates
}

func (l *boPrefetchLearner) streamInterval(defaultValue int) int {
	if l == nil {
		return defaultValue
	}

	if l.streamIntervalCycles > 0 {
		return l.streamIntervalCycles
	}

	avg := l.streamIntervalWindow.Average()
	if avg > 0 {
		return avg
	}

	return defaultValue
}

func (l *boPrefetchLearner) patternOffset(gpmID uint64, ptclID uint64) (int, bool) {
	start := l.predictStart(gpmID)
	delta := int64(ptclID) - start
	if delta < 0 {
		return 0, false
	}
	if l.intraStride == 0 || delta%l.intraStride != 0 {
		return 0, false
	}

	return int(delta / l.intraStride), true
}

func (l *boPrefetchLearner) predictStart(gpmID uint64) int64 {
	return int64(l.baseMinPTCL) + int64(gpmID-l.baseGPM)*l.interStride
}

func firstNPTCLs(ptcls map[uint64]struct{}, n int) []uint64 {
	values := make([]uint64, 0, len(ptcls))
	for ptclID := range ptcls {
		values = append(values, ptclID)
	}

	sort.Slice(values, func(i, j int) bool {
		return values[i] < values[j]
	})

	if len(values) > n {
		values = values[:n]
	}

	return values
}

func consistentRowStride(values []uint64) (int64, bool) {
	if len(values) < 3 {
		return 0, false
	}

	stride := int64(values[1]) - int64(values[0])
	for i := 1; i+1 < len(values); i++ {
		if int64(values[i+1])-int64(values[i]) != stride {
			return 0, false
		}
	}

	return stride, true
}

func (tlb *GMMUTLB) initPrefetchState() {
	if tlb.queuedPrefetches == nil {
		tlb.queuedPrefetches = make(map[prefetchTargetKey]struct{})
	}
	if tlb.inflightPrefetches == nil {
		tlb.inflightPrefetches = make(map[prefetchTargetKey]struct{})
	}
	if tlb.prefetchReqStates == nil {
		tlb.prefetchReqStates = make(map[string]*prefetchReqState)
	}
	if tlb.prefetchOutcomeByBlock == nil {
		tlb.prefetchOutcomeByBlock = make(map[uint64]*prefetchOutcomeCounts)
	}
	if tlb.prefetchedResident == nil {
		tlb.prefetchedResident = make(map[prefetchedResidentKey]uint64)
	}
	if tlb.prefetchDisabledBlocks == nil {
		tlb.prefetchDisabledBlocks = make(map[uint64]struct{})
	}
	if tlb.downstreamReqStates == nil {
		tlb.downstreamReqStates = make(map[string]downstreamReqState)
	}
}

func (tlb *GMMUTLB) resetPrefetchState() {
	tlb.initPrefetchState()
	tlb.prefetchQueue = nil
	clear(tlb.queuedPrefetches)
	clear(tlb.inflightPrefetches)
	clear(tlb.prefetchReqStates)
	clear(tlb.prefetchOutcomeByBlock)
	clear(tlb.prefetchedResident)
	clear(tlb.prefetchDisabledBlocks)
	clear(tlb.downstreamReqStates)
	tlb.prefetchCompletedCount = 0
	tlb.prefetchUsefulCount = 0
	tlb.prefetchLostCount = 0
	tlb.prefetchLateDemandCount = 0
	tlb.prefetchLateDemandQueuedCount = 0
	tlb.prefetchLateDemandInflightCount = 0
	tlb.prefetchRedundantFillCount = 0
	tlb.prefetchServedDemandCount = 0
	if tlb.prefetcher != nil {
		tlb.resetPrefetchLatencyWindows()
	}
}

func (tlb *GMMUTLB) maybeEnqueuePrefetches(
	now sim.VTimeInSec,
	req *vm.TranslationReq,
) {
	if tlb.prefetcher == nil ||
		!tlb.prefetcher.enabled ||
		req == nil ||
		req.IsPrefetch {
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

	learner := tlb.prefetcher.observe(
		page.PageBlock,
		tlb.DeviceID,
		tlb.ptclID(req.VAddr),
		int(tlb.Freq.Cycle(now)),
	)
	if learner == nil {
		return
	}

	afterClear := learner.hasClearPattern()
	afterNoClear := learner.hasNoClearPattern()
	patternParametersChanged := beforeClear && afterClear &&
		(beforeBaseGPM != learner.baseGPM ||
			beforeBaseMinPTCL != learner.baseMinPTCL ||
			beforeIntraStride != learner.intraStride ||
			beforeInterStride != learner.interStride)
	switch {
	case !beforeClear && afterClear:
		tlb.printPrefetcherGateEvent(now, page.PageBlock, "enable", "pattern-confirmed", learner)
	case !beforeNoClear && afterNoClear:
		tlb.printPrefetcherGateEvent(now, page.PageBlock, "disable", "no-clear-pattern", learner)
	case patternParametersChanged:
		tlb.printPrefetcherGateEvent(now, page.PageBlock, "update", "pattern-parameters-changed", learner)
	}

	if !afterClear {
		if afterNoClear {
			tlb.prefetcher.noClearPatternSkips++
		}
		return
	}

	budget := tlb.prefetchIssueBudget(page.PageBlock)
	if budget <= 0 {
		return
	}

	pending := tlb.pendingPrefetchesForBlock(page.PageBlock)
	if pending >= budget {
		return
	}
	budget -= pending

	lookahead := tlb.adaptivePrefetchLookahead(
		tlb.predictPrefetchRemotePath(),
		learner,
	)
	candidates := learner.predictAhead(
		tlb.DeviceID,
		tlb.ptclID(req.VAddr),
		lookahead,
		tlb.prefetcher.maxCandidatesPerReq,
	)

	selected := 0
	for _, candidate := range candidates {
		if selected >= budget || selected >= tlb.prefetcher.maxCandidatesPerReq {
			break
		}

		tlb.prefetcher.generatedCandidates++

		if !tlb.sharedFinePrefix(req.VAddr, tlb.ptclBaseVAddr(candidate.ptclID)) {
			tlb.prefetcher.droppedCandidates++
			tlb.prefetcher.rejectedByPrefix++
			continue
		}

		bitmap := tlb.prefetchBitmapForCandidate(req, candidate)
		reason := tlb.prefetchRejectReason(
			req.PID,
			candidate.targetGPM,
			candidate.ptclID,
			bitmap,
		)
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

		if tlb.prefetchDisabledByFeedback(page.PageBlock) {
			tlb.prefetcher.droppedCandidates++
			tlb.prefetcher.rejectedByFeedback++
			continue
		}

		if tlb.queuePrefetchRequest(req, page.PageBlock, candidate, bitmap) {
			tlb.prefetcher.enqueuedCandidates++
			selected++
			continue
		}

		tlb.prefetcher.droppedCandidates++
	}
}

func (tlb *GMMUTLB) prefetchRejectReason(
	pid vm.PID,
	targetGPM uint64,
	ptclID uint64,
	bitmap [8]bool,
) prefetchRejectReason {
	if targetGPM != tlb.DeviceID {
		return prefetchRejectInvalid
	}

	baseVAddr := tlb.ptclBaseVAddr(ptclID)
	bitmap = tlb.filterMappedBitmap(pid, baseVAddr, bitmap)
	if tlb.isBitmapZero(bitmap) {
		return prefetchRejectInvalid
	}

	resident := tlb.residentBitmap(pid, baseVAddr, bitmap)
	if tlb.isBitmapZero(tlb.subtractBitmaps(bitmap, resident)) {
		return prefetchRejectDuplicate
	}

	if tlb.mshr.GetEntry(pid, baseVAddr) != nil {
		return prefetchRejectDuplicate
	}

	if tlb.hasRespondingRequestForTarget(pid, baseVAddr) {
		return prefetchRejectDuplicate
	}

	if tlb.hasPendingPTELookupForTarget(pid, baseVAddr) {
		return prefetchRejectDuplicate
	}

	if tlb.hasQueuedPrefetchForTarget(pid, targetGPM, baseVAddr) {
		return prefetchRejectDuplicate
	}

	if tlb.hasInflightPrefetchForTarget(pid, targetGPM, baseVAddr) {
		return prefetchRejectDuplicate
	}

	return prefetchRejectNone
}

func (tlb *GMMUTLB) adaptivePrefetchLookahead(
	remotePath bool,
	learner *boPrefetchLearner,
) int {
	if tlb.prefetcher == nil {
		return 1
	}

	pace := tlb.prefetcher.demandLatencyCycles
	if learner != nil {
		pace = learner.streamInterval(pace)
	}
	if pace <= 0 {
		pace = 1
	}

	service := tlb.prefetcher.localPrefetchLatencyCycles
	if remotePath {
		service = tlb.prefetcher.remotePrefetchLatencyCycles
	}
	if service <= 0 {
		service = pace
	}

	lookahead := (service+pace-1)/pace + tlb.prefetcher.lookaheadMargin
	if remotePath {
		lookahead++
	}
	if lookahead < tlb.prefetcher.minLookahead {
		lookahead = tlb.prefetcher.minLookahead
	}
	if tlb.prefetcher.lookahead > 0 && lookahead > tlb.prefetcher.lookahead {
		lookahead = tlb.prefetcher.lookahead
	}

	tlb.prefetcher.currentLookahead = lookahead
	return lookahead
}

func (tlb *GMMUTLB) prefetchIssueBudget(pageBlock uint64) int {
	if tlb.prefetcher == nil {
		return 0
	}
	if tlb.prefetchDisabledByFeedback(pageBlock) {
		return 0
	}

	maxBudget := tlb.prefetcher.maxCandidatesPerReq
	if maxBudget <= 0 {
		maxBudget = 1
	}

	state := tlb.prefetchOutcomeState(pageBlock)
	if state.Completed < prefetchProbeCompletionThreshold {
		return minInt(1, maxBudget)
	}

	if state.Useful == 0 && state.Completed >= prefetchZeroUsefulCompletionThreshold {
		tlb.disablePrefetchByFeedback(pageBlock, "zero-useful-completed")
		return 0
	}

	if state.LostBeforeUse > 0 && state.Useful <= state.LostBeforeUse {
		return 0
	}

	if state.Useful >= 4+state.LostBeforeUse*4 {
		return maxBudget
	}

	if state.Useful > state.LostBeforeUse {
		return minInt(2, maxBudget)
	}

	return minInt(1, maxBudget)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}

	return b
}

func (tlb *GMMUTLB) pendingPrefetchesForBlock(pageBlock uint64) int {
	tlb.initPrefetchState()
	count := 0
	for _, entry := range tlb.prefetchQueue {
		if entry.pageBlock == pageBlock {
			count++
		}
	}
	for _, state := range tlb.prefetchReqStates {
		if state != nil && state.pageBlock == pageBlock {
			count++
		}
	}

	return count
}

func (tlb *GMMUTLB) predictPrefetchRemotePath() bool {
	if tlb.localPTWState != nil && tlb.localPTWState.HasFreePTW() {
		return false
	}

	return tlb.IOMMUPort != nil
}

func (tlb *GMMUTLB) prefetchBitmapForCandidate(
	demandReq *vm.TranslationReq,
	candidate prefetchCandidate,
) [8]bool {
	if demandReq == nil {
		return [8]bool{}
	}

	baseVAddr := tlb.ptclBaseVAddr(candidate.ptclID)
	bitmap := tlb.fullBitmap()
	if !tlb.ptclMode || tlb.vpnMSHRBaseline {
		bitmap = tlb.firstBitBitmap(tlb.normalizeBitmap(demandReq))
	}

	return tlb.filterMappedBitmap(demandReq.PID, baseVAddr, bitmap)
}

func (tlb *GMMUTLB) queuePrefetchRequest(
	demandReq *vm.TranslationReq,
	pageBlock uint64,
	candidate prefetchCandidate,
	bitmap [8]bool,
) bool {
	if candidate.targetGPM != tlb.DeviceID {
		return false
	}

	baseVAddr := tlb.ptclBaseVAddr(candidate.ptclID)
	bitmap = tlb.filterMappedBitmap(demandReq.PID, baseVAddr, bitmap)
	bitmap = tlb.subtractBitmaps(bitmap, tlb.residentBitmap(demandReq.PID, baseVAddr, bitmap))
	if tlb.isBitmapZero(bitmap) {
		return false
	}

	key := prefetchTargetKey{
		pid:       demandReq.PID,
		targetGPM: candidate.targetGPM,
		baseVAddr: baseVAddr,
	}

	tlb.initPrefetchState()
	if len(tlb.prefetchQueue) >= tlb.prefetchQueueLimit() {
		return false
	}

	tlb.queuedPrefetches[key] = struct{}{}
	tlb.prefetchQueue = append(tlb.prefetchQueue, prefetchQueueEntry{
		pid:        demandReq.PID,
		pageBlock:  pageBlock,
		candidate:  candidate,
		bitmap:     bitmap,
		startGPUID: demandReq.StartGPUID,
	})

	return true
}

func (tlb *GMMUTLB) prefetchQueueLimit() int {
	limit := tlb.numReqPerCycle * 8
	if limit < 8 {
		return 8
	}
	if limit > 64 {
		return 64
	}

	return limit
}

func (tlb *GMMUTLB) issueQueuedPrefetch(now sim.VTimeInSec) bool {
	if tlb.prefetcher == nil || !tlb.prefetcher.enabled {
		return false
	}
	if len(tlb.prefetchQueue) == 0 {
		return false
	}

	forceIOMMU := false
	if tlb.localPTWState == nil || !tlb.localPTWState.HasFreePTW() {
		tlb.prefetcher.blockedByNoFreePTW++
		if tlb.IOMMUPort == nil || !tlb.allowPrefetchIOMMUFallback(
			tlb.prefetchQueue[0].pageBlock,
		) {
			return false
		}
		forceIOMMU = true
	}

	entry := tlb.prefetchQueue[0]
	tlb.prefetchQueue = tlb.prefetchQueue[1:]

	baseVAddr := tlb.ptclBaseVAddr(entry.candidate.ptclID)
	delete(tlb.queuedPrefetches, prefetchTargetKey{
		pid:       entry.pid,
		targetGPM: entry.candidate.targetGPM,
		baseVAddr: baseVAddr,
	})

	reason := tlb.prefetchRejectReason(
		entry.pid,
		entry.candidate.targetGPM,
		entry.candidate.ptclID,
		entry.bitmap,
	)
	if reason != prefetchRejectNone {
		tlb.prefetcher.droppedCandidates++
		switch reason {
		case prefetchRejectDuplicate:
			tlb.prefetcher.rejectedByDuplicate++
		default:
			tlb.prefetcher.rejectedByInvalid++
		}
		return true
	}

	if tlb.prefetchDisabledByFeedback(entry.pageBlock) {
		tlb.prefetcher.droppedCandidates++
		tlb.prefetcher.rejectedByFeedback++
		return true
	}

	if tlb.issuePrefetchRequest(now, entry, forceIOMMU) {
		tlb.prefetcher.issuedCandidates++
		if forceIOMMU {
			tlb.prefetcher.iommuFallbackCandidates++
		}
		return true
	}

	tlb.prefetcher.droppedCandidates++
	return true
}

func (tlb *GMMUTLB) allowPrefetchIOMMUFallback(pageBlock uint64) bool {
	if tlb.prefetcher == nil {
		return false
	}

	if tlb.prefetcher.issuedCandidates < minPrefetchIssuesBeforeIOMMUFallback {
		return false
	}

	state := tlb.prefetchOutcomeState(pageBlock)
	if state.Completed < prefetchProbeCompletionThreshold {
		return false
	}

	return state.Useful > state.LostBeforeUse
}

func (tlb *GMMUTLB) issuePrefetchRequest(
	now sim.VTimeInSec,
	entry prefetchQueueEntry,
	forceIOMMU bool,
) bool {
	if entry.candidate.targetGPM != tlb.DeviceID {
		return false
	}

	baseVAddr := tlb.ptclBaseVAddr(entry.candidate.ptclID)
	bitmap := tlb.filterMappedBitmap(entry.pid, baseVAddr, entry.bitmap)
	bitmap = tlb.subtractBitmaps(bitmap, tlb.residentBitmap(entry.pid, baseVAddr, bitmap))
	if tlb.isBitmapZero(bitmap) {
		return false
	}

	responsePort := tlb.prefetchResponsePort(
		entry.pid,
		baseVAddr,
		bitmap,
		forceIOMMU,
	)
	if responsePort == nil {
		return false
	}

	prefetchReq := vm.TranslationReqBuilder{}.
		WithSendTime(now).
		WithSrc(responsePort).
		WithDst(responsePort).
		WithPID(entry.pid).
		WithVAddr(baseVAddr).
		WithDeviceID(tlb.DeviceID).
		WithTaskID(sim.GetIDGenerator().Generate()).
		WithOriginPort(responsePort).
		WithBitMap(bitmap).
		WithPrefetch(true).
		Build()
	prefetchReq.StartGPUID = entry.startGPUID

	var reqToBottom *vm.TranslationReq
	var ok bool
	if forceIOMMU {
		reqToBottom, ok = tlb.sendDownstreamToIOMMU(now, prefetchReq, bitmap)
	} else {
		reqToBottom, ok = tlb.sendDownstream(now, prefetchReq, bitmap)
	}
	if !ok || reqToBottom == nil {
		return false
	}

	tlb.registerInflightPrefetch(
		prefetchReq,
		reqToBottom,
		entry.pageBlock,
		entry.candidate.ptclID,
		now,
		responsePort == tlb.OutsidePort,
	)
	tracing.TraceReqReceive(prefetchReq, tlb)
	tracing.AddTaskStep(tracing.MsgIDAtReceiver(prefetchReq, tlb), tlb, "prefetch-miss")
	if reqToBottom != nil {
		tracing.TraceReqInitiate(reqToBottom, tlb, tracing.MsgIDAtReceiver(prefetchReq, tlb))
	}
	return true
}

func (tlb *GMMUTLB) prefetchResponsePort(
	pid vm.PID,
	vAddr uint64,
	bitmap [8]bool,
	forceIOMMU bool,
) sim.Port {
	if forceIOMMU {
		return tlb.OutsidePort
	}

	page, found := tlb.findFirstMappedPageInBitmap(pid, vAddr, bitmap)
	if !found {
		return nil
	}

	if page.DeviceID != tlb.DeviceID {
		return tlb.OutsidePort
	}

	return tlb.bottomPort
}

func (tlb *GMMUTLB) registerInflightPrefetch(
	req *vm.TranslationReq,
	reqToBottom *vm.TranslationReq,
	pageBlock uint64,
	targetPTCL uint64,
	now sim.VTimeInSec,
	remote bool,
) {
	if req == nil || reqToBottom == nil || !reqToBottom.IsPrefetch {
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

	tlb.initPrefetchState()
	tlb.inflightPrefetches[key] = struct{}{}
	tlb.prefetchReqStates[reqToBottom.ID] = &prefetchReqState{
		key:            key,
		req:            req,
		remainingPages: remainingPages,
		pageBlock:      pageBlock,
		targetPTCL:     targetPTCL,
		issueTime:      now,
		remote:         remote,
	}
	tlb.prefetchOutcomeState(pageBlock).Enqueued++
}

func (tlb *GMMUTLB) recordDownstreamIssue(
	now sim.VTimeInSec,
	req *vm.TranslationReq,
	remote bool,
) {
	if tlb.prefetcher == nil || req == nil {
		return
	}

	tlb.initPrefetchState()
	tlb.downstreamReqStates[req.ID] = downstreamReqState{
		issueTime: now,
		remote:    remote,
		prefetch:  req.IsPrefetch,
	}
}

func (tlb *GMMUTLB) resetPrefetchLatencyWindows() {
	if tlb.prefetcher == nil {
		return
	}

	tlb.prefetcher.demandLatencyWindow.Reset()
	tlb.prefetcher.localPrefetchLatencyWindow.Reset()
	tlb.prefetcher.remotePrefetchLatencyWindow.Reset()
	tlb.prefetcher.demandLatencyCycles =
		tlb.prefetcher.demandLatencyWindow.Average()
	tlb.prefetcher.localPrefetchLatencyCycles =
		tlb.prefetcher.localPrefetchLatencyWindow.Average()
	tlb.prefetcher.remotePrefetchLatencyCycles =
		tlb.prefetcher.remotePrefetchLatencyWindow.Average()
}

func (tlb *GMMUTLB) resetDemandTranslationLatencyState() {
	if tlb.demandTranslationStart == nil {
		tlb.demandTranslationStart = make(map[string]sim.VTimeInSec)
		return
	}

	clear(tlb.demandTranslationStart)
}

func (tlb *GMMUTLB) recordDemandTranslationStart(
	now sim.VTimeInSec,
	req *vm.TranslationReq,
) {
	if tlb.prefetcher == nil || req == nil || req.IsPrefetch {
		return
	}

	if tlb.demandTranslationStart == nil {
		tlb.demandTranslationStart = make(map[string]sim.VTimeInSec)
	}
	tlb.demandTranslationStart[req.ID] = now
}

func (tlb *GMMUTLB) observeDemandTranslationComplete(
	now sim.VTimeInSec,
	req *vm.TranslationReq,
) {
	if tlb.prefetcher == nil || req == nil || req.IsPrefetch {
		return
	}

	start, found := tlb.demandTranslationStart[req.ID]
	if !found {
		return
	}
	delete(tlb.demandTranslationStart, req.ID)

	elapsedCycles := int(tlb.Freq.Cycle(now - start))
	if elapsedCycles <= 0 {
		elapsedCycles = 1
	}

	tlb.prefetcher.demandLatencyCycles =
		tlb.prefetcher.demandLatencyWindow.Add(elapsedCycles)
}

func (tlb *GMMUTLB) observePrefetchTranslationComplete(
	elapsedCycles int,
	remote bool,
) {
	if tlb.prefetcher == nil {
		return
	}
	if elapsedCycles <= 0 {
		elapsedCycles = 1
	}

	if remote {
		tlb.prefetcher.remotePrefetchLatencyCycles =
			tlb.prefetcher.remotePrefetchLatencyWindow.Add(elapsedCycles)
		return
	}

	tlb.prefetcher.localPrefetchLatencyCycles =
		tlb.prefetcher.localPrefetchLatencyWindow.Add(
			elapsedCycles,
		)
}

func (tlb *GMMUTLB) observeDownstreamRsp(
	now sim.VTimeInSec,
	rsp *vm.TranslationRsp,
) {
	if tlb.prefetcher == nil || rsp == nil {
		return
	}

	tlb.initPrefetchState()
	state, found := tlb.downstreamReqStates[rsp.RespondTo]
	if !found {
		return
	}

	delete(tlb.downstreamReqStates, rsp.RespondTo)
	if state.prefetch {
		return
	}

	_ = state
}

func (tlb *GMMUTLB) handlePrefetchRsp(
	now sim.VTimeInSec,
	rsp *vm.TranslationRsp,
) bool {
	if rsp == nil || !rsp.IsPrefetch {
		return false
	}

	state := tlb.prefetchStateForRsp(rsp)
	installed := false
	if rsp.Page.Valid {
		if tlb.isPageResident(rsp.Page) {
			tlb.prefetchRedundantFillCount++
		}
		installed = tlb.installPage(rsp.Page)
	}
	if installed && state != nil {
		tlb.recordPrefetchInsert(rsp.Page, state.pageBlock)
	}
	servedDemand := tlb.satisfyDemandWithPrefetch(now, rsp.Page)
	if servedDemand {
		tlb.prefetchServedDemandCount++
	}
	if servedDemand && installed {
		tlb.observePrefetchUsefulHit(rsp.Page)
	}

	tlb.completeInflightPrefetchRsp(now, rsp)
	return true
}

func (tlb *GMMUTLB) satisfyDemandWithPrefetch(
	now sim.VTimeInSec,
	page vm.Page,
) bool {
	if !page.Valid {
		return false
	}

	mshrEntry := tlb.mshr.GetEntry(page.PID, page.VAddr)
	if mshrEntry == nil {
		return false
	}

	tlb.mshr.UpdatePage(page.PID, page.VAddr, page)
	tlb.mshr.UpdateResponseBitMap(page.PID, page.VAddr)
	tlb.scheduleReadyMSHREntry(now, mshrEntry, true)
	return true
}

func (tlb *GMMUTLB) prefetchStateForRsp(
	rsp *vm.TranslationRsp,
) *prefetchReqState {
	if rsp == nil || !rsp.IsPrefetch {
		return nil
	}

	tlb.initPrefetchState()
	return tlb.prefetchReqStates[rsp.RespondTo]
}

func (tlb *GMMUTLB) completeInflightPrefetchRsp(
	now sim.VTimeInSec,
	rsp *vm.TranslationRsp,
) {
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
	tlb.prefetchCompletedCount++
	tlb.prefetchOutcomeState(state.pageBlock).Completed++

	elapsedCycles := int(tlb.Freq.Cycle(now - state.issueTime))
	tlb.observePrefetchTranslationComplete(elapsedCycles, state.remote)

	if state.req != nil {
		tracing.TraceReqComplete(state.req, tlb)
	}

	_ = now
}

func (tlb *GMMUTLB) prefetchOutcomeState(pageBlock uint64) *prefetchOutcomeCounts {
	tlb.initPrefetchState()
	state, found := tlb.prefetchOutcomeByBlock[pageBlock]
	if !found {
		state = &prefetchOutcomeCounts{}
		tlb.prefetchOutcomeByBlock[pageBlock] = state
	}

	return state
}

func (tlb *GMMUTLB) recordDemandAgainstPendingPrefetch(
	req *vm.TranslationReq,
) {
	if tlb.prefetcher == nil || req == nil || req.IsPrefetch {
		return
	}

	tlb.initPrefetchState()
	baseVAddr := tlb.getBaseVaddr(req.VAddr)
	key := prefetchTargetKey{
		pid:       req.PID,
		targetGPM: tlb.DeviceID,
		baseVAddr: baseVAddr,
	}

	queued := false
	if _, found := tlb.queuedPrefetches[key]; found {
		queued = true
		tlb.prefetchLateDemandQueuedCount++
	}

	inflight := false
	if _, found := tlb.inflightPrefetches[key]; found {
		inflight = true
		tlb.prefetchLateDemandInflightCount++
	}

	if queued || inflight {
		tlb.prefetchLateDemandCount++
	}
}

func (tlb *GMMUTLB) prefetchedEntryKey(page vm.Page) prefetchedResidentKey {
	return prefetchedResidentKey{
		pid:   page.PID,
		vAddr: page.VAddr,
	}
}

func (tlb *GMMUTLB) isPageResident(page vm.Page) bool {
	if !page.Valid {
		return false
	}

	setID := tlb.vAddrToSetIDForPID(page.PID, page.VAddr)
	_, foundPage, found := tlb.Sets[setID].Lookup(page.PID, page.VAddr)
	return found && foundPage.Valid
}

func (tlb *GMMUTLB) recordPrefetchInsert(page vm.Page, pageBlock uint64) {
	if !page.Valid {
		return
	}

	tlb.initPrefetchState()
	tlb.prefetchedResident[tlb.prefetchedEntryKey(page)] = pageBlock
}

func (tlb *GMMUTLB) observePrefetchUsefulHit(page vm.Page) {
	if !page.Valid {
		return
	}

	tlb.initPrefetchState()
	key := tlb.prefetchedEntryKey(page)
	pageBlock, found := tlb.prefetchedResident[key]
	if !found {
		return
	}

	delete(tlb.prefetchedResident, key)
	tlb.prefetchUsefulCount++
	tlb.prefetchOutcomeState(pageBlock).Useful++
}

func (tlb *GMMUTLB) recordPrefetchEviction(page vm.Page) {
	if !page.Valid {
		return
	}

	tlb.initPrefetchState()
	key := tlb.prefetchedEntryKey(page)
	pageBlock, found := tlb.prefetchedResident[key]
	if !found {
		return
	}

	delete(tlb.prefetchedResident, key)
	tlb.prefetchLostCount++
	tlb.prefetchOutcomeState(pageBlock).LostBeforeUse++
	tlb.disablePrefetchByFeedback(pageBlock, "lost-before-use")
}

func (tlb *GMMUTLB) disablePrefetchByFeedback(pageBlock uint64, reason string) {
	if tlb.prefetcher == nil {
		return
	}
	if _, disabled := tlb.prefetchDisabledBlocks[pageBlock]; disabled {
		return
	}
	if reason == "" {
		reason = "feedback"
	}

	tlb.prefetchDisabledBlocks[pageBlock] = struct{}{}
	tlb.dropQueuedPrefetchesByBlock(pageBlock)
	fmt.Printf("[GMMU-PF][feedback] component=%s page_block=%d action=disable reason=%s lost_before_use=%d useful=%d\n",
		tlb.Name(), pageBlock, reason, tlb.prefetchLostCount, tlb.prefetchUsefulCount)
}

func (tlb *GMMUTLB) dropQueuedPrefetchesByBlock(pageBlock uint64) {
	if tlb.prefetcher == nil || len(tlb.prefetchQueue) == 0 {
		return
	}

	kept := tlb.prefetchQueue[:0]
	dropped := 0
	for _, entry := range tlb.prefetchQueue {
		if entry.pageBlock != pageBlock {
			kept = append(kept, entry)
			continue
		}

		dropped++
		delete(tlb.queuedPrefetches, prefetchTargetKey{
			pid:       entry.pid,
			targetGPM: entry.candidate.targetGPM,
			baseVAddr: tlb.ptclBaseVAddr(entry.candidate.ptclID),
		})
	}

	tlb.prefetchQueue = kept
	tlb.prefetcher.droppedCandidates += dropped
	tlb.prefetcher.rejectedByFeedback += dropped
}

func (tlb *GMMUTLB) prefetchDisabledByFeedback(pageBlock uint64) bool {
	tlb.initPrefetchState()
	_, disabled := tlb.prefetchDisabledBlocks[pageBlock]
	return disabled
}

func (tlb *GMMUTLB) lookupRequestPage(req *vm.TranslationReq) (vm.Page, bool) {
	if req == nil {
		return vm.Page{}, false
	}

	if tlb.isBitmapZero(req.BitMap) {
		return tlb.pageTable.Find(req.PID, req.VAddr)
	}

	baseVAddr := tlb.getBaseVaddr(req.VAddr)
	for i := 0; i < 8; i++ {
		if !req.BitMap[i] {
			continue
		}

		pageVAddr := baseVAddr + (uint64(i) << tlb.log2Pagesize)
		page, found := tlb.pageTable.Find(req.PID, pageVAddr)
		if found {
			return page, true
		}
	}

	return vm.Page{}, false
}

func (tlb *GMMUTLB) ptclBaseVAddr(ptclID uint64) uint64 {
	return ptclID << (tlb.log2Pagesize + 3)
}

func (tlb *GMMUTLB) residentBitmap(
	pid vm.PID,
	vAddr uint64,
	bitmap [8]bool,
) [8]bool {
	baseVAddr := tlb.getBaseVaddr(vAddr)
	resident := [8]bool{}

	for i := 0; i < 8; i++ {
		if !bitmap[i] {
			continue
		}

		pageVAddr := baseVAddr + (uint64(i) << tlb.log2Pagesize)
		setID := tlb.vAddrToSetIDForPID(pid, pageVAddr)
		_, page, found := tlb.Sets[setID].Lookup(pid, pageVAddr)
		if found && page.Valid {
			resident[i] = true
		}
	}

	return resident
}

func (tlb *GMMUTLB) hasRespondingRequestForTarget(
	pid vm.PID,
	baseVAddr uint64,
) bool {
	key := tlb.pteLookupGroupKey(pid, baseVAddr)
	for _, entry := range tlb.respondingMSHREntry {
		if entry.pid == key.pid && entry.baseVAddr == key.baseVAddr {
			return true
		}
	}

	return false
}

func (tlb *GMMUTLB) hasPendingPTELookupForTarget(
	pid vm.PID,
	baseVAddr uint64,
) bool {
	key := tlb.pteLookupGroupKey(pid, baseVAddr)
	if _, found := tlb.pteLookupGroups[key]; found {
		return true
	}
	if _, found := tlb.ptclRepresentativeMiss[key]; found {
		return true
	}

	return false
}

func (tlb *GMMUTLB) hasQueuedPrefetchForTarget(
	pid vm.PID,
	targetGPM uint64,
	baseVAddr uint64,
) bool {
	_, found := tlb.queuedPrefetches[prefetchTargetKey{
		pid:       pid,
		targetGPM: targetGPM,
		baseVAddr: baseVAddr,
	}]
	return found
}

func (tlb *GMMUTLB) hasInflightPrefetchForTarget(
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

func (tlb *GMMUTLB) sharedFinePrefix(vAddr1, vAddr2 uint64) bool {
	vpn1 := vAddr1 >> tlb.log2Pagesize
	vpn2 := vAddr2 >> tlb.log2Pagesize
	return (vpn1 >> 10) == (vpn2 >> 10)
}

func (tlb *GMMUTLB) printPrefetcherGateEvent(
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

	fmt.Printf("[GMMU-PF][gate] cycle=%d component=%s page_block=%d action=%s reason=%s confirmed=%t state=%d base_gpm=%d base_min_ptcl=%d intra_stride=%d inter_stride=%d footprint=%d\n",
		uint64(now*1e9), tlb.Name(), pageBlock, action, reason, confirmed, state,
		baseGPM, baseMinPTCL, intraStride, interStride, footprint)
}
