package tlb_gmmu

import "github.com/sarchlab/akita/v3/mem/vm"

type pcdLocator struct {
	valid bool
	setID int
	wayID int
}

type pcdEntry struct {
	valid         bool
	pid           vm.PID
	baseVAddr     uint64
	presentBitmap [8]bool
	locators      [8]pcdLocator
	lastVisit     uint64
}

type pcdSet struct {
	entries []pcdEntry
}

type ptclCoverageDirectory struct {
	sets           []pcdSet
	numSets        int
	numWays        int
	log2PageSize   uint64
	visitCounter   uint64
	entryEvictions int
	staleBits      int
}

func newPTCLCoverageDirectory(
	numSets int,
	numWays int,
	log2PageSize uint64,
) *ptclCoverageDirectory {
	if numSets <= 0 {
		numSets = 1
	}
	if numWays <= 0 {
		numWays = 1
	}

	d := &ptclCoverageDirectory{
		sets:         make([]pcdSet, numSets),
		numSets:      numSets,
		numWays:      numWays,
		log2PageSize: log2PageSize,
	}

	for i := range d.sets {
		d.sets[i].entries = make([]pcdEntry, numWays)
	}

	return d
}

func (d *ptclCoverageDirectory) baseVAddr(vAddr uint64) uint64 {
	vpn := vAddr >> d.log2PageSize
	return ((vpn >> 3) << 3) << d.log2PageSize
}

func (d *ptclCoverageDirectory) bit(vAddr uint64) int {
	vpn := vAddr >> d.log2PageSize
	return int(vpn & 0x7)
}

func (d *ptclCoverageDirectory) setID(pid vm.PID, baseVAddr uint64) int {
	if d.numSets <= 1 {
		return 0
	}

	ptclID := baseVAddr >> (d.log2PageSize + 3)
	shift := uint(0)
	for (1 << shift) < d.numSets {
		shift++
	}

	pidHash := uint64(pid) ^ (uint64(pid) >> shift)
	hashed := ptclID ^ (ptclID >> shift) ^ pidHash
	if d.numSets&(d.numSets-1) == 0 {
		return int(hashed & uint64(d.numSets-1))
	}

	return int(hashed % uint64(d.numSets))
}

func (d *ptclCoverageDirectory) lookupEntry(
	pid vm.PID,
	baseVAddr uint64,
) (*pcdEntry, bool) {
	set := &d.sets[d.setID(pid, baseVAddr)]
	for i := range set.entries {
		entry := &set.entries[i]
		if !entry.valid {
			continue
		}
		if entry.pid == pid && entry.baseVAddr == baseVAddr {
			d.visit(entry)
			return entry, true
		}
	}

	return nil, false
}

func (d *ptclCoverageDirectory) findOrAllocateEntry(
	pid vm.PID,
	baseVAddr uint64,
) *pcdEntry {
	if entry, found := d.lookupEntry(pid, baseVAddr); found {
		return entry
	}

	set := &d.sets[d.setID(pid, baseVAddr)]
	for i := range set.entries {
		entry := &set.entries[i]
		if !entry.valid {
			d.initializeEntry(entry, pid, baseVAddr)
			return entry
		}
	}

	victim := &set.entries[0]
	for i := 1; i < len(set.entries); i++ {
		if set.entries[i].lastVisit < victim.lastVisit {
			victim = &set.entries[i]
		}
	}

	d.entryEvictions++
	d.initializeEntry(victim, pid, baseVAddr)
	return victim
}

func (d *ptclCoverageDirectory) initializeEntry(
	entry *pcdEntry,
	pid vm.PID,
	baseVAddr uint64,
) {
	*entry = pcdEntry{
		valid:     true,
		pid:       pid,
		baseVAddr: baseVAddr,
	}
	d.visit(entry)
}

func (d *ptclCoverageDirectory) visit(entry *pcdEntry) {
	d.visitCounter++
	entry.lastVisit = d.visitCounter
}

func (d *ptclCoverageDirectory) recordFill(
	page vm.Page,
	setID int,
	wayID int,
) {
	if !page.Valid {
		return
	}

	baseVAddr := d.baseVAddr(page.VAddr)
	bit := d.bit(page.VAddr)
	if bit < 0 || bit >= 8 {
		return
	}

	entry := d.findOrAllocateEntry(page.PID, baseVAddr)
	entry.presentBitmap[bit] = true
	entry.locators[bit] = pcdLocator{
		valid: true,
		setID: setID,
		wayID: wayID,
	}
}

func (d *ptclCoverageDirectory) removePage(
	page vm.Page,
	setID int,
	wayID int,
) {
	if !page.Valid {
		return
	}

	baseVAddr := d.baseVAddr(page.VAddr)
	bit := d.bit(page.VAddr)
	if bit < 0 || bit >= 8 {
		return
	}

	entry, found := d.lookupEntry(page.PID, baseVAddr)
	if !found {
		return
	}

	locator := entry.locators[bit]
	if locator.valid && locator.setID == setID && locator.wayID == wayID {
		d.clearBit(entry, bit)
	}
}

func (d *ptclCoverageDirectory) invalidatePage(pid vm.PID, vAddr uint64) {
	baseVAddr := d.baseVAddr(vAddr)
	bit := d.bit(vAddr)
	if bit < 0 || bit >= 8 {
		return
	}

	entry, found := d.lookupEntry(pid, baseVAddr)
	if !found {
		return
	}

	d.clearBit(entry, bit)
}

func (d *ptclCoverageDirectory) clearBit(entry *pcdEntry, bit int) {
	entry.presentBitmap[bit] = false
	entry.locators[bit] = pcdLocator{}

	for i := 0; i < 8; i++ {
		if entry.presentBitmap[i] {
			return
		}
	}

	entry.valid = false
}

func (d *ptclCoverageDirectory) validEntryCount() int {
	count := 0
	for i := range d.sets {
		set := &d.sets[i]
		for j := range set.entries {
			if set.entries[j].valid {
				count++
			}
		}
	}
	return count
}
