//go:build unit

package channels

import (
	"os"
	"testing"
)

// TestMain ensures all global state is clean and channels are properly
// initialized before any test runs. Uses a buffered SingleModeChannel
// so NotifySingleModeComplete never blocks in tests.
func TestMain(m *testing.M) {
	CommandExecStatusChannel = make(chan CommandExecStatus, 1)
	SingleModeChannel = make(chan CommandExecStatus, 1) // buffered — prevents deadlock in tests
	isChannelOpen = false
	isSingleModeOpen = false
	DriverActionChannel = nil
	BroadCastUIChannel = nil
	BroadCastDriveStatusChannel = nil
	isReady = false
	os.Exit(m.Run())
}
