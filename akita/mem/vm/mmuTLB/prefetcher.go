package mmuTLB

import "sort"

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

type prefetchCandidate struct {
	targetGPM uint64
	ptclID    uint64
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
}

type translationPrefetcher struct {
	enabled                bool
	promoteDemandToPTCL    bool
	admissionThreshold     int
	maxLearners            int
	lookahead              int
	maxCandidatesPerReq    int
	coldBOs                map[uint64]*coldObservationState
	learners               map[uint64]*boPrefetchLearner
	generatedCandidates    int
	enqueuedCandidates     int
	droppedCandidates      int
	rejectedByPrefix       int
	rejectedByDuplicate    int
	rejectedByInvalid      int
	noClearPatternSkips    int
	admittedLearnersCount  int
	promotedDemandRequests int
}

func newTranslationPrefetcher(
	enabled bool,
	promoteDemandToPTCL bool,
	admissionThreshold int,
	maxLearners int,
	lookahead int,
	maxCandidatesPerReq int,
) *translationPrefetcher {
	if admissionThreshold <= 0 {
		admissionThreshold = 6
	}
	if maxLearners <= 0 {
		maxLearners = 4
	}
	if lookahead <= 0 {
		lookahead = 2
	}
	if maxCandidatesPerReq <= 0 {
		maxCandidatesPerReq = 4
	}

	return &translationPrefetcher{
		enabled:             enabled,
		promoteDemandToPTCL: promoteDemandToPTCL,
		admissionThreshold:  admissionThreshold,
		maxLearners:         maxLearners,
		lookahead:           lookahead,
		maxCandidatesPerReq: maxCandidatesPerReq,
		coldBOs:             make(map[uint64]*coldObservationState),
		learners:            make(map[uint64]*boPrefetchLearner),
	}
}

func (p *translationPrefetcher) observe(boID, gpmID, ptclID uint64) *boPrefetchLearner {
	if !p.enabled {
		return nil
	}

	if learner, found := p.learners[boID]; found {
		learner.observe(gpmID, ptclID)
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
		learner.observe(buffered.gpmID, buffered.ptclID)
	}
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
		boID:        boID,
		perGPMPTCLs: make(map[uint64]map[uint64]struct{}),
		uniquePTCLs: make(map[uint64]struct{}),
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

func (l *boPrefetchLearner) observe(gpmID, ptclID uint64) {
	row, found := l.perGPMPTCLs[gpmID]
	if !found {
		row = make(map[uint64]struct{})
		l.perGPMPTCLs[gpmID] = row
	}

	row[ptclID] = struct{}{}
	l.uniquePTCLs[ptclID] = struct{}{}
	l.refreshPattern()
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
