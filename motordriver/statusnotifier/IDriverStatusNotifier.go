package statusnotifier

type IOStatus struct {
	DriveName string `json:"drive_name"`
	ECS       bool   `json:"ecs"`
	FIN       bool   `json:"fin"`
	SOLOP     bool   `json:"sol_op"`
	CL        bool   `json:"cl"`
	DCL       bool   `json:"dcl"`
	ALMIN     bool   `json:"alm_in"`
	ALMOUT    bool   `json:"alm_out"`
	HOME      bool   `json:"home"`
	POT       bool   `json:"pot"`
	NOT       bool   `json:"not"`
}

//IDriverStatusNotifier interface definitation to communicate with clients
type IDriverStatusNotifier interface {
	//CurrentPosition notify the current position of the motor
	CurrentPosition(driveName string, position float64)

	//DestinationPosition notify the destination position motor should rotate
	DestinationPosition(driveName string, destPosition float32)

	Alarm(alarm string)

	SocketMessage(event string, msg string)

	DriverError(errorCode int)

	DriverStatus(driveName, status string)

	NotifyIOStatus(ioStat IOStatus)
}
