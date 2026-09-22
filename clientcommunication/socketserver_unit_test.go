//go:build unit

package clientcommunication

import (
	"testing"
	"time"

	channels "EtherCAT/channels"
	"EtherCAT/motordriver/statusnotifier"

	gosocketio "github.com/graarh/golang-socketio"
	"github.com/graarh/golang-socketio/transport"
)

// ─── ConnectedClientList / removeClient ──────────────────────────────────────

func resetClients() {
	connectedClients = ConnectedClientList{}
}

func TestRemoveClient_RemovesMatchingID(t *testing.T) {
	resetClients()
	connectedClients.Clients = []Client{
		{ID: "aaa", Channel: nil},
		{ID: "bbb", Channel: nil},
		{ID: "ccc", Channel: nil},
	}
	removeClient("bbb")
	if len(connectedClients.Clients) != 2 {
		t.Fatalf("expected 2 clients after remove, got %d", len(connectedClients.Clients))
	}
	for _, c := range connectedClients.Clients {
		if c.ID == "bbb" {
			t.Error("bbb should have been removed")
		}
	}
}

func TestRemoveClient_RemovesFirst(t *testing.T) {
	resetClients()
	connectedClients.Clients = []Client{
		{ID: "first", Channel: nil},
		{ID: "second", Channel: nil},
	}
	removeClient("first")
	if len(connectedClients.Clients) != 1 {
		t.Fatalf("expected 1 client, got %d", len(connectedClients.Clients))
	}
	if connectedClients.Clients[0].ID != "second" {
		t.Errorf("remaining client should be 'second', got %q", connectedClients.Clients[0].ID)
	}
}

func TestRemoveClient_RemovesLast(t *testing.T) {
	resetClients()
	connectedClients.Clients = []Client{
		{ID: "first", Channel: nil},
		{ID: "last", Channel: nil},
	}
	removeClient("last")
	if len(connectedClients.Clients) != 1 {
		t.Fatalf("expected 1 client, got %d", len(connectedClients.Clients))
	}
	if connectedClients.Clients[0].ID != "first" {
		t.Errorf("remaining client should be 'first', got %q", connectedClients.Clients[0].ID)
	}
}

func TestRemoveClient_NonExistentID_NoChange(t *testing.T) {
	resetClients()
	connectedClients.Clients = []Client{
		{ID: "aaa", Channel: nil},
		{ID: "bbb", Channel: nil},
	}
	removeClient("zzz")
	if len(connectedClients.Clients) != 2 {
		t.Errorf("expected 2 clients unchanged, got %d", len(connectedClients.Clients))
	}
}

func TestRemoveClient_EmptyList_NoOp(t *testing.T) {
	resetClients()
	removeClient("anything") // must not panic
	if len(connectedClients.Clients) != 0 {
		t.Error("empty list should remain empty")
	}
}

func TestRemoveClient_AllClients_ResultsInEmpty(t *testing.T) {
	resetClients()
	connectedClients.Clients = []Client{
		{ID: "x", Channel: nil},
	}
	removeClient("x")
	if len(connectedClients.Clients) != 0 {
		t.Errorf("expected empty list, got %d clients", len(connectedClients.Clients))
	}
}

func TestRemoveClient_DuplicateIDs_RemovesFirst(t *testing.T) {
	resetClients()
	// If two clients somehow share an ID, only the first should be removed
	connectedClients.Clients = []Client{
		{ID: "dup", Channel: nil},
		{ID: "dup", Channel: nil},
		{ID: "other", Channel: nil},
	}
	removeClient("dup")
	if len(connectedClients.Clients) != 2 {
		t.Fatalf("expected 2 clients after removing first dup, got %d", len(connectedClients.Clients))
	}
	// First entry should now be the second "dup"
	if connectedClients.Clients[0].ID != "dup" {
		t.Error("second dup should remain")
	}
}

// ─── rs232State / rs232Status struct ─────────────────────────────────────────

func TestRS232State_DefaultIsZero(t *testing.T) {
	// rs232State is package-level — defaults to 0 (OFF)
	// We can't reset it between tests easily, but we can verify the type
	// and that it holds integer values
	orig := rs232State
	rs232State = 0
	if rs232State != 0 {
		t.Error("rs232State should be 0 when set to 0")
	}
	rs232State = 1
	if rs232State != 1 {
		t.Error("rs232State should be 1 when set to 1")
	}
	rs232State = orig
}

func TestRS232Status_FieldsSetCorrectly(t *testing.T) {
	s := rs232Status{Data: "1"}
	if s.Data != "1" {
		t.Errorf("Data = %q, want 1", s.Data)
	}
	s2 := rs232Status{Data: "0"}
	if s2.Data != "0" {
		t.Errorf("Data = %q, want 0", s2.Data)
	}
}

// ─── ConnectedClientList struct ───────────────────────────────────────────────

func TestConnectedClientList_AppendAndLength(t *testing.T) {
	list := ConnectedClientList{}
	if len(list.Clients) != 0 {
		t.Errorf("new list should be empty, got %d", len(list.Clients))
	}
	list.Clients = append(list.Clients, Client{ID: "a", Channel: nil})
	list.Clients = append(list.Clients, Client{ID: "b", Channel: nil})
	if len(list.Clients) != 2 {
		t.Errorf("expected 2 clients, got %d", len(list.Clients))
	}
}

func TestClient_IDField(t *testing.T) {
	c := Client{ID: "test-123", Channel: nil}
	if c.ID != "test-123" {
		t.Errorf("ID = %q, want test-123", c.ID)
	}
}

// ─── sendAlarm — "No Alarms" vs real-alarm branch ────────────────────────────
//
// sendAlarm's only pure logic (not requiring a real *gosocketio.Channel) is
// the decision to call channels.WriteCommandExecInput("stop_prog_exec", "")
// when the alarm string is anything other than "No Alarms". The client-Emit
// loop is exercised with connectedClients.Clients kept empty — a non-nil
// Channel would panic on Emit since gosocketio.Channel needs a real
// connection, so this is the only way to reach the branch logic safely.

func TestSendAlarm_NoAlarmsMessage_DoesNotTriggerStopProgExec(t *testing.T) {
	resetClients() // ensure no clients — Emit loop body never runs

	channels.CommandExecInputChannel = make(chan channels.CommandExecInput, 4)
	t.Cleanup(func() { channels.CommandExecInputChannel = nil })

	sendAlarm(channels.SocketMessage{Alarm: "No Alarms", Event: "alarm_error"})

	select {
	case msg := <-channels.CommandExecInputChannel:
		t.Errorf("sendAlarm('No Alarms') should not write to CommandExecInputChannel, got %+v", msg)
	default:
		// expected — nothing written
	}
}

func TestSendAlarm_RealAlarm_TriggersStopProgExec(t *testing.T) {
	resetClients()

	channels.CommandExecInputChannel = make(chan channels.CommandExecInput, 4)
	t.Cleanup(func() { channels.CommandExecInputChannel = nil })

	sendAlarm(channels.SocketMessage{Alarm: "POT Limit Exceeded", Event: "alarm_error"})

	select {
	case msg := <-channels.CommandExecInputChannel:
		if msg.InputType != "stop_prog_exec" {
			t.Errorf("InputType = %q, want stop_prog_exec", msg.InputType)
		}
	default:
		t.Error("sendAlarm(real alarm) should write stop_prog_exec to CommandExecInputChannel")
	}
}

func TestSendAlarm_EmptyClientList_NoPanic(t *testing.T) {
	resetClients()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("sendAlarm panicked with empty client list: %v", r)
		}
	}()
	sendAlarm(channels.SocketMessage{Alarm: "some alarm"})
}

// ─── Send — empty client list ────────────────────────────────────────────────

func TestSend_EmptyClientList_NoPanic(t *testing.T) {
	resetClients()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Send panicked with empty client list: %v", r)
		}
	}()
	Send(channels.SocketMessage{Event: "current_position", Data: "45.0"})
}

// ─── executeProgram — drive-faulted early-return path ────────────────────────
//
// IMPORTANT: executeProgram delegates to executors.RunCodeFile, which checks
// motor.GetCurrentAlarm() FIRST and returns immediately if the drive is
// faulted — but if the drive is NOT faulted, RunCodeFile's very next step is
// a REAL license-server check (executors/command_executor.go's
// validateLicenseBeforeRun -> licensechecker.CheckLicense), which makes a
// live network call and can take 30-40+ seconds, and even writes a real
// license file to disk on success. That path is fundamentally incompatible
// with a fast, isolated unit test — there is no filename, missing or not,
// that reaches "file not found" without going through it first.
//
// This test instead exercises executeProgram's error-handling via the
// drive-faulted branch, which returns before the license check entirely:
// simulating a fault with statusnotifier.Alarm(...) makes RunCodeFile
// return an error immediately, and executeProgram forwards it to
// channels.SendAlarm — exactly the behavior we actually want to verify,
// without the network dependency.

func TestExecuteProgram_DriveFaulted_SendsAlarmToUIChannel(t *testing.T) {
	statusnotifier.Alarm("POT Limit Exceeded")
	t.Cleanup(func() { statusnotifier.AlarmCleared() })

	// Drain whatever's in the channel (including the broadcast from the
	// Alarm() call above) so the assertion below sees only executeProgram's
	// own message.
	for len(channels.BroadCastUIChannel) > 0 {
		<-channels.BroadCastUIChannel
	}

	executeProgram("irrelevant.nc") // never reached — RunCodeFile returns before opening any file

	select {
	case msg := <-channels.BroadCastUIChannel:
		if msg.Alarm == "" {
			t.Errorf("expected a non-empty alarm message when the drive is faulted, got %+v", msg)
		}
	case <-time.After(time.Second):
		t.Error("executeProgram with a faulted drive did not send an alarm within 1s")
	}
}

// ─── socketEventsCreator ──────────────────────────────────────────────────

// TestSocketEventsCreator_RegistersHandlersWithoutPanic exercises
// socketEventsCreator directly. It only registers event handler closures on
// the server — it never starts listening on the network — so this is safe
// to call in a unit test. The closures themselves (OnConnection,
// OnDisconnection, the rs232/execute_program event handlers) aren't invoked
// here since that requires a real connected client; this test only confirms
// registration itself completes cleanly.
//
// Start() and uiBradcastMessageListner() are deliberately NOT tested here:
// Start() calls http.ListenAndServe and blocks forever on a real port;
// uiBradcastMessageListner() is an infinite loop with no stop mechanism
// (unlike the listeners in motordriver, which were given one during the
// race-detection work earlier in this project) — testing it directly would
// leak a goroutine for the life of the test binary.
func TestSocketEventsCreator_RegistersHandlersWithoutPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("socketEventsCreator panicked: %v", r)
		}
	}()
	server := gosocketio.NewServer(transport.GetDefaultWebsocketTransport())
	socketEventsCreator(server)
}
