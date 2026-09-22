package ethercatdevicedatatypes

// Device struct holds the details of the connected device.
type Device struct {
	Name              string `yaml:"name"`
	VendorID          int    `yaml:"vendor-id"`
	ProductCode       int    `yaml:"product-code"`
	Alias             int    `yaml:"alias"`
	ID                int    `yaml:"id"`
	IsReady           bool
	AddressConfigName string  `yaml:"address-config-name"`
	AddressConfigFile string  `yaml:"address-config-file"`
	PotNotThreshold   float64 `yaml:"pot-not-threshold"`
	StopWhenHWPOTNOT  bool    `yaml:"stop-when-hardware-potnot"`
	IOPollingInterval int     `yaml:"io-poll-interval"`
	DriveType         string  `yaml:"drive-type"`

	// RPMConst and DriveXRatio are NOT read from device-configuration.yml.
	// They are injected at runtime by auto-discovery from the drive YAML
	// motion: section. Do not add yaml tags here — values set in YAML will
	// be silently overwritten by the injection and cause confusion.
	RPMConst    int // encoder counts per RPM — drive-specific, from drive YAML
	DriveXRatio int // encoder counts per output shaft revolution — drive-specific, from drive YAML
}

// Devices holds the array of device configured
type Devices struct {
	Device []Device `yaml:"devices"`
}

// DeviceConfig holds the Devices Array
type DeviceConfig struct {
	Devices Devices
}
