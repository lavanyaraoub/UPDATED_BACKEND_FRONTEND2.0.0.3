package channels

// SocketMessage struct pass to client
type SocketMessage struct {
	Data      string `json:"data"`
	Reference string `json:"ref"`
	Position  string `json:"pos"`
	Event     string `json:"event"`

	Direction int `json:"dir"`
	Action    int `json:"action"`

	JogFeed *float64 `json:"jog_feed,omitempty"`

	Position2  float32  `json:"position"`
	FileName   string   `json:"file_name"`
	Alarm      string   `json:"alarm"`
	Code       int      `json:"code,omitempty"`
	LineNumber int      `json:"line"`
	DriveName  string   `json:"drive_name"`
	IOStat     IOStatus `json:"ioStat"`
	UserLine   string   `json:"user_line,omitempty"`
}

type IOStatus struct {
	ECS    bool `json:"ecs"`
	FIN    bool `json:"fin"`
	SOLOP  bool `json:"sol_op"`
	CL     bool `json:"cl"`
	DCL    bool `json:"dcl"`
	ALMIN  bool `json:"alm_in"`
	ALMOUT bool `json:"alm_out"`
	HOME   bool `json:"home"`
	POT    bool `json:"pot"`
	NOT    bool `json:"not"`
}

// BroadCastUIChannel channel used by other modules to send messages to ui via socket.
var BroadCastUIChannel chan SocketMessage

// SendAlarm helper function to send alarm to the connected clients.
func SendAlarm(alarm string) {
	message := SocketMessage{Alarm: alarm, Event: "alarm_error"}
	select {
	case BroadCastUIChannel <- message:
	default:
	}
}

// SendSystemNotice sends non-drive-alarm UI notices.
// Important: this must not use alarm_error, otherwise update/rollback
// success messages can overwrite real drive faults on the UI.
func SendSystemNotice(notice string) {
	message := SocketMessage{Data: notice, Event: "system_notice"}
	select {
	case BroadCastUIChannel <- message:
	default:
	}
}

// SendLineNumber broadcast the line number of the current executing code.
func SendLineNumber(line int) {
	message := SocketMessage{LineNumber: line, Event: "line_number"}
	select {
	case BroadCastUIChannel <- message:
	default:
	}
}

// NotifyUIProgramCompleted notify client that program is completed.
func NotifyUIProgramCompleted() {
	message := SocketMessage{Event: "program_complete"}
	select {
	case BroadCastUIChannel <- message:
	default:
	}
}

func StepModeComplete() {
	message := SocketMessage{Event: "step_mode_completed"}
	select {
	case BroadCastUIChannel <- message:
	default:
	}
}

func DestinationReached() {
	message := SocketMessage{Event: "destination_reached"}
	select {
	case BroadCastUIChannel <- message:
	default:
	}
}