package channels

//DriverStatus used for transferring the current status of the driver, it can be position data or alarms
type DriverStatus struct {
	//Name of the driver for e.g. A or B etc
	DriveName string
	//Data for eg postion or current alarm etc
	Data string
	//Event denotes what kind of event, for e.g. current_postion, alarm etc
	Event string
	//Description any detailed description of the current status
	Description string
}

//BroadCastDriveStatusChannel channel used by module like driverStatusKeeper to keep the current status
//of the driver, for e.g. current position, current alarms etc
var BroadCastDriveStatusChannel chan DriverStatus
