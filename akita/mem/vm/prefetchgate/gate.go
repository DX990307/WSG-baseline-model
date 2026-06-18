package prefetchgate

import "sync"

type Key struct {
	PageBlock uint64
	TargetGPM uint64
}

type Counters struct {
	UsefulHit  int
	LateUseful int
	Unused     int
	Disabled   bool
}

var (
	mu       sync.Mutex
	counters = make(map[Key]*Counters)
)

func RecordUsefulHit(pageBlock, targetGPM uint64) {
	mu.Lock()
	defer mu.Unlock()

	key := Key{PageBlock: pageBlock, TargetGPM: targetGPM}
	counter := counterForKey(key)
	counter.UsefulHit++
	maybeDisable(counter)
}

func RecordLateUseful(pageBlock, targetGPM uint64) {
	mu.Lock()
	defer mu.Unlock()

	key := Key{PageBlock: pageBlock, TargetGPM: targetGPM}
	counter := counterForKey(key)
	counter.LateUseful++
	maybeDisable(counter)
}

func RecordUnused(pageBlock, targetGPM uint64) {
	mu.Lock()
	defer mu.Unlock()

	key := Key{PageBlock: pageBlock, TargetGPM: targetGPM}
	counter := counterForKey(key)
	counter.Unused++
	maybeDisable(counter)
}

func Disabled(pageBlock, targetGPM uint64) bool {
	mu.Lock()
	defer mu.Unlock()

	key := Key{PageBlock: pageBlock, TargetGPM: targetGPM}
	counter, found := counters[key]
	if !found {
		return false
	}

	return counter.Disabled
}

func Snapshot(pageBlock, targetGPM uint64) Counters {
	mu.Lock()
	defer mu.Unlock()

	key := Key{PageBlock: pageBlock, TargetGPM: targetGPM}
	counter, found := counters[key]
	if !found {
		return Counters{}
	}

	return *counter
}

func counterForKey(key Key) *Counters {
	counter, found := counters[key]
	if !found {
		counter = &Counters{}
		counters[key] = counter
	}

	return counter
}

func maybeDisable(counter *Counters) {
	if counter == nil || counter.Disabled {
		return
	}

	resolved := counter.UsefulHit + counter.LateUseful + counter.Unused
	if resolved < 2 {
		return
	}

	if counter.LateUseful+counter.Unused > counter.UsefulHit {
		counter.Disabled = true
	}
}
