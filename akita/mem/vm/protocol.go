// Package vm provides the models for address translations
package vm

import (
	"github.com/sarchlab/akita/v3/sim"
)

// A TranslationReq asks the receiver component to translate the request.
type TranslationReq struct {
	sim.MsgMeta
	VAddr         uint64
	PID           PID
	TaskID        string
	DeviceID      uint64
	OriginPort    sim.Port
	Translate     bool
	NeedTranslate bool
	Hops          int
	StartGPUID    int // The GPU ID that starts the translation
	PrefetchNums  int // The number of prefetches to be sent
	IsPrefetch    bool
	TransLatency  uint64
	BitMap        [8]bool
}

// Meta returns the meta data associated with the message.
func (r *TranslationReq) Meta() *sim.MsgMeta {
	return &r.MsgMeta
}

// TranslationReqBuilder can build translation requests
type TranslationReqBuilder struct {
	sendTime      sim.VTimeInSec
	src, dst      sim.Port
	vAddr         uint64
	pid           PID
	taskID        string
	deviceID      uint64
	origPort      sim.Port
	translate     bool
	needTranslate bool
	bitMap        [8]bool
	transLatency  uint64
	prefetchNums  int
	isPrefetch    bool
}

func (b TranslationReqBuilder) WithTransLatency(
	latency uint64,
) TranslationReqBuilder {
	b.transLatency = latency
	return b
}

func (b TranslationReqBuilder) WithBitMap(bitMap [8]bool) TranslationReqBuilder {
	b.bitMap = bitMap
	return b
}

// WithPrefetchNums sets the number of prefetches to be sent.
func (b TranslationReqBuilder) WithPrefetchNums(
	prefetchNums int,
) TranslationReqBuilder {
	b.prefetchNums = prefetchNums
	return b
}

// WithPrefetch marks whether the request is a synthetic prefetch.
func (b TranslationReqBuilder) WithPrefetch(isPrefetch bool) TranslationReqBuilder {
	b.isPrefetch = isPrefetch
	return b
}

func (b TranslationReqBuilder) WithNeedTranslate(
	needTranslate bool,
) TranslationReqBuilder {
	b.needTranslate = needTranslate
	return b
}

// WithTranslate sets the translate flag of the request to build.
func (b TranslationReqBuilder) WithTranslate(
	translate bool,
) TranslationReqBuilder {
	b.translate = translate
	return b
}

func (b TranslationReqBuilder) WithOriginPort(port sim.Port) TranslationReqBuilder {
	b.origPort = port
	return b
}

// WithSendTime sets the send time of the request to build.:w
func (b TranslationReqBuilder) WithSendTime(
	t sim.VTimeInSec,
) TranslationReqBuilder {
	b.sendTime = t
	return b
}

// WithSrc sets the source of the request to build.
func (b TranslationReqBuilder) WithSrc(src sim.Port) TranslationReqBuilder {
	b.src = src
	return b
}

// WithDst sets the destination of the request to build.
func (b TranslationReqBuilder) WithDst(dst sim.Port) TranslationReqBuilder {
	b.dst = dst
	return b
}

// WithVAddr sets the virtual address of the request to build.
func (b TranslationReqBuilder) WithVAddr(vAddr uint64) TranslationReqBuilder {
	b.vAddr = vAddr
	return b
}

// WithPID sets the virtual address of the request to build.
func (b TranslationReqBuilder) WithPID(pid PID) TranslationReqBuilder {
	b.pid = pid
	return b
}

// WithDeviceID sets the GPU ID of the request to build.
func (b TranslationReqBuilder) WithDeviceID(deviceID uint64) TranslationReqBuilder {
	b.deviceID = deviceID
	return b
}

func (b TranslationReqBuilder) WithTaskID(taskID string) TranslationReqBuilder {
	b.taskID = taskID
	return b
}

// Build creates a new TranslationReq
func (b TranslationReqBuilder) Build() *TranslationReq {
	r := &TranslationReq{}
	r.ID = sim.GetIDGenerator().Generate()
	r.Src = b.src
	r.Dst = b.dst
	r.SendTime = b.sendTime
	r.VAddr = b.vAddr
	r.PID = b.pid
	r.TaskID = b.taskID
	r.DeviceID = b.deviceID
	r.OriginPort = b.origPort
	r.Translate = b.translate
	r.NeedTranslate = b.needTranslate
	r.PrefetchNums = b.prefetchNums
	r.IsPrefetch = b.isPrefetch
	r.BitMap = b.bitMap
	r.TransLatency = b.transLatency
	return r
}

// A TranslationRsp is the respond for a TranslationReq. It carries the physical
// address.
type TranslationRsp struct {
	sim.MsgMeta
	RespondTo        string // The ID of the request it replies
	Page             Page
	TaskID           string
	OriginPort       sim.Port
	IsPrefetch       bool
	AvailableWalkers int // The number of available walkers to handle the request
}

// Meta returns the meta data associated with the message.
func (r *TranslationRsp) Meta() *sim.MsgMeta {
	return &r.MsgMeta
}

// GetRspTo returns the request ID that the respond is responding to.
func (r *TranslationRsp) GetRspTo() string {
	return r.RespondTo
}

// TranslationRspBuilder can build translation requests
type TranslationRspBuilder struct {
	sendTime         sim.VTimeInSec
	src, dst         sim.Port
	rspTo            string
	page             Page
	taskID           string
	origPort         sim.Port
	isPrefetch       bool
	availableWalkers int
}

// WithAvailableWalkers sets the number of available walkers to handle the request.
func (b TranslationRspBuilder) WithAvailableWalkers(
	availableWalkers int,
) TranslationRspBuilder {
	b.availableWalkers = availableWalkers
	return b
}

// WithOriginPort sets the origin port of the respond to build.
func (b TranslationRspBuilder) WithOriginPort(port sim.Port) TranslationRspBuilder {
	b.origPort = port
	return b
}

func (b TranslationRspBuilder) WithPrefetch(
	isPrefetch bool,
) TranslationRspBuilder {
	b.isPrefetch = isPrefetch
	return b
}

// WithSendTime sets the send time of the message to build.
func (b TranslationRspBuilder) WithSendTime(
	t sim.VTimeInSec,
) TranslationRspBuilder {
	b.sendTime = t
	return b
}

// WithSrc sets the source of the respond to build.
func (b TranslationRspBuilder) WithSrc(src sim.Port) TranslationRspBuilder {
	b.src = src
	return b
}

// WithDst sets the destination of the respond to build.
func (b TranslationRspBuilder) WithDst(dst sim.Port) TranslationRspBuilder {
	b.dst = dst
	return b
}

// WithRspTo sets the request ID of the respond to build.
func (b TranslationRspBuilder) WithRspTo(rspTo string) TranslationRspBuilder {
	b.rspTo = rspTo
	return b
}

// WithPage sets the page of the respond to build.
func (b TranslationRspBuilder) WithPage(page Page) TranslationRspBuilder {
	b.page = page
	return b
}

func (b TranslationRspBuilder) WithTaskID(taskID string) TranslationRspBuilder {
	b.taskID = taskID
	return b
}

// Build creates a new TranslationRsp
func (b TranslationRspBuilder) Build() *TranslationRsp {
	r := &TranslationRsp{}
	r.ID = sim.GetIDGenerator().Generate()
	r.Src = b.src
	r.Dst = b.dst
	r.SendTime = b.sendTime
	r.RespondTo = b.rspTo
	r.Page = b.page
	r.TaskID = b.taskID
	r.OriginPort = b.origPort
	r.IsPrefetch = b.isPrefetch
	r.AvailableWalkers = b.availableWalkers
	return r
}

// PageMigrationInfo records the information required for the driver to perform
// a page migration.
type PageMigrationInfo struct {
	GPUReqToVAddrMap map[uint64][]uint64
}

// PageMigrationReqToDriver is a req to driver from MMU to start page migration process
type PageMigrationReqToDriver struct {
	sim.MsgMeta

	StartTime         sim.VTimeInSec
	EndTime           sim.VTimeInSec
	MigrationInfo     *PageMigrationInfo
	CurrAccessingGPUs []uint64
	PID               PID
	CurrPageHostGPU   uint64
	PageSize          uint64
	RespondToTop      bool
}

// Meta returns the meta data associated with the message.
func (m *PageMigrationReqToDriver) Meta() *sim.MsgMeta {
	return &m.MsgMeta
}

// NewPageMigrationReqToDriver creates a PageMigrationReqToDriver.
func NewPageMigrationReqToDriver(
	time sim.VTimeInSec,
	src, dst sim.Port,
) *PageMigrationReqToDriver {
	cmd := new(PageMigrationReqToDriver)
	cmd.SendTime = time
	cmd.Src = src
	cmd.Dst = dst
	return cmd
}

// PageMigrationRspFromDriver is a rsp from driver to MMU marking completion of migration
type PageMigrationRspFromDriver struct {
	sim.MsgMeta

	StartTime sim.VTimeInSec
	EndTime   sim.VTimeInSec
	VAddr     []uint64
	RspToTop  bool
}

// Meta returns the meta data associated with the message.
func (m *PageMigrationRspFromDriver) Meta() *sim.MsgMeta {
	return &m.MsgMeta
}

// NewPageMigrationRspFromDriver creates a new PageMigrationRspFromDriver.
func NewPageMigrationRspFromDriver(
	time sim.VTimeInSec,
	src, dst sim.Port,
) *PageMigrationRspFromDriver {
	cmd := new(PageMigrationRspFromDriver)
	cmd.SendTime = time
	cmd.Src = src
	cmd.Dst = dst
	return cmd
}

type RemoveMSHRReq struct {
	sim.MsgMeta
	VAddr      uint64
	PID        PID
	TaskID     string
	DeviceID   uint64
	OriginPort sim.Port
	Translate  bool
}

func (r *RemoveMSHRReq) Meta() *sim.MsgMeta {
	return &r.MsgMeta
}

type RemoveMSHRReqBuilder struct {
	sendTime  sim.VTimeInSec
	src, dst  sim.Port
	vAddr     uint64
	pid       PID
	taskID    string
	deviceID  uint64
	origPort  sim.Port
	translate bool
}

func (b RemoveMSHRReqBuilder) WithTranslate(
	translate bool,
) RemoveMSHRReqBuilder {
	b.translate = translate
	return b
}

func (b RemoveMSHRReqBuilder) WithOriginPort(port sim.Port) RemoveMSHRReqBuilder {
	b.origPort = port
	return b
}

func (b RemoveMSHRReqBuilder) WithSendTime(
	t sim.VTimeInSec,
) RemoveMSHRReqBuilder {
	b.sendTime = t
	return b
}

func (b RemoveMSHRReqBuilder) WithSrc(src sim.Port) RemoveMSHRReqBuilder {
	b.src = src
	return b
}

func (b RemoveMSHRReqBuilder) WithDst(dst sim.Port) RemoveMSHRReqBuilder {
	b.dst = dst
	return b
}

func (b RemoveMSHRReqBuilder) WithVAddr(vAddr uint64) RemoveMSHRReqBuilder {
	b.vAddr = vAddr
	return b
}

func (b RemoveMSHRReqBuilder) WithPID(pid PID) RemoveMSHRReqBuilder {
	b.pid = pid
	return b
}

func (b RemoveMSHRReqBuilder) WithDeviceID(deviceID uint64) RemoveMSHRReqBuilder {
	b.deviceID = deviceID
	return b
}

func (b RemoveMSHRReqBuilder) WithTaskID(taskID string) RemoveMSHRReqBuilder {
	b.taskID = taskID
	return b
}

func (b RemoveMSHRReqBuilder) Build() *RemoveMSHRReq {
	r := &RemoveMSHRReq{}
	r.ID = sim.GetIDGenerator().Generate()
	r.Src = b.src
	r.Dst = b.dst
	r.SendTime = b.sendTime
	r.VAddr = b.vAddr
	r.PID = b.pid
	r.TaskID = b.taskID
	r.DeviceID = b.deviceID
	r.OriginPort = b.origPort
	r.Translate = b.translate
	return r
}

type PageLoadMsg struct {
	sim.MsgMeta
	Page       Page
	OriginPort sim.Port
	TaskID     string
	LocalFlag  bool
}

func (r *PageLoadMsg) Meta() *sim.MsgMeta {
	return &r.MsgMeta
}

type PageLoadMsgBuilder struct {
	sendTime  sim.VTimeInSec
	src, dst  sim.Port
	page      Page
	origPort  sim.Port
	taskID    string
	localFlag bool
}

func (b PageLoadMsgBuilder) WithLocalFlag(localFlag bool) PageLoadMsgBuilder {
	b.localFlag = localFlag
	return b
}

func (b PageLoadMsgBuilder) WithOriginPort(port sim.Port) PageLoadMsgBuilder {
	b.origPort = port
	return b
}

func (b PageLoadMsgBuilder) WithSendTime(
	t sim.VTimeInSec,
) PageLoadMsgBuilder {
	b.sendTime = t
	return b
}

func (b PageLoadMsgBuilder) WithSrc(src sim.Port) PageLoadMsgBuilder {
	b.src = src
	return b
}

func (b PageLoadMsgBuilder) WithDst(dst sim.Port) PageLoadMsgBuilder {
	b.dst = dst
	return b
}

func (b PageLoadMsgBuilder) WithPage(page Page) PageLoadMsgBuilder {
	b.page = page
	return b
}

func (b PageLoadMsgBuilder) WithTaskID(taskID string) PageLoadMsgBuilder {
	b.taskID = taskID
	return b
}

func (b PageLoadMsgBuilder) Build() *PageLoadMsg {
	r := &PageLoadMsg{}
	r.ID = sim.GetIDGenerator().Generate()
	r.Src = b.src
	r.Dst = b.dst
	r.SendTime = b.sendTime
	r.Page = b.page
	r.OriginPort = b.origPort
	r.TaskID = b.taskID
	r.LocalFlag = b.localFlag
	return r
}

type TranslationFinished struct {
	sim.MsgMeta
	PID              PID
	VAddr            uint64
	TranslatedBitmap [8]bool
	TaskID           string
}

func (r *TranslationFinished) Meta() *sim.MsgMeta {
	return &r.MsgMeta
}

type TranslationFinishedBuilder struct {
	sendTime         sim.VTimeInSec
	src, dst         sim.Port
	vaddr            uint64
	pID              PID
	taskID           string
	TranslatedBitmap [8]bool
}

func (b TranslationFinishedBuilder) WithTranslatedBitmap(
	bitmap [8]bool,
) TranslationFinishedBuilder {
	b.TranslatedBitmap = bitmap
	return b
}

func (b TranslationFinishedBuilder) WithSendTime(
	t sim.VTimeInSec,
) TranslationFinishedBuilder {
	b.sendTime = t
	return b
}

func (b TranslationFinishedBuilder) WithPID(
	pID PID,
) TranslationFinishedBuilder {
	b.pID = pID
	return b
}

func (b TranslationFinishedBuilder) WithSrc(
	src sim.Port,
) TranslationFinishedBuilder {
	b.src = src
	return b
}
func (b TranslationFinishedBuilder) WithDst(
	dst sim.Port,
) TranslationFinishedBuilder {
	b.dst = dst
	return b
}

func (b TranslationFinishedBuilder) WithVAddr(
	VAddr uint64,
) TranslationFinishedBuilder {
	b.vaddr = VAddr
	return b
}

func (b TranslationFinishedBuilder) WithTaskID(
	taskID string,
) TranslationFinishedBuilder {
	b.taskID = taskID
	return b
}

func (b TranslationFinishedBuilder) Build() *TranslationFinished {
	r := &TranslationFinished{}
	r.ID = sim.GetIDGenerator().Generate()
	r.Src = b.src
	r.Dst = b.dst
	r.SendTime = b.sendTime
	r.VAddr = b.vaddr
	r.TaskID = b.taskID
	r.PID = b.pID
	r.TranslatedBitmap = b.TranslatedBitmap
	return r
}
