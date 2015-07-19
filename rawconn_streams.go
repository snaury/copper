package copper

type rawConnStreams struct {
	owner  *rawConn
	err    error
	flag   uint32
	next   uint32
	closed bool

	live map[uint32]*rawStream
}

func (s *rawConnStreams) init(owner *rawConn, server bool) {
	s.owner = owner
	if server {
		s.flag = 0
		s.next = 2
	} else {
		s.flag = 1
		s.next = 1
	}
	s.live = make(map[uint32]*rawStream)
}

func (s *rawConnStreams) isClient() bool {
	return s.flag != 0
}

func (s *rawConnStreams) ownedID(id uint32) bool {
	return id&1 == s.flag
}

func (s *rawConnStreams) failLocked(err error, closed bool) {
	if s.err == nil || closed && !s.closed {
		s.err = err
		if closed {
			s.closed = true
		}
		for _, stream := range s.live {
			s.owner.mu.Unlock()
			stream.closeWithError(err, closed)
			s.owner.mu.Lock()
		}
	}
}

func (s *rawConnStreams) allocateLocked() (uint32, error) {
	if s.err != nil {
		return 0, s.err
	}
	wrapped := false
	for {
		streamID := s.next
		s.next += 2
		if s.next >= 0x80000000 {
			if wrapped {
				return 0, ErrNoFreeStreamID
			}
			s.next -= 0x80000000
			if s.next == 0 {
				s.next = 2
			}
			wrapped = true
		}
		stream := s.live[streamID]
		if stream == nil {
			return streamID, nil
		}
	}
}

func (s *rawConnStreams) addLockedWithUnlock(stream *rawStream) {
	s.live[stream.streamID] = stream
	err := s.err
	closed := s.closed
	s.owner.mu.Unlock()
	if err != nil {
		stream.closeWithError(err, closed)
	}
}

func (s *rawConnStreams) find(streamID uint32) *rawStream {
	s.owner.mu.RLock()
	stream := s.live[streamID]
	s.owner.mu.RUnlock()
	return stream
}

func (s *rawConnStreams) remove(stream *rawStream) {
	s.owner.mu.Lock()
	if s.live[stream.streamID] == stream {
		delete(s.live, stream.streamID)
	}
	s.owner.mu.Unlock()
}

func (s *rawConnStreams) changeWindow(diff int) {
	s.owner.mu.RLock()
	for _, stream := range s.live {
		stream.changeWindow(diff)
	}
	s.owner.mu.RUnlock()
}
