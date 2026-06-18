package tlb_gmmu

import (
	"fmt"
	"log"

	"github.com/sarchlab/akita/v3/mem/vm"
	"github.com/sarchlab/akita/v3/sim"
)

type mshrEntry struct {
	pid            vm.PID
	baseVAddr      uint64
	UplevelBitMap  [8]bool
	IssuedBitMap   [8]bool
	ResponseBitMap [8]bool
	Requests       []*vm.TranslationReq
	reqToBottom    *vm.TranslationReq
	Pages          [8]vm.Page
	RealAddrBitmap [8]bool
	RealAddrTime   [8]sim.VTimeInSec
	// startPage     [8]int
}

// newMSHREntry returns a new MSHR entry object
func newMSHREntry() *mshrEntry {
	e := new(mshrEntry)
	return e
}

// IsReady 检查当前 mshrEntry 的页面 VPN 是否连续
func (e *mshrEntry) IsReady() bool {
	for i := 0; i < 8; i++ {
		if e.UplevelBitMap[i] && !e.ResponseBitMap[i] {
			return false // 如果 UplevelBitMap[i] 为 true，但 ResponseBitMap[i] 为 false，则页面不连续
		}
	}

	return true // 所有检查通过，页面连续且有效
}

// mshr is an interface that controls MSHR entries
type mshr interface {
	Add(pid vm.PID, addr uint64, now sim.VTimeInSec, predictRadius int) *mshrEntry
	Remove(pid vm.PID, addr uint64) *mshrEntry
	IsFull() bool
	Occupancy() (entries int, capacity int)
	IsEntryFull(pid vm.PID, vAddr uint64) bool
	Reset()
	GetEntry(pid vm.PID, vAddr uint64) *mshrEntry
	IsEntryPresent(pid vm.PID, vAddr uint64) bool
	UpdateUpLevelBitMap(pid vm.PID, vAddr uint64, now sim.VTimeInSec, predictRadius int) bool
	UpdatePage(pid vm.PID, vAddr uint64, page vm.Page) bool
	UpdateResponseBitMap(pid vm.PID, vAddr uint64) bool
}

type mshrImpl struct {
	capacity     int
	entryDepth   int
	log2PageSize uint64
	perVPNMSHR   bool
	entries      []*mshrEntry
}

// newMSHR returns a new mshr object
func newMSHR(
	capacity int,
	entryDepth int,
	log2PageSize uint64,
	perVPNMSHR bool,
) mshr {
	m := new(mshrImpl)
	m.capacity = capacity
	m.entryDepth = entryDepth
	m.log2PageSize = log2PageSize
	m.perVPNMSHR = perVPNMSHR

	return m
}

func (m *mshrImpl) Add(pid vm.PID, vAddr uint64, now sim.VTimeInSec, predictRadius int) *mshrEntry {
	if entry, _ := m.findEntry(pid, vAddr); entry != nil {
		m.UpdateUpLevelBitMap(pid, vAddr, now, predictRadius)
		return entry
	}

	if len(m.entries) >= m.capacity {
		log.Panic("MSHR is full")
	}

	BaseVaddr := m.getEntryVAddr(vAddr)
	VPN := vAddr >> m.log2PageSize
	entry := newMSHREntry()
	entry.pid = pid
	entry.baseVAddr = BaseVaddr
	bitMap := [8]bool{}
	RealAddrBitmap := [8]bool{}

	bitMap[VPN%8] = true
	RealAddrBitmap[VPN%8] = true

	entry.UplevelBitMap = bitMap
	entry.RealAddrBitmap = RealAddrBitmap
	entry.RealAddrTime[VPN%8] = now

	m.entries = append(m.entries, entry)
	return entry
}

func (m *mshrImpl) Remove(pid vm.PID, vAddr uint64) *mshrEntry {
	entry, index := m.findEntry(pid, vAddr)
	if entry == nil {
		panic("trying to remove an non-exist entry")
	}

	m.entries = append(m.entries[:index], m.entries[index+1:]...)
	return entry
}

func (m *mshrImpl) AllEntries() []*mshrEntry {
	return m.entries
}

func (m *mshrImpl) IsFull() bool {
	return len(m.entries) >= m.capacity
}

func (m *mshrImpl) Occupancy() (entries int, capacity int) {
	return len(m.entries), m.capacity
}

func (m *mshrImpl) Reset() {
	m.entries = nil
}

func (m *mshrImpl) GetEntry(pid vm.PID, vAddr uint64) *mshrEntry {
	entry, _ := m.findEntry(pid, vAddr)
	return entry
}

func (m *mshrImpl) IsEntryPresent(pid vm.PID, vAddr uint64) bool {
	entry, _ := m.findEntry(pid, vAddr)
	return entry != nil
}

func (m *mshrImpl) IsEntryFull(pid vm.PID, vAddr uint64) bool {
	entry, _ := m.findEntry(pid, vAddr)
	return entry != nil && len(entry.Requests) >= m.entryDepth
}

func (m *mshrImpl) PrintStats() (uint64, uint64, uint64) {
	var numEntries uint64
	var numReqs uint64
	var maxNumReqsPerEntry uint64
	for _, e := range m.entries {
		numEntries++
		numReqs += uint64(len(e.Requests))
		if uint64(len(e.Requests)) > maxNumReqsPerEntry {
			maxNumReqsPerEntry = uint64(len(e.Requests))
		}
	}
	return numEntries, numReqs, maxNumReqsPerEntry
}

func (m *mshrImpl) getBaseVaddr(vAddr uint64) uint64 {
	VPN := vAddr >> m.log2PageSize
	BaseVAddr := (VPN >> 3) << 3 // Clear the lower 3 bits to get the base VPN
	return BaseVAddr << m.log2PageSize
}

func (m *mshrImpl) getEntryVAddr(vAddr uint64) uint64 {
	if m.perVPNMSHR {
		return (vAddr >> m.log2PageSize) << m.log2PageSize
	}

	return m.getBaseVaddr(vAddr)
}

func (m *mshrImpl) findEntry(pid vm.PID, vAddr uint64) (*mshrEntry, int) {
	BaseVaddr := m.getEntryVAddr(vAddr)
	for i, e := range m.entries {
		if e.pid == pid && e.baseVAddr == BaseVaddr {
			return e, i
		}
	}

	return nil, -1
}

func (m *mshrImpl) GetUpLevelBitMap(pid vm.PID, vAddr uint64) ([8]bool, bool) {
	entry, _ := m.findEntry(pid, vAddr)
	if entry == nil {
		return [8]bool{}, false
	}

	return entry.UplevelBitMap, true
}

func (m *mshrImpl) UpdateUpLevelBitMap(pid vm.PID, vAddr uint64, now sim.VTimeInSec, predictRadius int) bool {
	entry, _ := m.findEntry(pid, vAddr)
	if entry == nil {
		return false
	}

	bitmap := entry.UplevelBitMap
	realAddrBitmap := entry.RealAddrBitmap
	VPN := vAddr >> m.log2PageSize
	bit := VPN % 8

	bitmap[bit] = true
	if !realAddrBitmap[bit] {
		entry.RealAddrTime[bit] = now
	}
	realAddrBitmap[bit] = true

	entry.UplevelBitMap = bitmap
	entry.RealAddrBitmap = realAddrBitmap
	return true
}

func (m *mshrImpl) SavePage(Page vm.Page) bool {
	entry, _ := m.findEntry(Page.PID, Page.VAddr)
	if entry == nil {
		return false
	}

	offset := m.getVaddrOffset(Page.VAddr)
	if offset >= 0 && offset < 8 {
		entry.Pages[offset] = Page
		entry.ResponseBitMap[offset] = true
		return true
	}

	fmt.Printf("Invalid offset %d for vAddr\n", offset)
	return false
}

func (m *mshrImpl) getVaddrOffset(vAddr uint64) int {
	VPN := vAddr >> m.log2PageSize
	BaseVPN := (VPN >> 3) << 3 // Clear the lower 3 bits to get the base VPN
	return int(VPN - BaseVPN)  // 返回 0-7 的索引
}

func (m *mshrImpl) UpdatePage(pid vm.PID, vAddr uint64, page vm.Page) bool {
	entry, _ := m.findEntry(pid, vAddr)
	if entry == nil {
		return false
	}

	offset := m.getVaddrOffset(vAddr)
	if offset >= 0 && offset < 8 {
		entry.Pages[offset] = page
		entry.ResponseBitMap[offset] = true
		return true
	}

	fmt.Printf("Invalid offset %d for vAddr %d in MSHR for PID %d\n", offset, vAddr, pid)
	return false
}

func (m *mshrImpl) GetPages(pid vm.PID, vAddr uint64) ([8]vm.Page, bool) {
	var pages [8]vm.Page
	entry, _ := m.findEntry(pid, vAddr)
	if entry == nil {
		return pages, false
	}

	for i := 0; i < 8; i++ {
		if entry.Pages[i] != (vm.Page{}) {
			pages[i] = entry.Pages[i]
		}
	}
	return pages, true
}

func (m *mshrImpl) IsReady(pid vm.PID, vAddr uint64) bool {
	entry, _ := m.findEntry(pid, vAddr)
	if entry == nil {
		return false
	}

	return entry.IsReady()
}

func (m *mshrImpl) GetNonZeroElementsfromBitmap(pid vm.PID, vaddr uint64) int {
	entry, _ := m.findEntry(pid, vaddr)
	if entry == nil {
		return 0
	}

	count := 0
	for _, bit := range entry.RealAddrBitmap {
		if bit {
			count++
		}
	}
	return count
}

func (m *mshrImpl) UpdateResponseBitMap(pid vm.PID, vAddr uint64) bool {
	entry, _ := m.findEntry(pid, vAddr)
	if entry == nil {
		return false
	}

	bitmap := entry.ResponseBitMap
	VPN := vAddr >> m.log2PageSize

	bitmap[VPN%8] = true
	entry.ResponseBitMap = bitmap
	return true
}

func (m *mshrImpl) GetResponseBitMap(pid vm.PID, vAddr uint64) ([8]bool, bool) {
	entry, _ := m.findEntry(pid, vAddr)
	if entry == nil {
		return [8]bool{}, false
	}

	return entry.ResponseBitMap, true
}

func (m *mshrImpl) IsPredicted(pid vm.PID, vAddr uint64) bool {
	entry, _ := m.findEntry(pid, vAddr)
	if entry == nil {
		return false
	}

	VPN := vAddr >> m.log2PageSize
	return entry.UplevelBitMap[VPN%8]
}
