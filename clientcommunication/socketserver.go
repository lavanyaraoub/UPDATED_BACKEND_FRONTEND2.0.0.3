package clientcommunication

import (
	channels "EtherCAT/channels"
	executors "EtherCAT/executors"
	"EtherCAT/helper"
	logger "EtherCAT/logger"
	motor "EtherCAT/motordriver"
	settings "EtherCAT/settings"
	"EtherCAT/systemupdate"
	"fmt"
	"net/http"
	"strings"
	"sync"

	gosocketio "github.com/graarh/golang-socketio"
	"github.com/graarh/golang-socketio/transport"
)

// ConnectedClientList keep tracks of all the clients connected
type ConnectedClientList struct {
	Clients []Client
}

// Client keeps client details connected via socket
type Client struct {
	Channel *gosocketio.Channel
	ID      string
}

var connectedClients ConnectedClientList

func init() {
	channels.BroadCastUIChannel = make(chan channels.SocketMessage, 100)
}

var rs232State int = 0 // 0 = OFF, 1 = ON(Socketserver rs232 state change)

// RS232 status payload contract for UI: { Data: "0" } or { Data: "1" }
type rs232Status struct {
	Data string `json:"Data"`
}

// =====================================================
// SHARED MANUAL JOG FEED STATE
// =====================================================
// Internal RTC jog-feed value remains 0..20.
// This state mirrors only the selected speed between UIs.
// It does not start/stop motion and does not alter motion math.
var (
	currentJogFeed   float64 = 1.0
	currentJogFeedMu sync.RWMutex
)

func getCurrentJogFeed() float64 {
	currentJogFeedMu.RLock()
	defer currentJogFeedMu.RUnlock()
	return currentJogFeed
}

func setCurrentJogFeed(value float64) {
	currentJogFeedMu.Lock()
	currentJogFeed = value
	currentJogFeedMu.Unlock()
}

type jogFeedStatus struct {
	JogFeed float64 `json:"jog_feed"`
}

type currentAlarmStatus struct {
	Alarm       string `json:"alarm"`
	Code        int    `json:"code"`
	FaultActive bool   `json:"fault_active"`
}

// Start for socket connection from client
func Start() error {

	server := gosocketio.NewServer(transport.GetDefaultWebsocketTransport())
	setCurrentJogFeed(float64(settings.GetDriverSettings("A").JogFeed))

	// ---- RS232: restore persisted state on backend boot ----
	// SAFETY: RS232 is allowed ON only when ECS is enabled.
	data, err := settings.LoadRS232Data()
	if err != nil {
		logger.Error("Failed to load RS232 status from disk:", err)

		// Fail safe.
		rs232State = 0
		executors.RS232Enabled.Store(false)
	} else {
		ecsEnabled := settings.GetDriverSettings("A").ECS == 1

		if data == "1" && !ecsEnabled {
			logger.Warn("[RS232-SAFETY] Persisted RS232=1 but ECS=0; forcing RS232 OFF")

			rs232State = 0
			executors.RS232Enabled.Store(false)
			data = "0"

			if err := settings.SaveRS232Data("0"); err != nil {
				logger.Error("[RS232-SAFETY] Failed to persist forced RS232 OFF:", err)
			}
		} else if data == "1" {
			rs232State = 1
			executors.RS232Enabled.Store(true)
		} else {
			rs232State = 0
			executors.RS232Enabled.Store(false)
		}

		logger.Info("RS232 status restored:", data)
	}

	socketEventsCreator(server)
	serveMux := http.NewServeMux()
	serveMux.Handle("/socket.io/", server)
	//go routine waiting for any sort of messages that needs to transmit to ui
	go uiBradcastMessageListner()

	logger.Info("starting socket.io server listening at port 9090...")
	err = http.ListenAndServe(":9090", serveMux)
	if err != nil {
		logger.Error(err)
	}
	logger.Info("started socket.io server listening at port 9090")
	return err
}

func socketEventsCreator(server *gosocketio.Server) {
	server.On(gosocketio.OnConnection, func(c *gosocketio.Channel) {
		logger.Debug("new client connected, client id:", c.Id())
		client := Client{Channel: c, ID: c.Id()}
		connectedClients.Clients = append(connectedClients.Clients, client)

		emitCurrentDriveAlarm(c)

		// ---- RS232: push current status to UI on connect ----
		cur := "0"
		if executors.RS232Enabled.Load() {
			cur = "1"
		}
		c.Emit("rs232_status", rs232Status{Data: cur})

		// ---- JOG FEED SYNC: push current shared speed to new UI ----
		c.Emit(
			"jog_feed_update",
			jogFeedStatus{JogFeed: getCurrentJogFeed()},
		)
	})

	server.On("get_current_alarm", func(c *gosocketio.Channel) {
		emitCurrentDriveAlarm(c)
	})

	server.On("request_current_alarm", func(c *gosocketio.Channel) {
		emitCurrentDriveAlarm(c)
	})

	server.On(gosocketio.OnDisconnection, func(c *gosocketio.Channel) {
		logger.Debug("client dis-connected, client id:", c.Id())
		removeClient(c.Id())
	})

	// =====================================================
	// SHARED JOG FEED SYNC
	// =====================================================
	// Any connected Manual screen may update the selected
	// jog-feed value. The backend mirrors that value to every
	// connected UI. Motion START/STOP is NOT mirrored here.
	server.On("set_jog_feed", func(c *gosocketio.Channel, msg jogFeedStatus) {
		value := msg.JogFeed

		// Preserve the existing RTC Manual Jog Feed range: 0..20.
		if value < 0 {
			value = 0
		}
		if value > 20 {
			value = 20
		}

		setCurrentJogFeed(value)

		logger.Info(
			"[JOG-SYNC] shared jog_feed=",
			getCurrentJogFeed(),
			" source_client=",
			c.Id(),
		)

		for _, cl := range connectedClients.Clients {
			cl.Channel.Emit(
				"jog_feed_update",
				jogFeedStatus{JogFeed: getCurrentJogFeed()},
			)
		}
	})

	server.On("jog_mode", func(c *gosocketio.Channel, msg channels.SocketMessage) {

		logger.Debug("jog_mode event received from client")

		/*
		 * Existing direction conversion.
		 *
		 * DO NOT CHANGE.
		 */
		direction := 1

		if msg.Direction <= 0 {
			direction = -1
		}

		if msg.Action == 1 {

			/*
			 * =================================================
			 * FEATURE 2 - MANUAL SPEEDOMETER
			 * =================================================
			 *
			 * Empty Value means:
			 *
			 * old frontend / GPIO / legacy behavior
			 *
			 * Non-empty Value means:
			 *
			 * frontend Manual speedometer supplied Jog Feed.
			 */
			jogFeedValue := ""

			if msg.JogFeed != nil {

				// =====================================================
				// JOG SPEED - preserve decimal speed point
				// =====================================================
				jogFeedValue =
					fmt.Sprintf(
						"%.6f",
						*msg.JogFeed,
					)

				logger.Info(
					"[JOG-SPEED] frontend requested jog_feed=",
					*msg.JogFeed,
					" drive=",
					msg.DriveName,
				)

			} else {

				logger.Debug(
					"[JOG-SPEED] no frontend jog_feed; using legacy Machine Parameter",
				)
			}

			logger.Debug("start jogging")

			driverAction := channels.DriverAction{
				Action:    "MANUAL_JOG",
				Direction: direction,
				DriveName: msg.DriveName,

				// =============================================
				// FEATURE 2
				// =============================================
				Value: jogFeedValue,
				// ================= END FEATURE 2 =============
			}

			channels.DriverActionChannel <- driverAction

		} else {

			/*
			 * Existing STOP behavior.
			 *
			 * DO NOT CHANGE.
			 */
			logger.Debug("stop jogging")

			driverAction := channels.DriverAction{
				Action:    "STOP_JOG",
				DriveName: msg.DriveName,
			}

			channels.DriverActionChannel <- driverAction
		}
	})

	server.On("reset", func(c *gosocketio.Channel, msg channels.SocketMessage) {
		logger.Debug("Reset initiated")
		channels.NotifyMotorDriver("RESET", "", msg.DriveName, 0)
		// Do NOT send "No Alarms" here unconditionally.
		// The reset worker (reset_driver_system.go) will send the correct
		// alarm state after the reset completes — "No Alarms" on success,
		// or the active fault string if the drive could not be cleared.
	})

	server.On("goToZero", func(c *gosocketio.Channel, msg channels.SocketMessage) {
		logger.Debug("Zero referenced enabled")
		channels.NotifyMotorDriver("ZERO_REF", "", msg.DriveName, 0)
	})

	server.On("enable_step_mode", func(c *gosocketio.Channel, msg channels.SocketMessage) {
		logger.Debug("step mode enabled")
		channels.NotifyMotorDriver("STEP_MODE_ENABLE", "", msg.DriveName, 0)
	})

	server.On("step_mode", func(c *gosocketio.Channel, msg channels.SocketMessage) {
		logger.Debug("running in step mode, with position to add", msg.Position2)
		direction := 1
		if msg.Direction <= 0 {
			direction = -1
		}
		channels.NotifyMotorDriver("STEP_MODE", fmt.Sprintf("%f", msg.Position2), msg.DriveName, direction)
	})

	server.On("execute", func(c *gosocketio.Channel, msg channels.SocketMessage) {
		logger.Debug("execute the program")
		executeProgram(msg.FileName)
	})

	server.On("emergency", func(c *gosocketio.Channel, msg channels.SocketMessage) {
		logger.Debug("emergency activated")
		channels.NotifyMotorDriver("EMERGENCY", "", msg.DriveName, 0)
	})

	server.On("set_program_mode", func(c *gosocketio.Channel, msg channels.SocketMessage) {
		logger.Debug("set program mode")
		if msg.Data == "single" {
			channels.WriteCommandExecInput("command_exec_mode", "single")
		} else {
			channels.WriteCommandExecInput("command_exec_mode", "continuous")
		}
	})

	server.On("exec_next_line", func(c *gosocketio.Channel, msg channels.SocketMessage) {
		logger.Debug("execute next line")
		channels.WriteCommandExecInput("move_next_line", "1")
	})

	server.On("stop_execution", func(c *gosocketio.Channel, msg channels.SocketMessage) {
		logger.Debug("stop executing program")
		channels.WriteCommandExecInput("stop_prog_exec", "")
		channels.WriteCommandExecInput("move_next_line", "1")
	})

	server.On("resetMultiTurn", func(c *gosocketio.Channel, msg channels.SocketMessage) {
		logger.Debug("reset multiturn requested by user")
		channels.NotifyMotorDriver("RESET_MULTI_TURN", "", msg.DriveName, 0)
	})

	server.On("perform_system_update", func(c *gosocketio.Channel, msg channels.SocketMessage) {
		logger.Debug("system update requested from user")
		go systemupdate.PerformSystemUpdate(true)
	})

	server.On("check_system_update", func(c *gosocketio.Channel, msg channels.SocketMessage) {
		logger.Debug("check for system update requested by user")
		go systemupdate.CheckforUpdates(true)
	})

	server.On("save_line_number", func(c *gosocketio.Channel, msg channels.SocketMessage) {
		logger.Debug("save_line_number event received from client")

		if msg.UserLine == "" {
			logger.Warn("no user_line provided in message")
			return
		}

		err := settings.SaveLineNumber(msg.UserLine)

		if err != nil {
			logger.Error("failed to save line number:", err)
		} else {
			logger.Info("line number saved to userline.json:", msg.UserLine)
			executors.UpdateLastLineFromJSON()
		}
	})

	server.On("save-text-program", func(c *gosocketio.Channel, config settings.TextProgramConfig) {
		logger.Info("save-text-program event received from client")

		err := settings.SaveTextProgramConfig(config)
		if err != nil {
			logger.Error("failed to save text program config:", err)
		} else {
			logger.Info("text program config saved successfully to textprogram.json")

		}
	})

	server.On("get-text-program-config", func(c *gosocketio.Channel) {
		logger.Info("get-text-program-config event received from client")

		config, err := settings.LoadTextProgramConfig()
		if err != nil {
			logger.Error("failed to load text program config:", err)
			c.Emit("text-program-config", nil)
			return
		}

		c.Emit("text-program-config", config)
	})

	// ---- RS232: UI asks for current status ----
	server.On("get_rs232_status", func(c *gosocketio.Channel) {
		cur := "0"
		if executors.RS232Enabled.Load() {
			cur = "1"
		}
		c.Emit("rs232_status", rs232Status{Data: cur})
	})

	server.On("rs232_toggle", func(c *gosocketio.Channel, msg channels.SocketMessage) {

		// sanitize: accept only "0" or "1"
		data := msg.Data
		if data != "0" && data != "1" {
			data = "0"
		}

		// RS232 / ECS SAFETY INTERLOCK:
		// RS232 cannot be enabled while ECS is OFF.
		if data == "1" && settings.GetDriverSettings("A").ECS != 1 {
			logger.Warn("[RS232-SAFETY] RS232 enable rejected because ECS is disabled")

			// Keep runtime and persisted state OFF.
			rs232State = 0
			executors.RS232Enabled.Store(false)
			if err := settings.SaveRS232Data("0"); err != nil {
				logger.Error("[RS232-SAFETY] Failed to persist RS232 OFF:", err)
			}

			// Force every UI switch back to OFF.
			for _, cl := range connectedClients.Clients {
				cl.Channel.Emit("rs232_status", rs232Status{Data: "0"})
			}

			channels.SendAlarm("Cannot enable RS232. ECS is OFF. Set ECS = 1 and press SAVE.")
			return
		}

		if data == "1" {
			rs232State = 1
			executors.RS232Enabled.Store(true)
			logger.Info("RS232 state updated to: 1 (ENABLED)")
		} else {
			rs232State = 0
			executors.RS232Enabled.Store(false)
			logger.Info("RS232 state updated to: 0 (DISABLED)")
		}

		// Persist to disk so reboot remembers.
		if err := settings.SaveRS232Data(data); err != nil {
			logger.Error("Failed to save RS232 status to disk:", err)
		}

		// Broadcast updated status to all connected clients.
		for _, cl := range connectedClients.Clients {
			cl.Channel.Emit("rs232_status", rs232Status{Data: data})
		}
	})
}

func executeProgram(fileName string) {
	err := executors.RunCodeFile(helper.GetCodeFilePath() + "/" + fileName)
	if err != nil {
		logger.Error(err)
		channels.SendAlarm(err.Error())
	}
}

func uiBradcastMessageListner() {
	for {
		msg := <-channels.BroadCastUIChannel
		if len(connectedClients.Clients) >= 0 {
			if msg.Alarm == "" {
				go Send(msg)
			} else {
				go sendAlarm(msg)
			}
		}
	}
}

func removeClient(id string) {
	for i, client := range connectedClients.Clients {
		if client.ID == id {
			connectedClients.Clients = append(connectedClients.Clients[:i], connectedClients.Clients[i+1:]...)
			logger.Debug("removed disconnected client from collection", id)
			break
		}
	}
}

func Send(message channels.SocketMessage) {
	for _, client := range connectedClients.Clients {
		client.Channel.Emit(message.Event, message)
	}
}

func sendAlarm(message channels.SocketMessage) {
	if !strings.Contains(message.Alarm, "No Alarms") {
		channels.WriteCommandExecInput("stop_prog_exec", "")
	}
	logger.Trace("send alarm to ui", message.Alarm)
	for _, client := range connectedClients.Clients {
		client.Channel.Emit(message.Event, message.Alarm)
	}
}

func emitCurrentDriveAlarm(c *gosocketio.Channel) {
	currentAlarm := motor.GetCurrentAlarm()
	errCode := motor.GetCurrentErrorCode()
	displayCode := errCode
	if errCode>>8 == 0xFF {
		displayCode = errCode & 0xFF
	}

	c.Emit("alarm_state", currentAlarmStatus{
		Alarm:       currentAlarm,
		Code:        displayCode,
		FaultActive: currentAlarm != "" && !strings.Contains(currentAlarm, "No Alarms"),
	})
	c.Emit("alarm_error", currentAlarm)

	if errCode != 0 {
		c.Emit("driver_error", errCode)
		c.Emit("drive_error_code", struct {
			Code int `json:"code"`
		}{Code: displayCode})
	}
}

/*
	custom events listen by ui client
	--------------------------------------
	reset_alert
	pos_data: destination postion
	sent_file_cont: sent the file content of program
	reset_done
	destination_position  (tcpserver.js line# 415 ui_clients[i].emit("destination_position",{"pos":p+(mp.factor_backlash*mp.drive_backlash)-pe_val});)
	alarm_error
	FINSIGNAL
	alarms
	ref_complete  (updateClients("ref_complete",{'ref':'complete'});)
	ETH_DOWN  updateClients('ETH_DOWN', "Ethercat communication down...");
	ETH_UP  updateClients('ETH_UP', "Ethercat communication up...");

	custom events listen by server
	--------------------------------------
	set_program_mode
	program
	status
	stop_execution
	updateSettings
	reset
	resetMultiTurn
	emergency
	execute
	get_file_cont
	line_number
	line_complete
	enable_step_mode
	step_mode
	jog_mode
	start_homing
	stop_homing
	homing_complete
	pos_data
	destination_position
	exec_next_line
	enable_ecs
	disable_ecs
	goToZero
	pot_hard_limit
	not_hard_limit
*/
