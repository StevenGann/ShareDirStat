//go:build !linux

package ops

import "os"

// unixAccessW has no portable equivalent outside Linux. Developer machines
// fall back to trusting the later operation to report the real error; the
// deployment target is Linux, where the real check runs.
func unixAccessW(int) error {
	_ = os.Getpid()
	return nil
}
