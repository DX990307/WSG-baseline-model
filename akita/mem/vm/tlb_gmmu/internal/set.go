// Package internal provides the definition required for defining TLB.
package internal

import (
	"fmt"
	"sort"

	"github.com/sarchlab/akita/v3/mem/vm"
)

// A Set holds a certain number of pages.
type Set interface {
	Lookup(pid vm.PID, vAddr uint64) (wayID int, page vm.Page, found bool)
	Peek(wayID int) (page vm.Page, ok bool)
	Update(wayID int, page vm.Page)
	Evict() (wayID int, ok bool, page vm.Page)
	EvictInvalid() (wayID int, ok bool, page vm.Page)
	Visit(wayID int)
}

// PTCLLookupResult is the bitmap-level result of a PTCL-aware set lookup.
type PTCLLookupResult struct {
	HitBitmap  [8]bool
	MissBitmap [8]bool
	Pages      [8]vm.Page
	LineHit    bool
}

// PTCLFillResult reports what happened when filling a PTCL bitmap response.
type PTCLFillResult struct {
	EvictedPages    []vm.Page
	Installed       bool
	ConflictEvicted bool
}

// PTCLSet is an optional extension implemented by the default set implementation.
// It keeps the public Set interface stable for existing code.
type PTCLSet interface {
	LookupPTCL(
		pid vm.PID,
		baseVAddr uint64,
		requestBitmap [8]bool,
		pageSize uint64,
	) PTCLLookupResult
	FillPTCL(
		pid vm.PID,
		baseVAddr uint64,
		pages [8]vm.Page,
		responseBitmap [8]bool,
		pageSize uint64,
	) PTCLFillResult
	Invalidate(pid vm.PID, vAddr uint64, pageSize uint64)
	Clear()
	PTCLLineValid() bool
	PTCLValidBitCount() int
	ValidPageCount() int
}

type setMode int

const (
	pteSetMode setMode = iota
	ptclSetMode
)

// NewSet creates a new TLB set.
func NewSet(numWays int) Set {
	s := &setImpl{}
	s.blocks = make([]*block, numWays)
	s.visitList = make([]*block, 0, numWays)
	s.vAddrWayIDMap = make(map[string]int)
	for i := range s.blocks {
		b := &block{}
		s.blocks[i] = b
		b.wayID = i
		s.Visit(i)
	}
	return s
}

type block struct {
	page      vm.Page
	wayID     int
	lastVisit uint64
	local     bool
}

func (b *block) Less(anotherBlock *block) bool {
	return b.lastVisit < anotherBlock.lastVisit
}

type setImpl struct {
	blocks        []*block
	vAddrWayIDMap map[string]int
	visitList     []*block
	visitCount    uint64

	mode          setMode
	ptclValid     bool
	ptclPID       vm.PID
	ptclBaseVAddr uint64
	ptclBitmap    [8]bool
}

func (s *setImpl) keyString(pid vm.PID, vAddr uint64) string {
	return fmt.Sprintf("%d%016x", pid, vAddr)
}

func (s *setImpl) Lookup(pid vm.PID, vAddr uint64) (
	wayID int,
	page vm.Page,
	found bool,
) {
	key := s.keyString(pid, vAddr)
	wayID, ok := s.vAddrWayIDMap[key]
	if !ok {
		return 0, vm.Page{}, false
	}

	block := s.blocks[wayID]

	return block.wayID, block.page, true
}

func (s *setImpl) Peek(wayID int) (page vm.Page, ok bool) {
	if wayID < 0 || wayID >= len(s.blocks) {
		return vm.Page{}, false
	}

	return s.blocks[wayID].page, true
}

func (s *setImpl) Update(wayID int, page vm.Page) {
	s.clearPTCLMetadata()

	block := s.blocks[wayID]
	key := s.keyString(block.page.PID, block.page.VAddr)
	delete(s.vAddrWayIDMap, key)

	block.page = page
	if page.Valid {
		key = s.keyString(page.PID, page.VAddr)
		s.vAddrWayIDMap[key] = wayID
	}
}

func (s *setImpl) Evict() (wayID int, ok bool, page vm.Page) {
	if s.mode == ptclSetMode {
		s.Clear()
	}

	if s.hasNothingToEvict() {
		return 0, false, vm.Page{}
	}

	// wayID = s.visitTree.DeleteMin().(*block).wayID
	leastVisited := s.visitList[0]
	wayID = leastVisited.wayID
	s.visitList = s.visitList[1:]
	return wayID, true, s.blocks[wayID].page
}

func (s *setImpl) EvictInvalid() (wayID int, ok bool, page vm.Page) {
	for _, block := range s.blocks {
		if block.page.Valid {
			continue
		}

		s.removeFromVisitList(block.wayID)
		return block.wayID, true, block.page
	}

	return 0, false, vm.Page{}
}

func (s *setImpl) InvalidWayCount() int {
	count := 0
	for _, block := range s.blocks {
		if !block.page.Valid {
			count++
		}
	}
	return count
}

func (s *setImpl) ValidPageCount() int {
	count := 0
	for _, block := range s.blocks {
		if block.page.Valid {
			count++
		}
	}
	return count
}

func (s *setImpl) Visit(wayID int) {
	block := s.blocks[wayID]

	s.removeFromVisitList(wayID)

	s.visitCount++
	block.lastVisit = s.visitCount

	index := sort.Search(len(s.visitList), func(i int) bool {
		return s.visitList[i].lastVisit > block.lastVisit
	})

	s.visitList = append(s.visitList, nil)
	copy(s.visitList[index+1:], s.visitList[index:])
	s.visitList[index] = block
}

func (s *setImpl) hasNothingToEvict() bool {
	return len(s.visitList) == 0
}

func (s *setImpl) removeFromVisitList(wayID int) {
	for i, b := range s.visitList {
		if b.wayID == wayID {
			s.visitList = append(s.visitList[:i], s.visitList[i+1:]...)
			return
		}
	}
}

func (s *setImpl) LookupPTCL(
	pid vm.PID,
	baseVAddr uint64,
	requestBitmap [8]bool,
	pageSize uint64,
) PTCLLookupResult {
	result := PTCLLookupResult{}
	result.MissBitmap = requestBitmap

	for _, block := range s.blocks {
		page := block.page
		if !page.Valid || page.PID != pid {
			continue
		}

		if page.VAddr < baseVAddr || page.VAddr >= baseVAddr+8*pageSize {
			continue
		}

		bit := int((page.VAddr - baseVAddr) / pageSize)
		if bit < 0 || bit >= 8 || !requestBitmap[bit] {
			continue
		}

		result.HitBitmap[bit] = true
		result.MissBitmap[bit] = false
		result.Pages[bit] = page
		s.Visit(block.wayID)
	}

	return result
}

func (s *setImpl) FillPTCL(
	pid vm.PID,
	baseVAddr uint64,
	pages [8]vm.Page,
	responseBitmap [8]bool,
	pageSize uint64,
) PTCLFillResult {
	result := PTCLFillResult{}
	if s.bitmapZero(responseBitmap) {
		return result
	}

	for bit := 0; bit < 8; bit++ {
		if !responseBitmap[bit] || !pages[bit].Valid {
			continue
		}

		expectedVAddr := baseVAddr + uint64(bit)*pageSize
		page := pages[bit]
		page.PID = pid
		page.VAddr = expectedVAddr

		wayID, existingPage, found := s.Lookup(pid, expectedVAddr)
		if found {
			oldKey := s.keyString(existingPage.PID, existingPage.VAddr)
			delete(s.vAddrWayIDMap, oldKey)
			s.blocks[wayID].page = page
			s.vAddrWayIDMap[s.keyString(page.PID, page.VAddr)] = wayID
			s.Visit(wayID)
			result.Installed = true
			continue
		}

		wayID, ok, evictedPage := s.EvictInvalid()
		if !ok {
			wayID, ok, evictedPage = s.Evict()
			if !ok {
				continue
			}
		}

		if evictedPage.Valid {
			result.EvictedPages = append(result.EvictedPages, evictedPage)
			result.ConflictEvicted = true
			delete(s.vAddrWayIDMap, s.keyString(evictedPage.PID, evictedPage.VAddr))
		}

		s.blocks[wayID].page = page
		s.vAddrWayIDMap[s.keyString(page.PID, page.VAddr)] = wayID
		s.Visit(wayID)
		result.Installed = true
	}

	return result
}

func (s *setImpl) Invalidate(pid vm.PID, vAddr uint64, pageSize uint64) {
	if s.mode == ptclSetMode {
		baseVAddr := vAddr / (8 * pageSize) * (8 * pageSize)
		bit := int((vAddr - baseVAddr) / pageSize)
		if s.samePTCLLine(pid, baseVAddr) && bit >= 0 && bit < 8 && bit < len(s.blocks) {
			block := s.blocks[bit]
			key := s.keyString(block.page.PID, block.page.VAddr)
			delete(s.vAddrWayIDMap, key)
			block.page = vm.Page{}
			s.ptclBitmap[bit] = false
			if s.bitmapZero(s.ptclBitmap) {
				s.Clear()
			}
		}
		return
	}

	key := s.keyString(pid, vAddr)
	wayID, ok := s.vAddrWayIDMap[key]
	if !ok {
		return
	}

	delete(s.vAddrWayIDMap, key)
	s.blocks[wayID].page = vm.Page{}
}

func (s *setImpl) Clear() {
	s.vAddrWayIDMap = make(map[string]int)
	s.visitList = s.visitList[:0]
	for i, block := range s.blocks {
		block.page = vm.Page{}
		block.local = false
		s.Visit(i)
	}
	s.clearPTCLMetadata()
}

func (s *setImpl) PTCLLineValid() bool {
	return s.mode == ptclSetMode && s.ptclValid
}

func (s *setImpl) PTCLValidBitCount() int {
	return s.bitmapCount(s.ptclBitmap)
}

func (s *setImpl) clearPTCLMetadata() {
	s.mode = pteSetMode
	s.ptclValid = false
	s.ptclPID = 0
	s.ptclBaseVAddr = 0
	s.ptclBitmap = [8]bool{}
}

func (s *setImpl) samePTCLLine(pid vm.PID, baseVAddr uint64) bool {
	return s.mode == ptclSetMode &&
		s.ptclValid &&
		s.ptclPID == pid &&
		s.ptclBaseVAddr == baseVAddr
}

func (s *setImpl) hasValidPage() bool {
	for _, block := range s.blocks {
		if block.page.Valid {
			return true
		}
	}
	return false
}

func (s *setImpl) bitmapZero(bitmap [8]bool) bool {
	for _, bit := range bitmap {
		if bit {
			return false
		}
	}
	return true
}

func (s *setImpl) bitmapCount(bitmap [8]bool) int {
	count := 0
	for _, bit := range bitmap {
		if bit {
			count++
		}
	}
	return count
}
