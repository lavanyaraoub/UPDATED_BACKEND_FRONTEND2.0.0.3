package motordriver

// polledDrivers tracks which IMotorDriver instances were started by pollIOStat
// so stopPollIOStat can stop each one correctly.
//
// WHY PER-DEVICE DISPATCH:
//   The old version called GetMotorDriver() (global) and passed ALL devices to
//   a single pollIOStat call. In a mixed-drive setup (e.g. A6 on Axis A, Delta
//   on Axis B) the global driver was whichever type was configured last —
//   meaning every device's I/O was read using the wrong driver's bit-mapping.
//
//   Fix: each device is dispatched individually to its own Driver.pollIOStat()
//   so each axis uses the correct register layout and active-HIGH/LOW conventions
//   for its specific hardware.
var polledDrivers []IMotorDriver

func pollIOStat(availableDevices []*MasterDevice) {
	polledDrivers = nil
	for _, dev := range availableDevices {
		if dev == nil || dev.Driver == nil {
			continue
		}
		// Pass only this device to its own driver's pollIOStat.
		dev.Driver.pollIOStat([]*MasterDevice{dev})
		polledDrivers = append(polledDrivers, dev.Driver)
	}
}

func stopPollIOStat() {
	for _, driver := range polledDrivers {
		driver.stopPollIOStat()
	}
	polledDrivers = nil
}