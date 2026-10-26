package datatypes

import (
	"EtherCAT/channels"
	"regexp"
	"strconv"
	"sync"
)

// ExecutionController gives every program run a unique generation. Stop,
// Emergency and Reset invalidate that generation permanently, so an old
// goroutine cannot continue after a shared StopExecution flag is cleared.
var ExecController ExecutionController

type ExecutionController struct {
	mu               sync.RWMutex
	generation       uint64
	running          bool
	cancelGeneration uint64
	cancelReason     CancellationReason
}

// CancellationReason distinguishes a resumable operator Stop from a
// destructive Reset/Emergency. Both invalidate the current RunID, but only a
// destructive cancellation is allowed to erase the saved resume line.
type CancellationReason uint8

const (
	CancellationNone CancellationReason = iota
	CancellationStop
	CancellationAbort
)

func (c *ExecutionController) Begin() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	c.running = true
	return c.generation
}

// Cancel stops the active execution but preserves its resume position.
func (c *ExecutionController) Cancel() {
	c.cancel(CancellationStop)
}

// Abort permanently discards the active execution. Reset and Emergency must
// use Abort rather than Cancel.
func (c *ExecutionController) Abort() {
	c.cancel(CancellationAbort)
}

func (c *ExecutionController) cancel(reason CancellationReason) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		// Store the RunID being cancelled before advancing the generation.
		c.cancelGeneration = c.generation
		c.cancelReason = reason
		c.generation++
		c.running = false
		return
	}

	// A Reset can arrive immediately after Stop. Upgrade the already-cancelled
	// run to a destructive abort; never let a duplicate Stop downgrade it.
	if reason > c.cancelReason {
		c.cancelReason = reason
	}
}

func (c *ExecutionController) IsCurrent(runID uint64) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.running && c.generation == runID
}

func (c *ExecutionController) Generation() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.generation
}

func (c *ExecutionController) CancellationReasonFor(runID uint64) CancellationReason {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if runID != 0 && c.cancelGeneration >= runID {
		return c.cancelReason
	}
	return CancellationNone
}

func (c *ExecutionController) CancelGeneration() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cancelGeneration
}

// GetCancelGeneration is retained for command plugins such as delay.
func GetCancelGeneration() uint64 { return ExecController.CancelGeneration() }

type DriveSetting struct {
	POTLimit             int
	NOTLimit             int
	ConfiguredWorkOffset map[string]float64
	// CurrentWorkOffSet    string
	DestinationPosition float64
}

//ExecutionContext keeps track of execution context of the commands
type ExecutionContext struct {
	Commands                  []Command // <-- ADD THIS LINE
	Divide360On                int
	CommandMaps                Execution
	CurrentExecCommandLine     int  /* Set the command line currently executing*/
	LoopCount                  int  /* Set the value accompained with R command. All the commands between R** and G10 should execute the in loop*/
	WhereLoopStarted           int  /* Set the next line after encounter R** commmand. Say R** encounter in line 5 then this will set to 6  */
	RestartExecFromBegining    bool /* Set to  true when M99 detects*/
	LinearInterpolationEnabled bool /* set to true when G01 command executes. Set to false when G0 command executes*/
	NextCmdLineToExec          int
	Err                        error
	TrialModeActive            bool   /* When trial mode active no commands will send to driver, instead will do a trial run to see all the commands are fine */
	RunMode                    string /* Absolute or relative mode, values ABS/REL */
	CurrentLoopCounter         int    /* LoopCount will say how many time loop executed, say for e.g. 10, CurrentLoopCounter will say how many times the loop executed  */
	ExecutionMode              string /* single or continuous, if single then position command will wait for next block singal from ui */
	EmergencyActivated         bool
	StopExecution              bool   /* StopExecution stops the execution of the program  */
	NextLineWhenStopped        int    /* Track of the next line of command to execute when stop program triggered*/
	CommandFileName            string /* holds the file name of the current executing program */
	HasResetted                bool   /* if reset from ui the set it to true*/
	ECSEnabled                 int
	DriveSettings              map[string]DriveSetting
	CurrentWorkOffSet          string
	WaitingForECS              bool
	ExecutingFilePath          string
	// RunID permanently identifies the active program execution generation.
	RunID uint64
	// POTLimit                   int
	// NOTLimit                   int
	// ConfiguredWorkOffset       map[string]int
	// CurrentWorkOffSet          int
	// DestinationPosition float64 /* Calculate and keeps the destination postion, this will be used to identify whether POT or NOT limit breach */
}

// MoveNextLine update the next line to execute.
func (e *ExecutionContext) MoveNextLine() {
	if !e.WaitingForECS {
		e.NextCmdLineToExec = e.CurrentExecCommandLine + 1
	}
}

//EndExecution will set NextCmdLineToExec to -1 and thus ending the execution of the program
func (e *ExecutionContext) EndExecution() {
	e.Reset()
	e.NextCmdLineToExec = -1
}

//MoveToStart move the command line to execute to 0 and start again.
func (e *ExecutionContext) MoveToStart() {
	e.NextCmdLineToExec = 0
	e.Reset()
}

//Reset reset the data to initial state
func (e *ExecutionContext) Reset() {
	e.CurrentExecCommandLine = 0
	e.RestartExecFromBegining = false
	e.LinearInterpolationEnabled = false
	e.NextCmdLineToExec = 0
	e.Err = nil
	e.StopExecution = false
	e.Divide360On = 0
	e.LoopCount = 0
	e.CurrentLoopCounter = 0
	e.WhereLoopStarted = 0
	e.CurrentExecCommandLine = 0
	e.NextLineWhenStopped = 0
	e.RunMode = "ABS"
	e.CurrentWorkOffSet = "G53"
	e.WaitingForECS = false
	for _, i := range e.DriveSettings {
		i.DestinationPosition = 0
	}
}
// ResumeExecution resumes a stopped program from the last saved line.

//ResetExec will reset the executing line number
// func (e *ExecutionContext) ResetExec() {
// 	// e.Divide360On = 0
// 	// e.LoopCount = 0
// 	// e.CurrentLoopCounter = 0
// 	// e.WhereLoopStarted = 0
// 	// e.NextCmdLineToExec = 0
// 	// e.CurrentExecCommandLine = 0
// 	// e.StopExecution = false
// 	// e.NextLineWhenStopped = 0
// 	// e.RunMode = "ABS"
// 	// e.DestinationPosition = 0
// }

//PrepareExecutingFile will compare the current executing file name and the passed one
//if passed one is different from previous then reset the state
func (e *ExecutionContext) PrepareExecutingFile(fileName string) {
	e.StopExecution = false
	e.HasResetted = false
	if e.CommandFileName != fileName {
		e.Reset()
		e.CommandFileName = fileName
	}
}

// ActivateTrialMode activate trial mode, if in trial mode no commands will send any signal to
// motor drivers
func (e *ExecutionContext) ActivateTrialMode() {
	e.TrialModeActive = true
}

// WaitExecuteNextCommand if single block execution enabled then wait for user input to move to next command
func (e *ExecutionContext) WaitExecuteNextCommand() {
	if e.TrialModeActive {
		return
	}
	//&& e.ECSEnabled == 0
	if e.ExecutionMode == "continuous" {
		return
	}
	channels.OpenSingleModeChannel()
	channels.WaitTillSingleModeComplete()
}

//DeActivateTrialMode deactivate trial mode and command handlers will send commands to
//motor drivers
func (e *ExecutionContext) DeActivateTrialMode() {
	e.TrialModeActive = false
}

//ExtractString extract the string part of the passed string
//helpfull to get the driver name
func (e *ExecutionContext) ExtractString(str string) string {
	re := regexp.MustCompile("[A-Z]+")
	return re.FindString(str)
}

//ExtractNumeric extract the number from the passed string
//this will help to get the number for e.g. A90.5 will return 90.5
func (e *ExecutionContext) ExtractNumeric(str string) string {
	re := regexp.MustCompile("[-]?[0-9.]+")
	return re.FindString(str)
}

//ExtractNumericAsFloat return the numeric part as float
func (e *ExecutionContext) ExtractNumericAsFloat(str string) (float64, error) {
	numericStr := e.ExtractNumeric(str)
	return strconv.ParseFloat(numericStr, 32)
}

//ExtractNumericAsInt return the numeric part as int
func (e *ExecutionContext) ExtractNumericAsInt(str string) (int, error) {
	numericStr := e.ExtractNumeric(str)
	return strconv.Atoi(numericStr)
}