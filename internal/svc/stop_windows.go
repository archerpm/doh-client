//go:build windows

package svc

import (
	"fmt"
	"time"

	"github.com/archerpm/doh-client/internal/paths"
	"github.com/kardianos/service"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// stopService stops the service with a hard deadline. kardianos/service v1.3.0
// waits forever if the service hangs in StopPending (its timeout `break` only
// leaves the select), so the SCM is driven directly here.
func stopService(_ service.Service) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(paths.ServiceName)
	if err != nil {
		return err
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return nil
	}
	if st.State != svc.StopPending {
		if _, err := s.Control(svc.Stop); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(45 * time.Second) // daemon restores DNS during stop
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		if st, err = s.Query(); err != nil {
			return err
		}
		if st.State == svc.Stopped {
			return nil
		}
	}
	return fmt.Errorf("service still in state %d after 45 s", st.State)
}
