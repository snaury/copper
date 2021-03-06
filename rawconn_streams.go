package copper

// rawConnStreams keeps track of currently live streams
// It uses the connection lock for protecting its fields
type rawConnStreams struct {
	conn *rawConn

	err  error  // non-nil when the connection is closed
	flag uint32 // stream id flag, 1 for clients and 0 for servers
	next uint32 // next stream id, wraps around 2^31

	live map[uint32]*rawStream // currently live streams
}

func (s *rawConnStreams) init(conn *rawConn, server bool) {
	s.conn = conn
	if server {
		s.flag = 0
		s.next = 2
	} else {
		s.flag = 1
		s.next = 1
	}
	s.live = make(map[uint32]*rawStream)
}

// Returns true if the connection is in client mode
func (s *rawConnStreams) isClient() bool {
	return s.flag != 0
}

// Returns true if the stream id is owned by the connection
func (s *rawConnStreams) isOwnedID(id uint32) bool {
	return id&1 == s.flag
}

// Fails all known streams with err
func (s *rawConnStreams) failLocked(err error) {
	if s.err == nil {
		s.err = err
		for _, stream := range s.live {
			s.conn.mu.Unlock()
			stream.CloseWithError(err)
			s.conn.mu.Lock()
		}
	}
}

// Allocates the next free stream id
func (s *rawConnStreams) allocateLocked() (uint32, error) {
	if s.err != nil {
		return 0, s.err
	}
	wrapped := false
	for {
		streamID := s.next
		s.next += 2
		if s.next > MaxStreamID {
			if wrapped {
				return 0, ErrNoFreeStreamID
			}
			wrapped = true
			s.next &= MaxStreamID
			if s.next == 0 {
				s.next = 2
			}
		}
		stream := s.live[streamID]
		if stream == nil {
			return streamID, nil
		}
	}
}

// Adds the stream to the map of live streams
func (s *rawConnStreams) addLocked(stream *rawStream) {
	s.live[stream.streamID] = stream
}

// Returns live stream by id, nil if there is no such stream
func (s *rawConnStreams) find(streamID uint32) *rawStream {
	s.conn.mu.RLock()
	stream := s.live[streamID]
	s.conn.mu.RUnlock()
	return stream
}

// Removes the stream from live streams
func (s *rawConnStreams) remove(stream *rawStream) {
	s.conn.mu.Lock()
	if s.live[stream.streamID] == stream {
		delete(s.live, stream.streamID)
	}
	s.conn.mu.Unlock()
}

// Changes the write window size of all currently live streams
func (s *rawConnStreams) changeWriteWindowLocked(diff int) {
	for _, stream := range s.live {
		stream.changeWriteWindow(diff)
	}
}
