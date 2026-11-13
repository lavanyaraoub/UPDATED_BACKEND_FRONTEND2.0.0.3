# RTC Sumit Accuracy + Lavanya UI/RS232 Merge

## Merge contract

This project is based on `SUMIT POS LOGIC` and deliberately keeps Sumit's
positioning, tolerance, backlash, PDO, homing, drive-discovery, executor, and
machine configuration logic as the authority.

Only these integration areas were added:

- Lavanya's active React Native Web build in `www_v2/`.
- The matching frontend source snapshot in `frontend-src/`. The 36 bundled
  application source files were checked byte-for-byte against the active
  `main.374c1782.js.map` source contents.
- Frontend socket contracts for RS232 status, RS232/ECS operator messages,
  shared jog-feed display, and per-command `jog_feed`.
- FANUC RS232 frame cleanup and strict command validation.
- RS232/ECS safety interlocks in the socket and REST settings paths.
- A minimal optional jog-feed adapter inside Sumit's existing `ManualJog`.

## Protected Sumit behavior

The merge did **not** import Lavanya's alternate versions of:

- `motordriver/move_to_degree.go`
- `motordriver/drive_angle_manipulation.go`
- `motordriver/driver_status_keeper.go`
- `motordriver/pdo_cyclic_task.go`
- `motordriver/zero_reference.go`
- `helper/position_finder.go`
- `executors/command_executor.go`
- drive YAML files, machine settings, G/M-code programs, OTA, or rollback code

Within `motordriver/drive_rotation.go`, Sumit's position tolerance, settling,
position goal, final target validation, and backlash direction commit remain
unchanged. Only `ManualJog` accepts an optional UI speed value. Legacy callers
still use Machine Parameters > Jog Feed.

Within `motordriver/driver_action_listener.go`, only the `MANUAL_JOG` case was
adapted to validate and forward that optional speed value. Sumit's step-mode,
multiturn reset, home-calibration, settings-change, emergency, and program
handling remain unchanged.

## RS232 behavior after this merge

- `B 10`, `B    10`, and `B10` normalize to the same `B10;` command.
- NUL/control padding and the observed FANUC `&HE:` prefix are removed.
- Axis/feed values must be numeric; malformed or unknown tokens are rejected.
- Invalid commands raise `Invalid RS232 command` instead of silently waiting.
- RS232 commands are ignored while the RS232 switch is OFF.
- RS232 cannot be enabled while ECS is OFF.
- ECS cannot be disabled while RS232 is active.
- The serial listener runs in a goroutine, so GPIO startup and shutdown handling
  are no longer blocked by a successfully opened serial port.
- The RS232 enabled flag remains the operator-selected runtime state after a
  program finishes; execution no longer silently turns that switch OFF.
- The cgo linker uses the project directory (`${SRCDIR}`) instead of a hardcoded
  `/home/pi/gosrc/src/EtherCAT` path, so the merged source can be built from a
  renamed extraction directory.

## Raspberry Pi validation

Run from the extracted project directory on the Raspberry Pi with the IgH
EtherCAT development files and Go toolchain installed:

```bash
source ~/.bashrc
source ~/.profile

go test ./serialtest ./clientcommunication ./restapi ./motordriver
go test -tags=integration ./restapi

make clean || true
make all
make exec
```

Before live motion, validate in this order:

1. Start with the drive disabled or mechanics safely isolated.
2. Confirm the UI loads from port 8000 and REST remains on port 5000.
3. With ECS=0, verify RS232 ON is rejected and the message is visible.
4. Set ECS=1, enable RS232, and verify `B 10` produces `B10;` in `gm_codes/FILES`.
5. Disable RS232 and verify incoming serial text does not alter `FILES`.
6. Test hold-to-jog at low speed and confirm STOP on release.
7. Run the established `0 -> 10 -> 0` indexing test five times and compare final
   pulse error/backlash with the unmodified Sumit build before production use.

Hardware-in-the-loop motion was not run while preparing this merge. Do not
replace the live controller without a backup and the staged checks above.
