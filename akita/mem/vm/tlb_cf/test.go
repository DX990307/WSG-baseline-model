func (tlb *TLB) lookupFromTopPort(now sim.VTimeInSec) bool {
	msg := tlb.topPort.Peek()
	if msg == nil {
		return false
	}

	req := msg.(*vm.TranslationReq)

	if tlb.filterFlag {
		if !tlb.checkCuckooFilter(req) {
			msg := vm.TranslationReqBuilder{}.
				WithSendTime(now).
				WithSrc(tlb.cmpPort).
				WithDst(tlb.CmpModule).
				WithPID(req.PID).
				WithVAddr(req.VAddr).
				WithDeviceID(req.DeviceID).
				WithTaskID(req.TaskID).
				WithOriginPort(tlb.outsidePort).
				Build()
			tlb.sentToCmp(now, msg)
		}
	}

	mshrReq := &MshrRequest{
		Request:    req,
		LocalFlag:  true,
		RemoteFlag: false,
	}

	mshrEntry := tlb.mshr.Query(req.PID, req.VAddr)
	if mshrEntry != nil {
		return tlb.processTLBMSHRHit(now, mshrEntry, mshrReq)
	}

	setID := tlb.vAddrToSetID(req.VAddr)
	set := tlb.Sets[setID]
	wayID, page, found := set.Lookup(req.PID, req.VAddr)
	if found && page.Valid {
		return tlb.handleTranslationHit(now, mshrReq, setID, wayID, page)
	}

	return tlb.handleTranslationMiss(now, mshrReq)
}