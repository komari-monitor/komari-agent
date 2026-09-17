package cmd

import (
	"log"
	"sync"
)

type shutdownCoordinator struct {
	mu            sync.Mutex
	once          sync.Once
	exitCode      int
	stopWarning   func()
	stopNetstatic func() error
	exit          func(int)
}

func newShutdownCoordinator(stopWarning func(), stopNetstatic func() error, exit func(int)) *shutdownCoordinator {
	return &shutdownCoordinator{
		stopWarning:   stopWarning,
		stopNetstatic: stopNetstatic,
		exit:          exit,
	}
}

func (s *shutdownCoordinator) shutdown(exitCode int) {
	s.mu.Lock()
	if exitCode > s.exitCode {
		s.exitCode = exitCode
	}
	s.mu.Unlock()

	s.once.Do(func() {
		s.stopWarning()
		if err := s.stopNetstatic(); err != nil {
			log.Printf("Failed to stop netstatic monitoring: %v", err)
		}

		s.mu.Lock()
		exitCode = s.exitCode
		s.mu.Unlock()
		s.exit(exitCode)
	})
}
