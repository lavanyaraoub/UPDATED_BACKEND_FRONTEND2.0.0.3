//go:build unit

package channels

import (
	"testing"
	"time"
)

// resetCmdState resets all command-exec globals to a clean baseline.
// Called at the start of every test that touches these globals.
func resetCmdState() {
	isChannelOpen = false
	isSingleModeOpen = false
	// Drain stale messages
	for len(CommandExecStatusChannel) > 0 {
		<-CommandExecStatusChannel
	}
	// Drain buffered SingleModeChannel
	for len(SingleModeChannel) > 0 {
		<-SingleModeChannel
	}
}

// ─── OpenWaitChannel ─────────────────────────────────────────────────────────

func TestOpenWaitChannel_SetsChannelOpen(t *testing.T) {
	resetCmdState()
	OpenWaitChannel()
	if !isChannelOpen {
		t.Error("expected isChannelOpen = true after OpenWaitChannel()")
	}
}

// ─── WaitTillCmdComplete ─────────────────────────────────────────────────────

func TestWaitTillCmdComplete_WhenNotOpen_ReturnsImmediately(t *testing.T) {
	resetCmdState()
	// isChannelOpen=false — WaitTillCmdComplete must return without blocking
	done := make(chan struct{})
	go func() { WaitTillCmdComplete(); close(done) }()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("WaitTillCmdComplete blocked when isChannelOpen=false")
	}
}

func TestWaitTillCmdComplete_ResetsFlag(t *testing.T) {
	resetCmdState()
	OpenWaitChannel()
	// Send the completion signal before calling Wait so it never blocks
	CommandExecStatusChannel <- CommandExecStatus{Completed: true}
	WaitTillCmdComplete()
	if isChannelOpen {
		t.Error("isChannelOpen should be false after WaitTillCmdComplete")
	}
}

// ─── NotifyCmdComplete ───────────────────────────────────────────────────────

func TestNotifyCmdComplete_WhenNotOpen_IsNoOp(t *testing.T) {
	resetCmdState()
	// Should return immediately without sending anything
	NotifyCmdComplete()
	if len(CommandExecStatusChannel) != 0 {
		t.Error("NotifyCmdComplete should not send when isChannelOpen=false")
	}
}

func TestNotifyCmdComplete_WhenOpen_SendsAndClosesFlag(t *testing.T) {
	resetCmdState()
	OpenWaitChannel()
	NotifyCmdComplete()
	if isChannelOpen {
		t.Error("isChannelOpen should be false after NotifyCmdComplete")
	}
	if len(CommandExecStatusChannel) != 1 {
		t.Error("expected one message on CommandExecStatusChannel")
	}
	// drain
	<-CommandExecStatusChannel
}

// ─── Full round-trip ─────────────────────────────────────────────────────────

func TestCmdComplete_RoundTrip(t *testing.T) {
	resetCmdState()
	OpenWaitChannel()

	done := make(chan struct{})
	go func() {
		WaitTillCmdComplete()
		close(done)
	}()

	// Give goroutine time to reach the channel receive
	time.Sleep(20 * time.Millisecond)
	NotifyCmdComplete()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("WaitTillCmdComplete did not unblock after NotifyCmdComplete")
	}
}

func TestCmdComplete_MultipleRoundTrips(t *testing.T) {
	for i := 0; i < 5; i++ {
		resetCmdState()
		OpenWaitChannel()
		done := make(chan struct{})
		go func() {
			WaitTillCmdComplete()
			close(done)
		}()
		time.Sleep(10 * time.Millisecond)
		NotifyCmdComplete()
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("round trip %d: WaitTillCmdComplete did not unblock", i)
		}
	}
}

// ─── OpenSingleModeChannel ───────────────────────────────────────────────────

func TestOpenSingleModeChannel_SetsFlag(t *testing.T) {
	resetCmdState()
	OpenSingleModeChannel()
	if !isSingleModeOpen {
		t.Error("expected isSingleModeOpen = true after OpenSingleModeChannel()")
	}
}

// ─── WaitTillSingleModeComplete ──────────────────────────────────────────────

func TestWaitTillSingleModeComplete_WhenNotOpen_ReturnsImmediately(t *testing.T) {
	resetCmdState()
	done := make(chan struct{})
	go func() { WaitTillSingleModeComplete(); close(done) }()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("WaitTillSingleModeComplete blocked when isSingleModeOpen=false")
	}
}

// ─── NotifySingleModeComplete ────────────────────────────────────────────────

func TestNotifySingleModeComplete_WhenNotOpen_IsNoOp(t *testing.T) {
	resetCmdState()
	NotifySingleModeComplete()
	if len(SingleModeChannel) != 0 {
		t.Error("NotifySingleModeComplete should not send when isSingleModeOpen=false")
	}
}

func TestNotifySingleModeComplete_WhenOpen_SendsMessage(t *testing.T) {
	resetCmdState()
	OpenSingleModeChannel()
	NotifySingleModeComplete() // SingleModeChannel is buffered(1) in TestMain — won't block
	// NotifySingleModeComplete sends but does NOT clear isSingleModeOpen —
	// that flag is only cleared by WaitTillSingleModeComplete after it receives.
	if len(SingleModeChannel) != 1 {
		t.Error("expected one message on SingleModeChannel after NotifySingleModeComplete")
	}
	<-SingleModeChannel // drain
}
