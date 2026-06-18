package vm

import "github.com/sarchlab/akita/v3/sim"

type PrefetchFeedbackState int

const (
	PrefetchFeedbackStateEnabled PrefetchFeedbackState = iota + 1
	PrefetchFeedbackStateDisabled
)

type PrefetchFeedbackMsg struct {
	sim.MsgMeta
	PageBlock uint64
	TargetGPM uint64
	State     PrefetchFeedbackState
}

func (m *PrefetchFeedbackMsg) Meta() *sim.MsgMeta {
	return &m.MsgMeta
}

type PrefetchFeedbackMsgBuilder struct {
	sendTime  sim.VTimeInSec
	src, dst  sim.Port
	pageBlock uint64
	targetGPM uint64
	state     PrefetchFeedbackState
}

func (b PrefetchFeedbackMsgBuilder) WithSendTime(t sim.VTimeInSec) PrefetchFeedbackMsgBuilder {
	b.sendTime = t
	return b
}

func (b PrefetchFeedbackMsgBuilder) WithSrc(src sim.Port) PrefetchFeedbackMsgBuilder {
	b.src = src
	return b
}

func (b PrefetchFeedbackMsgBuilder) WithDst(dst sim.Port) PrefetchFeedbackMsgBuilder {
	b.dst = dst
	return b
}

func (b PrefetchFeedbackMsgBuilder) WithPageBlock(pageBlock uint64) PrefetchFeedbackMsgBuilder {
	b.pageBlock = pageBlock
	return b
}

func (b PrefetchFeedbackMsgBuilder) WithTargetGPM(targetGPM uint64) PrefetchFeedbackMsgBuilder {
	b.targetGPM = targetGPM
	return b
}

func (b PrefetchFeedbackMsgBuilder) WithState(state PrefetchFeedbackState) PrefetchFeedbackMsgBuilder {
	b.state = state
	return b
}

func (b PrefetchFeedbackMsgBuilder) Build() *PrefetchFeedbackMsg {
	m := &PrefetchFeedbackMsg{}
	m.ID = sim.GetIDGenerator().Generate()
	m.Src = b.src
	m.Dst = b.dst
	m.SendTime = b.sendTime
	m.PageBlock = b.pageBlock
	m.TargetGPM = b.targetGPM
	m.State = b.state
	return m
}
