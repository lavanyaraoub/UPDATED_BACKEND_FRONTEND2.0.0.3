package channels

/*
Channel and its type used by motorodriver/motor_action_listener.go to perform any motor driver actions
like Reset, Jog etc
*/

//ActionType enum type of actions callers can ask the drive to do. MotorActionListener only listen and performs
//to the ActioType detailed below
type ActionType string

const (
	//RESET perform a reset action on the driver
	RESET ActionType = "RESET"

	//JOG initiate jog
	JOG = "JOG"

	//EXIT_DRIVE_LISTENER exit the go routine listens to DriverActionChannel
	EXIT_DRIVE_LISTENER = "EXIT_DRIVE_LISTENER"

	//STOP_JOG stop jogging
	STOP_JOG = "STOP_JOG"

	//MANUAL_JOG start manual jog initiated from the ui
	MANUAL_JOG = "MANUAL_JOG"

	//ZEROREF set the driver to 0 position
	ZERO_REF = "ZERO_REF"

	//STEP_MODE run motor in step mode
	STEP_MODE = "STEP_MODE"

	//STEP_MODE_ENABLE called when step mode enabled from ui
	STEP_MODE_ENABLE = "STEP_MODE_ENABLE"

	//SET_RPM set rpm of the driver
	SET_RPM = "SET_RPM"

	//MOVE_TO_POSITION move to the desired position
	MOVE_TO_POSITION = "MOVE_TO_POSITION"

	//POSITION_MODE ABS/REL determines absolute or relative mode positioning
	POSITION_MODE = "POSITION_MODE"

	//START_EXECUTION start execution of the program
	START_EXECUTION = "START_EXECUTION"

	//SHORTEST_PATH_ENABLED if true then drive should rotate in shortest path
	SHORTEST_PATH_ENABLED = "SHORTEST_PATH_ENABLED"

	EMERGENCY = "EMERGENCY"

	PROGRAM_EXEC_COMPLETED = "PROGRAM_EXEC_COMPLETED"

	FAST_POWER_OFF = "FAST_POWER_OFF"

	RESET_MULTI_TURN = "RESET_MULTI_TURN"

	SET_WORK_OFFSET = "SET_WORK_OFFSET"

	SETTINGS_CHANGED = "SETTINGS_CHANGED"

	STOP_PROGRAM_EXECUTION = "STOP_PROGRAM_EXECUTION"
)

//DriverAction what action a drive should perform is passed via this struct
type DriverAction struct {
	Action    ActionType
	DriveName string
	Value     string
	Direction int
}

//DriverActionChannel channel used by other modules to perform actions to a driver.
var DriverActionChannel chan DriverAction

var isReady bool = false

func DriveActionChannelReady() {
	isReady = true
}

//NotifyMotorDriver modules can call this function to notify action to motor driver
func NotifyMotorDriver(action ActionType, value string, driveName string, direction int) {
	if !isReady {
		return
	}
	driverAction := DriverAction{Action: action, DriveName: driveName, Value: value, Direction: direction}
	DriverActionChannel <- driverAction
}

// ─────────────────────────────────────────────────────────────────────────────
// BPF: RunProgramFunc callback
//
// Holds a reference to executors.RunCodeFile registered at startup by
// executors package. This breaks the motordriver→executors import cycle:
//
//   executors registers RunCodeFile here at startup (executors→channels ✓)
//   motordriver calls RunProgramFunc from bpf_modbus_poller (motordriver→channels ✓)
//   No direct motordriver→executors import needed.
// ─────────────────────────────────────────────────────────────────────────────

// RunProgramFunc is the function called by BPF poller to execute a GM code file.
// Registered by executors.RegisterRunProgramFunc() at startup.
var RunProgramFunc func(filePath string) error

// RegisterRunProgramFunc is called once at startup by the executors package
// to register executors.RunCodeFile into this channel so motordriver can
// call it without importing executors directly.
func RegisterRunProgramFunc(fn func(filePath string) error) {
	RunProgramFunc = fn
}

// NotifyRunProgram executes the registered RunProgramFunc with the given file path.
// Called by bpf_modbus_poller when a valid BPF signal is detected.
func NotifyRunProgram(filePath string) {
	if RunProgramFunc == nil {
		return
	}
	go func() {
		if err := RunProgramFunc(filePath); err != nil {
			SendAlarm("[BPF] RunCodeFile error: " + err.Error())
		}
	}()
}