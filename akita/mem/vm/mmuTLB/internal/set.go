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
	Update(wayID int, page vm.Page)
	Evict() (wayID int, ok bool)
	Visit(wayID int)
}

type PTCLLookupResult struct {
	HitBitmap  [8]bool
	MissBitmap [8]bool
	Pages      [8]vm.Page
	LineHit    bool
}

type PTCLFillResult struct {
	Installed       bool
	ConflictEvicted bool
}

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
	Clear()
	PTCLLineValid() bool
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

func (s *setImpl) Evict() (wayID int, ok bool) {
	if s.mode == ptclSetMode {
		s.Clear()
	}

	if s.hasNothingToEvict() {
		return 0, false
	}

	// wayID = s.visitTree.DeleteMin().(*block).wayID
	leastVisited := s.visitList[0]
	wayID = leastVisited.wayID
	s.visitList = s.visitList[1:]
	return wayID, true
}

func (s *setImpl) Visit(wayID int) {
	block := s.blocks[wayID]

	for i, b := range s.visitList {
		if b.wayID == wayID {
			s.visitList = append(s.visitList[:i], s.visitList[i+1:]...)
		}
	}

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

func (s *setImpl) LookupPTCL(
	pid vm.PID,
	baseVAddr uint64,
	requestBitmap [8]bool,
	pageSize uint64,
) PTCLLookupResult {
	result := PTCLLookupResult{MissBitmap: requestBitmap}
	if s.mode != ptclSetMode ||
		!s.ptclValid ||
		s.ptclPID != pid ||
		s.ptclBaseVAddr != baseVAddr {
		return result
	}

	result.LineHit = true
	for bit := 0; bit < 8 && bit < len(s.blocks); bit++ {
		if !requestBitmap[bit] {
			result.MissBitmap[bit] = false
			continue
		}

		block := s.blocks[bit]
		if s.ptclBitmap[bit] && block.page.Valid {
			result.HitBitmap[bit] = true
			result.MissBitmap[bit] = false
			result.Pages[bit] = block.page
		}
	}

	_ = pageSize
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

	if !s.samePTCLLine(pid, baseVAddr) {
		result.ConflictEvicted = s.hasValidPage()
		s.Clear()
		s.mode = ptclSetMode
		s.ptclValid = true
		s.ptclPID = pid
		s.ptclBaseVAddr = baseVAddr
	}

	for bit := 0; bit < 8 && bit < len(s.blocks); bit++ {
		if !responseBitmap[bit] || !pages[bit].Valid {
			continue
		}

		block := s.blocks[bit]
		oldKey := s.keyString(block.page.PID, block.page.VAddr)
		delete(s.vAddrWayIDMap, oldKey)

		page := pages[bit]
		page.PID = pid
		page.VAddr = baseVAddr + uint64(bit)*pageSize
		block.page = page
		s.vAddrWayIDMap[s.keyString(page.PID, page.VAddr)] = bit
		s.ptclBitmap[bit] = true
		s.Visit(bit)
		result.Installed = true
	}

	return result
}

func (s *setImpl) Clear() {
	s.vAddrWayIDMap = make(map[string]int)
	s.visitList = s.visitList[:0]
	for i, block := range s.blocks {
		block.page = vm.Page{}
		s.Visit(i)
	}
	s.clearPTCLMetadata()
}

func (s *setImpl) PTCLLineValid() bool {
	return s.mode == ptclSetMode && s.ptclValid
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
