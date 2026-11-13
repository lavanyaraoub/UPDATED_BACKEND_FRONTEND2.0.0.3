package systemupdate

import (
	"EtherCAT/channels"
	"EtherCAT/logger"
	"EtherCAT/motordriver"
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var isUpdateInProgress bool

const (
	rtcUpdateRoot  = "/home/pi/rtc_updates"
	releasesRoot   = "/mnt/app/releases"
	installPath    = "/mnt/app/current"
	otaScriptPath  = "/usr/local/lib/jamun-ota/apply_ota.sh"
	updateLockPath = "/home/pi/rtc_updates/update.lock"
)

func isAnUpdateInProgress() bool {
	return isUpdateInProgress
}

// PerformSystemUpdate — called when user clicks UPDATE.
// Step 1: Stop EtherCAT driver
// Step 2: Launch apply_ota.sh → systemctl restart → pre_start_ota.sh replaces files
// Step 3: Notify customer to power cycle
func PerformSystemUpdate(sendUINotification bool) {
	if isUpdateInProgress {
		logger.Info("Update already in progress, ignoring request")
		return
	}

	logger.Info("Starting staged system update...")
	isUpdateInProgress = true

	filename := readFileNameOfUpdate()
	if filename == "" {
		logger.Info("No staged update found")
		isUpdateInProgress = false
		if sendUINotification {
			channels.SendAlarm("No update staged. Please check for updates first.")
		}
		return
	}

	// Auto-remove stale lock older than 10 minutes
	if info, err := os.Stat(updateLockPath); err == nil {
		if time.Since(info.ModTime()) > 10*time.Minute {
			logger.Info("Stale update lock detected, removing...")
			os.Remove(updateLockPath)
		}
	}

	lockFile, err := os.OpenFile(updateLockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		logger.Info("Update lock exists — update already started. Skipping duplicate run.")
		isUpdateInProgress = false
		return
	}
	lockFile.WriteString(time.Now().Format(time.RFC3339))
	lockFile.Close()

	stagePath := filepath.Join(rtcUpdateRoot, "staging", strings.TrimSuffix(filename, ".tar.gz"))

	logger.Info("Staged update file: ", filename)
	logger.Info("Staged update path: ", stagePath)
	logger.Info("Install target path: ", installPath)
	logger.Info("OTA script path: ", otaScriptPath)

	// Validate staged folder
	if _, err := os.Stat(stagePath); os.IsNotExist(err) {
		logger.Error("Staged folder missing: ", stagePath)
		if sendUINotification {
			channels.SendAlarm("Update failed: staged folder missing. Please check for updates again.")
		}
		os.Remove(updateLockPath)
		isUpdateInProgress = false
		return
	}

	// Validate jamun binary
	if _, err := os.Stat(filepath.Join(stagePath, "jamun")); os.IsNotExist(err) {
		logger.Error("Staged jamun binary missing")
		if sendUINotification {
			channels.SendAlarm("Update failed: staged binary missing. Please check for updates again.")
		}
		os.Remove(updateLockPath)
		isUpdateInProgress = false
		return
	}

	// Validate OTA script
	if _, err := os.Stat(otaScriptPath); os.IsNotExist(err) {
		logger.Error("OTA apply script missing: ", otaScriptPath)
		if sendUINotification {
			channels.SendAlarm("Update failed: OTA script missing.")
		}
		os.Remove(updateLockPath)
		isUpdateInProgress = false
		return
	}

	if err := os.Chmod(otaScriptPath, 0750); err != nil {
		logger.Error("Cannot chmod OTA script: ", err)
		if sendUINotification {
			channels.SendAlarm("Update failed: cannot prepare OTA script.")
		}
		os.Remove(updateLockPath)
		isUpdateInProgress = false
		return
	}

	// STEP 1 — Stop EtherCAT driver with 5s timeout
	if motordriver.HasDriverConnected() {
		logger.Info("Stopping EtherCAT driver before OTA...")
		done := make(chan struct{})
		go func() {
			motordriver.StopSystem()
			close(done)
		}()
		select {
		case <-done:
			logger.Info("EtherCAT driver stopped cleanly")
		case <-time.After(5 * time.Second):
			logger.Error("EtherCAT stop timed out after 5s — proceeding anyway")
		}
		time.Sleep(1 * time.Second)
	}

	// STEP 2 — Launch apply_ota.sh in background
	logger.Info("Starting OTA apply script in background...")
	cmd := exec.Command("bash", otaScriptPath, stagePath)
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		logger.Error("Failed to start OTA apply script: ", err)
		if sendUINotification {
			channels.SendAlarm("Update failed: unable to launch OTA script.")
		}
		os.Remove(updateLockPath)
		isUpdateInProgress = false
		return
	}

	logger.Info("OTA apply script started. PID: ", cmd.Process.Pid)

	// STEP 3 — Notify customer
	if sendUINotification {
		channels.SendAlarm("Update started. Restart/Reboot the System to apply Updates.")
	}

	// Give apply_ota.sh time to call systemctl restart before jamun exits
	time.Sleep(3 * time.Second)

	isUpdateInProgress = false
	logger.Info("Exiting jamun so systemd can restart with new version...")
	os.Exit(2)
}

func readFileNameOfUpdate() string {
	file, err := os.Open(filepath.Join(rtcUpdateRoot, "current_update"))
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Name") {
			parts := strings.Split(line, ":")
			if len(parts) > 1 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

func SendBootSuccessNotification() {
	logger.Info("System boot successful.")
}

func applyUpdate(stagePath string) error {
	return fmt.Errorf("applyUpdate is disabled; handled by %s", otaScriptPath)
}
