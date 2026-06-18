// Package internal provides support for the driver implementation.
package internal

import (
	"sync"

	"github.com/sarchlab/akita/v3/mem/vm"
)

// A MemoryAllocator can allocate memory on the CPU and GPUs
type MemoryAllocator interface {
	RegisterDevice(device *Device)
	GetDeviceIDByPAddr(pAddr uint64) int
	Allocate(pid vm.PID, byteSize uint64, deviceID int) uint64
	AllocateUnified(pid vm.PID, byteSize uint64) uint64
	Free(vAddr uint64)
	Remap(pid vm.PID, pageVAddr, byteSize uint64, deviceID int)
	RemovePage(vAddr uint64)
	AllocatePageWithGivenVAddr(
		pid vm.PID,
		deviceID int,
		vAddr uint64,
		unified bool,
	) vm.Page
}

// NewMemoryAllocator creates a new memory allocator.
func NewMemoryAllocator(
	pageTable vm.PageTable,
	log2PageSize uint64,
) MemoryAllocator {
	a := &memoryAllocatorImpl{
		pageTable:            pageTable,
		totalStorageByteSize: 1 << log2PageSize, // Starting with a page to avoid 0 address.
		log2PageSize:         log2PageSize,
		processMemoryStates:  make(map[vm.PID]*processMemoryState),
		vAddrToPageMapping:   make(map[uint64]vm.Page),
		devices:              make(map[int]*Device),
	}
	return a
}

type processMemoryState struct {
	pid       vm.PID
	nextVAddr uint64
}

// A memoryAllocatorImpl provides the default implementation for
// memoryAllocator
type memoryAllocatorImpl struct {
	sync.Mutex
	pageTable            vm.PageTable
	log2PageSize         uint64
	vAddrToPageMapping   map[uint64]vm.Page
	processMemoryStates  map[vm.PID]*processMemoryState
	devices              map[int]*Device
	totalStorageByteSize uint64
}

func (a *memoryAllocatorImpl) RegisterDevice(device *Device) {
	a.Lock()
	defer a.Unlock()

	state := device.MemState
	state.setInitialAddress(a.totalStorageByteSize)

	a.totalStorageByteSize += state.getStorageSize()

	a.devices[device.ID] = device
}

func (a *memoryAllocatorImpl) GetDeviceIDByPAddr(pAddr uint64) int {
	a.Lock()
	defer a.Unlock()

	return a.deviceIDByPAddr(pAddr)
}

func (a *memoryAllocatorImpl) deviceIDByPAddr(pAddr uint64) int {
	for id, dev := range a.devices {
		state := dev.MemState
		if isPAddrOnDevice(pAddr, state) {
			return id
		}
	}

	panic("device not found")
}

func isPAddrOnDevice(
	pAddr uint64,
	state DeviceMemoryState,
) bool {
	return pAddr >= state.getInitialAddress() &&
		pAddr < state.getInitialAddress()+state.getStorageSize()
}

func (a *memoryAllocatorImpl) Allocate(
	pid vm.PID,
	byteSize uint64,
	deviceID int,
) uint64 {
	if byteSize == 0 {
		panic("Allocating 0 bytes.")
	}

	a.Lock()
	defer a.Unlock()

	pageSize := uint64(1 << a.log2PageSize)
	numPages := (byteSize-1)/pageSize + 1
	return a.allocatePages(int(numPages), pid, deviceID, false)
}

func (a *memoryAllocatorImpl) AllocateUnified(
	pid vm.PID,
	byteSize uint64,
) uint64 {
	if byteSize == 0 {
		panic("Allocating 0 bytes.")
	}

	a.Lock()
	defer a.Unlock()

	pageSize := uint64(1 << a.log2PageSize)
	numPages := (byteSize-1)/pageSize + 1
	return a.allocatePages(int(numPages), pid, 1, true)
}

func (a *memoryAllocatorImpl) allocatePages(
	numPages int,
	pid vm.PID,
	deviceID int,
	unified bool,
) (firstPageVAddr uint64) {
	pState, found := a.processMemoryStates[pid]
	if !found {
		a.processMemoryStates[pid] = &processMemoryState{
			pid:       pid,
			nextVAddr: uint64(1 << a.log2PageSize),
		}
		pState = a.processMemoryStates[pid]
	}
	device := a.devices[deviceID]

	pageSize := uint64(1 << a.log2PageSize)
	nextVAddr := pState.nextVAddr
	// initVPN := nextVAddr >> a.log2PageSize
	currentPage, exists := a.pageTable.GetLastPage(pid)
	pageBlockNum := uint64(0)
	if exists {
		pageBlockNum = currentPage.PageBlock
	}

	currentPageBlock := pageBlockNum + 1

	for i := 0; i < numPages; i++ {
		// Previous unified-GPU policy:
		// pAddr := device.allocatePage()
		//
		// For a unified GPU, device.allocatePage() selects the next actual GPU
		// after every page, which creates page-level round-robin placement.
		// Keep the old line above for quick rollback, but use contiguous
		// distribute-style placement for multi-tile experiments.
		pAddr := a.allocatePageWithDistributePolicy(device, i, numPages)
		vAddr := nextVAddr + uint64(i)*pageSize

		page := vm.Page{
			PID:          pid,
			VAddr:        vAddr,
			PAddr:        pAddr,
			PageSize:     pageSize,
			Valid:        true,
			Unified:      unified,
			AccessCounts: 0,
			DeviceID:     uint64(a.deviceIDByPAddr(pAddr)),
			PageBlock:    currentPageBlock,
		}

		// fmt.Printf("page.addr is %x page.vAddr is %d page.Device ID is %d \n", page.PAddr, page.VAddr>>12, page.DeviceID)
		// debug.PrintStack()
		a.pageTable.Insert(page)
		a.vAddrToPageMapping[page.VAddr] = page
	}

	pState.nextVAddr += pageSize * uint64(numPages)
	// finalVPN := (pState.nextVAddr - 1) >> a.log2PageSize

	// startVPN := nextVAddr >> a.log2PageSize
	// endVPN := (pState.nextVAddr - 1) >> a.log2PageSize

	// fmt.Printf("Allocating %d pages for process %d on device %d. VPN: %d - %d, currentPageBlock: %d\n",
	// numPages, pid, deviceID, startVPN, endVPN, currentPageBlock)

	return nextVAddr
}

func (a *memoryAllocatorImpl) allocatePageWithDistributePolicy(
	device *Device,
	pageIndex int,
	numPages int,
) uint64 {
	if device.Type != DeviceTypeUnifiedGPU {
		return device.allocatePage()
	}

	actualGPU := a.selectActualGPUForDistributedPage(
		device, pageIndex, numPages)
	return actualGPU.allocatePage()
}

func (a *memoryAllocatorImpl) selectActualGPUForDistributedPage(
	device *Device,
	pageIndex int,
	numPages int,
) *Device {
	numGPUs := len(device.ActualGPUs)
	if numGPUs == 0 {
		panic("unified GPU has no actual GPUs")
	}

	numPagesPerGPU := numPages / numGPUs
	if numPagesPerGPU == 0 {
		return device.ActualGPUs[0]
	}

	gpuIndex := pageIndex / numPagesPerGPU
	if gpuIndex >= numGPUs {
		gpuIndex = numGPUs - 1
	}

	return device.ActualGPUs[gpuIndex]
}

// func (a *memoryAllocatorImpl) allocatePages(
// 	numPages int,
// 	pid vm.PID,
// 	deviceID int,
// 	unified bool,
// ) (firstPageVAddr uint64) {
// 	pState, found := a.processMemoryStates[pid]
// 	if !found {
// 		a.processMemoryStates[pid] = &processMemoryState{
// 			pid:       pid,
// 			nextVAddr: uint64(1 << a.log2PageSize),
// 		}
// 		pState = a.processMemoryStates[pid]
// 	}
// 	device := a.devices[deviceID]

// 	pageSize := uint64(1 << a.log2PageSize)
// 	nextVAddr := pState.nextVAddr
// 	// initVPN := nextVAddr >> a.log2PageSize
// 	currentPage, exists := a.pageTable.GetLastPage(pid)
// 	pageBlockNum := uint64(0)
// 	if exists {
// 		pageBlockNum = currentPage.PageBlock
// 	}

// 	currentPageBlock := pageBlockNum + 1

// 	for i := 0; i < numPages; i++ {
// 		pAddr := device.allocatePage()
// 		vAddr := nextVAddr + uint64(i)*pageSize

// 		page := vm.Page{
// 			PID:          pid,
// 			VAddr:        vAddr,
// 			PAddr:        pAddr,
// 			PageSize:     pageSize,
// 			Valid:        true,
// 			Unified:      unified,
// 			AccessCounts: 0,
// 			DeviceID:     uint64(a.deviceIDByPAddr(pAddr)),
// 			PageBlock:    currentPageBlock,
// 		}

// 		// fmt.Printf("page.addr is %x page.vAddr is %d page.Device ID is %d \n", page.PAddr, page.VAddr>>12, page.DeviceID)
// 		// debug.PrintStack()
// 		a.pageTable.Insert(page)
// 		a.vAddrToPageMapping[page.VAddr] = page
// 	}

// 	pState.nextVAddr += pageSize * uint64(numPages)
// 	// finalVPN := (pState.nextVAddr - 1) >> a.log2PageSize

// 	// startVPN := nextVAddr >> a.log2PageSize
// 	// endVPN := (pState.nextVAddr - 1) >> a.log2PageSize

// 	// fmt.Printf("Allocating %d pages for process %d on device %d. VPN: %d - %d, currentPageBlock: %d\n",
// 	// numPages, pid, deviceID, startVPN, endVPN, currentPageBlock)

// 	return nextVAddr
// }

func (a *memoryAllocatorImpl) Remap(
	pid vm.PID,
	pageVAddr, byteSize uint64,
	deviceID int,
) {
	a.Lock()
	defer a.Unlock()

	pageSize := uint64(1 << a.log2PageSize)
	addr := pageVAddr
	vAddrs := make([]uint64, 0)
	for addr < pageVAddr+byteSize {
		vAddrs = append(vAddrs, addr)
		addr += pageSize
	}

	a.allocateMultiplePagesWithGivenVAddrs(pid, deviceID, vAddrs, false)
}

func (a *memoryAllocatorImpl) RemovePage(vAddr uint64) {
	a.Lock()
	defer a.Unlock()

	a.removePage(vAddr)
}

func (a *memoryAllocatorImpl) removePage(vAddr uint64) {
	page, ok := a.vAddrToPageMapping[vAddr]

	if !ok {
		panic("page not found")
	}

	deviceID := a.deviceIDByPAddr(page.PAddr)
	dState := a.devices[deviceID].MemState
	dState.addSinglePAddr(page.PAddr)

	a.pageTable.Remove(page.PID, page.VAddr)
}

func (a *memoryAllocatorImpl) AllocatePageWithGivenVAddr(
	pid vm.PID,
	deviceID int,
	vAddr uint64,
	isUnified bool,
) vm.Page {
	a.Lock()
	defer a.Unlock()

	return a.allocatePageWithGivenVAddr(pid, deviceID, vAddr, isUnified)
}

func (a *memoryAllocatorImpl) allocatePageWithGivenVAddr(
	pid vm.PID,
	deviceID int,
	vAddr uint64,
	isUnified bool,
) vm.Page {
	pageSize := uint64(1 << a.log2PageSize)

	device := a.devices[deviceID]
	pAddr := device.allocatePage()

	page := vm.Page{
		PID:      pid,
		VAddr:    vAddr,
		PAddr:    pAddr,
		PageSize: pageSize,
		Valid:    true,
		DeviceID: uint64(deviceID),
		Unified:  isUnified,
	}
	a.vAddrToPageMapping[page.VAddr] = page
	a.pageTable.Update(page)

	return page
}

func (a *memoryAllocatorImpl) allocateMultiplePagesWithGivenVAddrs(
	pid vm.PID,
	deviceID int,
	vAddrs []uint64,
	isUnified bool,
) (pages []vm.Page) {
	pageSize := uint64(1 << a.log2PageSize)

	device := a.devices[deviceID]
	pAddrs := device.allocateMultiplePages(len(vAddrs))

	for i, vAddr := range vAddrs {
		page := vm.Page{
			PID:      pid,
			VAddr:    vAddr,
			PAddr:    pAddrs[i],
			PageSize: pageSize,
			Valid:    true,
			DeviceID: uint64(deviceID),
			Unified:  isUnified,
		}
		a.vAddrToPageMapping[page.VAddr] = page
		a.pageTable.Update(page)
		pages = append(pages, page)
	}

	return pages
}

func (a *memoryAllocatorImpl) Free(ptr uint64) {
	a.Lock()
	defer a.Unlock()

	a.removePage(ptr)
}
