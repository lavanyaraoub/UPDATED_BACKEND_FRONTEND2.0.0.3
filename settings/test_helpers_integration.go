//go:build integration

package settings

// SetDriverSettings injects a DriverSettings value for the given drive name.
// Available under both unit and integration build tags so both test tiers
// can supply deterministic settings without touching the filesystem.
func SetDriverSettings(name string, ds DriverSettings) {
	settingsMutex.Lock()
	defer settingsMutex.Unlock()
	if settingsRoot == nil {
		settingsRoot = make(SettingsRoot)
	}
	settingsRoot[name] = ds
}
