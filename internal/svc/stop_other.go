//go:build !windows

package svc

import "github.com/kardianos/service"

// stopService: systemd/launchd stop commands are bounded by the service manager.
func stopService(s service.Service) error {
	if err := s.Stop(); err != nil {
		return err
	}
	waitStopped(s)
	return nil
}
