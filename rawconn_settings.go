package copper

import (
	"fmt"
	"time"
)

type rawConnSettings struct {
	owner     *rawConn
	callbacks []func(error)

	localConnWindowSize     int
	remoteConnWindowSize    int
	localStreamWindowSize   int
	remoteStreamWindowSize  int
	localInactivityTimeout  time.Duration
	remoteInactivityTimeout time.Duration
}

func (s *rawConnSettings) init(owner *rawConn) {
	s.owner = owner
	s.localConnWindowSize = defaultConnWindowSize
	s.remoteConnWindowSize = defaultConnWindowSize
	s.localStreamWindowSize = defaultStreamWindowSize
	s.remoteStreamWindowSize = defaultStreamWindowSize
	s.localInactivityTimeout = defaultInactivityTimeout
	s.remoteInactivityTimeout = defaultInactivityTimeout
}

func (s *rawConnSettings) failLocked(err error) {
	callbacks := s.callbacks
	s.callbacks = nil
	for _, callback := range callbacks {
		if callback != nil {
			callback(err)
		}
	}
}

func (s *rawConnSettings) handleAck() {
	var callback func(error)
	s.owner.mu.Lock()
	if len(s.callbacks) > 0 {
		callback = s.callbacks[0]
		copy(s.callbacks, s.callbacks[1:])
		s.callbacks[len(s.callbacks)-1] = nil
		s.callbacks = s.callbacks[:len(s.callbacks)-1]
	}
	s.owner.mu.Unlock()
	if callback != nil {
		callback(nil)
	}
}

func (s *rawConnSettings) handleSettings(frame *settingsFrame) error {
	s.owner.mu.Lock()
	for key, value := range frame.values {
		switch key {
		case settingConnWindow:
			if value < minWindowSize || value > maxWindowSize {
				s.owner.mu.Unlock()
				return copperError{
					error: fmt.Errorf("cannot set connection window to %d bytes", value),
					code:  EINVALIDFRAME,
				}
			}
			diff := int(value) - s.remoteConnWindowSize
			s.owner.outgoing.changeWindow(diff)
			s.remoteConnWindowSize = int(value)
		case settingStreamWindow:
			if value < minWindowSize || value > maxWindowSize {
				s.owner.mu.Unlock()
				return copperError{
					error: fmt.Errorf("cannot set stream window to %d bytes", value),
					code:  EINVALIDFRAME,
				}
			}
			diff := int(value) - s.remoteStreamWindowSize
			s.owner.streams.changeWindow(diff)
			s.remoteStreamWindowSize = int(value)
		case settingInactivityMilliseconds:
			if value < 1000 {
				s.owner.mu.Unlock()
				return copperError{
					error: fmt.Errorf("cannot set inactivity timeout to %dms", value),
					code:  EINVALIDFRAME,
				}
			}
			s.remoteInactivityTimeout = time.Duration(value) * time.Millisecond
		default:
			s.owner.mu.Unlock()
			return copperError{
				error: fmt.Errorf("unknown settings key %d", key),
				code:  EINVALIDFRAME,
			}
		}
	}
	s.owner.mu.Unlock()
	s.owner.outgoing.addSettingsAck()
	return nil
}

func (s *rawConnSettings) getLocalStreamWindowSize() int {
	s.owner.mu.RLock()
	window := s.localStreamWindowSize
	s.owner.mu.RUnlock()
	return window
}

func (s *rawConnSettings) getRemoteStreamWindowSize() int {
	s.owner.mu.RLock()
	window := s.remoteStreamWindowSize
	s.owner.mu.RUnlock()
	return window
}

func (s *rawConnSettings) getLocalInactivityTimeout() time.Duration {
	s.owner.mu.RLock()
	d := s.localInactivityTimeout
	s.owner.mu.RUnlock()
	return d
}

func (s *rawConnSettings) getRemoteInactivityTimeout() time.Duration {
	s.owner.mu.RLock()
	d := s.remoteInactivityTimeout
	s.owner.mu.RUnlock()
	return d
}
