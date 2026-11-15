package main

import (
	"time"
)

func (s *topicsService) resetPartialTodayAttempts() {
	s.registryCacheMu.Lock()
	s.registryPartialNext = time.Time{}
	s.registryCacheMu.Unlock()
}
