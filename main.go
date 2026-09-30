package main

/*
#cgo CFLAGS: -g -Wall -I/opt/etherlab/include
#cgo LDFLAGS: -L${SRCDIR} -lethercatinterface
*/
import "C"
import (
	"EtherCAT/channels"
	socket "EtherCAT/clientcommunication"
	"EtherCAT/constants"
	executors "EtherCAT/executors"
	"EtherCAT/helper"
	"EtherCAT/hotspot"
	"EtherCAT/licensechecker"
	"EtherCAT/logger"

	//"runtime"
	motor "EtherCAT/motordriver"
	"EtherCAT/restapi"
	settings "EtherCAT/settings"
	"EtherCAT/systemupdate"
	"EtherCAT/tunnel"

	//"EtherCAT/ren"
	"EtherCAT/serialtest"
	"EtherCAT/webserver"
	"bytes"
	"fmt"
	_ "net/http/pprof"
	"time"

	//"net/http"
	//"log"
	gpiohandler "EtherCAT/gpiohandle"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	//"encoding/binary"
	"text/tabwriter"
)

// Version of the program
var Version = constants.SystemVersion

// RelChannel release channel of the program
var RelChannel = "v1"

// LogLevel tells what level of logs should be logged
var LogLevel = "TRACE"

var BuildTime string

var wait chan bool

func main() {
	envSetting := settings.GetEnvSettings()
	LogLevel = envSetting.LogLevel
	RelChannel = envSetting.ReleaseChannel
	logger.Init(LogLevel)
	// ---- RS232: restore persisted toggle state on boot ----
	rsData, loadErr := settings.LoadRS232Data()
	if loadErr != nil {
		logger.Error("Failed to load RS232 status from disk:", loadErr)
	} else {
		if rsData == "1" {
			executors.SetRS232Enabled(true)
			logger.Info("RS232 status restored: 1 (ENABLED)")
		} else {
			executors.SetRS232Enabled(false)
			logger.Info("RS232 status restored: 0 (DISABLED)")
		}
	}

	if (len(os.Args)) > 1 {
		sysArg := strings.ToLower(os.Args[1])
		exit := processSytemParm(sysArg)
		if exit {
			return
		}
	}

	logger.Info("-------------------------------------------------------------------------------------")
	logger.Info("Starting Ethercat Driver Controller ...")
	logger.Info("Version:", Version)
	logger.Info("Build time:", BuildTime)
	logger.Info("Release channel:", RelChannel)
	logger.Info("Log level: ", LogLevel)

	if envSetting.HotSpotOnStart {
		logger.Info("starting hotspot")
		h := hotspot.NewHotspot()
		h.Create()
	}
	setupCloseHandler()

	_ = settings.LoadDriverSettings()
	logger.Info("loaded driver settings")

	go socket.Start()

	licErr := licensechecker.CheckLicense(true)
	if licErr != nil {
		logger.Error(licErr)
		return
	}

	execErr := executors.Initialize()

	if execErr != nil {
		logger.Error(execErr)
		logger.Error("Exiting the system ...........")
		os.Exit(0)
	}

	err := motor.InitMaster()
	if err != nil {
		logger.Error("EtherCAT initialization failed:", err)
		logger.Error("Exiting Jamun so systemd can retry EtherCAT initialization")
		os.Exit(1)
	}

	w := webserver.NewWebUI()
	go w.Start(envSetting.UIVer)

	a := restapi.NewApi()
	go a.Start()
	go systemupdate.CheckRollbackSuccess()
	go runStartScript()

	// Start the RS-232 listener without blocking GPIO startup or shutdown handling.
	go serialtest.StartSerialListener()

	// 🔹 START GPIO HANDLER HERE
	go startGPIOHandler()

	// BPF Modbus poller is started inside motor.InitMaster() via initListeners()
	// No separate startModbusHandler needed here.

	//wait for done signal from closeHandler
	<-wait
}

func processSytemParm(sysParm string) bool {
	if sysParm == "-h" {
		w := tabwriter.NewWriter(os.Stdout, 1, 1, 1, ' ', 0)
		fmt.Fprintln(w, "-v\t", "Display version and other environment settings")
		fmt.Fprintln(w, "-s\t", "Check for system update and download if any update available")
		fmt.Fprintln(w, "-u\t", "Update the system to latest version")
		fmt.Fprintln(w, "-c\t", "Remove log, license and backup files")
		fmt.Fprintln(w, "-l\t", "Download license file")
		fmt.Fprintln(w, "--hotspot-down\t", "Kill hotspot")
		fmt.Fprintln(w, "--hotspot-up\t", "Create hotspot")
		fmt.Fprintln(w, "--tunnel-up\t", "Create remote tunnel")
		fmt.Fprintln(w, "--tunnel-down\t", "Stop remote tunnel")
		fmt.Fprintln(w, "--pdo-pos\t", "Print drive position using TxPDO (0x6064:0) without altering normal flow")
		fmt.Fprintln(w, "--scan-bus\t", "Scan EtherCAT bus and print device-configuration.yml template for each discovered slave")
		w.Flush()
		return true
	} else if sysParm == "-v" {
		s, _ := helper.ReadSerialNumber()
		w := tabwriter.NewWriter(os.Stdout, 1, 1, 1, ' ', 0)
		fmt.Fprintln(w, "Version\t:", Version)
		fmt.Fprintln(w, "Release channel\t:", RelChannel)
		fmt.Fprintln(w, "Loglevel\t:", LogLevel)
		fmt.Fprintln(w, "Serial#\t:", s)
		w.Flush()
		return true
	} else if sysParm == "-s" {
		systemupdate.CheckforUpdates(false)
		return true
	} else if sysParm == "-u" {
		systemupdate.PerformSystemUpdate(false)
		return true
	} else if sysParm == "-l" {
		os.Remove(helper.AppendWDPath("/license.key"))
		os.Remove(helper.AppendWDPath("/license.lic"))
		licErr := licensechecker.CheckLicense(true)
		if licErr != nil {
			logger.Error(licErr)
		}
		return true
	} else if sysParm == "-c" {
		fmt.Println("Starting file cleanup...")
		fmt.Println("Remove log files")
		os.RemoveAll(helper.AppendWDPath("/log"))
		fmt.Println("Remove license file")
		os.Remove(helper.AppendWDPath("/license.key"))
		os.Remove(helper.AppendWDPath("/license.lic"))
		fmt.Println("Remove system backup files")
		os.RemoveAll("/home/pi/jamun_backup")
		fmt.Println("File cleanup completed")
		return true
	} else if sysParm == "--hotspot-down" {
		h := hotspot.NewHotspot()
		h.Kill()
		return true
	} else if sysParm == "--hotspot-up" {
		h := hotspot.NewHotspot()
		h.Create()
		return true
	} else if sysParm == "--tunnel-up" {
		t := tunnel.NewTunnel()
		u, err := t.StartHTTP()
		if err != nil {
			logger.Error(err)
		} else {
			logger.Info("Remote access url", u)
		}
		return true
	} else if sysParm == "--tunnel-down" {
		t := tunnel.NewTunnel()
		t.Stop()
		return true
	} else if sysParm == "--pdo-pos" {
		logger.Info("PDO position is printed automatically during runtime now (integrated mode).")
		return true
	} else if sysParm == "--scan-bus" {
		motor.RunBusScan()
		return true
	}
	return false
}

func setupCloseHandler() {
	c := make(chan os.Signal, 1)
	wait = make(chan bool, 1)
	// SIGINT: Ctrl+C, SIGTERM: service/process stop, SIGHUP: terminal/VS Code close.
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	logger.Info("[LIFECYCLE] shutdown handler v3 active (SIGINT/SIGTERM/SIGHUP)")

	go func() {
		sig := <-c
		logger.Info("[LIFECYCLE] v3 received signal:", sig, "starting full shutdown...")
		signal.Stop(c)
		motor.StopBPFPoller()

		shutdownDone := make(chan struct{})
		go func() {
			// Stop every high-level listener before releasing the EtherCAT
			// master. Calling ShutdownMasters directly leaves I/O/ECS workers
			// alive and they can repeatedly issue SDO reads against a released
			// master ("Bad file descriptor") while the process is exiting.
			motor.StopSystem()
			close(shutdownDone)
		}()

		select {
		case <-shutdownDone:
			logger.Info("Graceful hardware shutdown complete.")
		case <-time.After(4 * time.Second):
			logger.Warn("Shutdown timed out! Forcing master release...")
			motor.ForceReleaseMaster()
		}

		logger.Info("Exiting the application")
		// Cleanup has completed (or hit its bounded timeout). Exit explicitly
		// so a cgo/C worker cannot keep the make/VS Code terminal attached.
		os.Exit(0)
	}()
}

func runStartScript() {
	c := exec.Command("/bin/sh", helper.AppendWDPath("/scripts/start.sh"))
	stderr := &bytes.Buffer{}
	stdout := &bytes.Buffer{}
	c.Stderr = stderr
	c.Stdout = stdout
	if err := c.Run(); err != nil {
		logger.Error(err)
		logger.Error("error opening ui in kiosk mode")
	}
}

func startGPIOHandler() {
	cfg := gpiohandler.Config{
		InputPins:  []int{17, 22, 23},
		OutputPins: []int{},
		OnInputChange: func(pin, state int) {
			switch pin {
			case 17:
				if state == 1 {
					logger.Info("[GPIO] Start Jog")
					channels.DriverActionChannel <- channels.DriverAction{
						Action:    "MANUAL_JOG",
						Direction: 1,
					}
				} else {
					logger.Info("[GPIO] Stop Jog")
					channels.DriverActionChannel <- channels.DriverAction{
						Action: "STOP_JOG",
					}
				}

			case 22:
				if state == 1 {
					logger.Info("[GPIO] Zero reference triggered by hardware")
					channels.DriverActionChannel <- channels.DriverAction{
						Action: "ZERO_REF",
					}
				}

			case 23:
				if state == 1 {
					logger.Warn("[GPIO] Stopping program before EXECUTION...")
					channels.WriteCommandExecInput("stop_prog_exec", "")
					executors.ResetExecutingProgram()
					time.Sleep(1 * time.Second)

					motor.RefreshCurrentPosition()

					filePath := helper.GetCodeFilePath() + "/FILES"
					logger.Info("[GPIO] Executing program file:", filePath)

					go func() {
						err := executors.RunCodeFile(filePath)
						if err != nil {
							logger.Error("GPIO program execution failed:", err)
						}
					}()
				} else {
					logger.Info("[GPIO] Program button released (GPIO 23 LOW)")
				}
			}
		},
	}

	handler, err := gpiohandler.New(cfg)
	if err != nil {
		logger.Error("GPIO init failed:", err)
		return
	}
	handler.Start()
}
