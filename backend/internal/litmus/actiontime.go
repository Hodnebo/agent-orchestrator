package litmus

import "time"

// actionTime records both wall time for older recordings and the monotonic
// offset used by an active leg recording.
type actionTime struct {
	sentAt         time.Time
	recordOffsetMS *int64
	recordSegment  string
	generation     uint64 // daemon initialization at send time, even if a later receipt restarts it
}

// actionSent samples the leg recorder's monotonic clock at the action boundary.
// elapsed is fixed at recorder construction. Sampling it without the capture
// lock keeps frame disk I/O out of the action's dispatch path.
func (s *Service) actionSent(runID, leg string) actionTime {
	var stamp actionTime
	s.recorderMu.Lock()
	r := s.recorders[runID+"/"+leg]
	s.recorderMu.Unlock()
	if r != nil && r.elapsed != nil {
		offset := r.elapsed().Milliseconds()
		stamp.recordOffsetMS = &offset
		stamp.recordSegment = r.file
	}
	stamp.sentAt = s.clock()
	return stamp
}
