#ifndef ETHERCATINTERFACE_H
#define ETHERCATINTERFACE_H

#include "ecrt.h"
#include <stdint.h>
#include <stdlib.h>

/* ============================================================
 * MAX_SLAVES — maximum slaves per master.
 * ============================================================ */
#define MAX_SLAVES 8

/* ============================================================
 * SlaveOffsets — per-slave PDO byte offset table.
 *
 * IgH fills these at ecrt_domain_reg_pdo_entry_list() time.
 * Each field is the byte offset of that object within the flat
 * domain process-data image returned by ecrt_domain_data().
 *
 * Fields that a drive does not map are left as 0.
 * The 'valid' flag is set to 1 after successful registration.
 *
 * INTEGRATED: Added op_mode_disp, touch_stat, touch_pos1,
 * following_err from the SDO-PDO build's Minas A6 TxPDO map.
 * These are YAML-driven — they are only populated for drives
 * that include those objects in their pdo: section.
 * ============================================================ */
typedef struct {
    /* RxPDO (output to drive) */
    unsigned int ctrl_word;    /* 0x6040  Controlword          (U16) */
    unsigned int op_mode;      /* 0x6060  Modes of operation   (S8)  */
    unsigned int target_pos;   /* 0x607A  Target position      (S32) */
    unsigned int target_vel;   /* 0x60FF  Target velocity      (S32) */
    unsigned int dig_out_mask; /* 0x60FE:01 Digital output mask(U32) — A6 */
    unsigned int dig_out_val;  /* 0x60FE:02 Digital output val (U32) — A6 */

    /* TxPDO (input from drive) */
    unsigned int error_code;   /* 0x603F  Error code           (U16) */
    unsigned int status_word;  /* 0x6041  Statusword           (U16) */
    unsigned int op_mode_disp; /* 0x6061  Op mode display      (S8)  — INTEGRATED from SDO build */
    unsigned int actual_pos;   /* 0x6064  Position actual      (S32) */
    unsigned int actual_vel;   /* 0x606C  Velocity actual      (S32) — Delta ASDA */
    unsigned int touch_stat;   /* 0x60B9  Touch probe status   (U16) — INTEGRATED from SDO build */
    unsigned int touch_pos1;   /* 0x60BA  Touch probe pos 1    (S32) — INTEGRATED from SDO build */
    unsigned int following_err;/* 0x60F4  Following error      (S32) — INTEGRATED from SDO build */
    unsigned int digital_in;   /* 0x60FD/0x4F25 Digital inputs (U32) */

    int          valid;        /* 1 after successful registration */
} SlaveOffsets;

/* ============================================================
 * PdoEntrySpec — one PDO entry passed from Go to the C engine.
 *
 * Go populates these from the YAML pdo: section and passes an
 * array of them to register_slave_pdos().
 *
 * name[32]: maps to a SlaveOffsets field (see SlaveOffsets above).
 *   Names starting with '_' are padding — registered with IgH
 *   to maintain byte alignment but their offsets are discarded.
 * ============================================================ */
typedef struct {
    uint16_t index;
    uint8_t  subindex;
    uint8_t  bits;
    char     name[32];   /* null-terminated, maps to SlaveOffsets field */
} PdoEntrySpec;

/* ============================================================
 * Master / SDO helpers
 * ============================================================ */
ec_master_t *requestMaster(int index);

int sdo_upload(ec_master_t *master, uint16_t slave_position,
               uint16_t index, uint8_t subindex,
               uint8_t *data, size_t data_size, uint32_t *abort_code);

int sdo_download(ec_master_t *master, uint16_t slave_position,
                 uint16_t index, uint8_t subindex,
                 uint8_t *data, size_t data_size, uint32_t *abort_code);

int drivePosition(ec_master_t *master, uint16_t slave_position,
                  uint16_t index, uint8_t subindex, uint8_t data);

/* sdo_upload2 — RETAINED from HAL build (present in c-header).
 *
 * NOTE: The SDO-PDO build deliberately removed this function in 2026-05
 * because it had two bugs: hard-coded uint32 buffer regardless of data_size,
 * and a discarded return code that masked errors.
 *
 * It is retained here because the HAL build's header declares it and
 * existing Go code calling C.sdo_upload2 through the HAL CGo layer may
 * still reference it. Callers should prefer sdo_upload() for new code.
 *
 * If no Go code references C.sdo_upload2 in the HAL layer, this
 * declaration (and the matching implementation) can be safely removed.
 */
int sdo_upload2(ec_master_t *master, uint16_t slave_position,
                uint16_t index, uint8_t subindex,
                uint8_t data, size_t data_size);

/* ============================================================
 * Generic PDO registration engine (multi-drive, YAML-driven)
 *
 * register_slave_pdos():
 *   Replaces configure_minas_a6_pdos / configure_delta_asda2e_pdos /
 *   setup_domain_sizing / setup_delta_domain_sizing entirely.
 *
 *   Steps performed internally:
 *   1. Builds ec_pdo_entry_info_t[] from rx_entries/tx_entries.
 *   2. Builds ec_pdo_info_t[2] and ec_sync_info_t[5].
 *   3. Calls ecrt_slave_config_pdos().
 *   4. Disables sync manager watchdog (prevents Error 80 on A6
 *      when Linux pauses the Go process for background tasks).
 *   5. Builds ec_pdo_entry_reg_t[] using name→SlaveOffsets mapping.
 *   6. Calls ecrt_domain_reg_pdo_entry_list().
 *   7. Fills slave_offsets[slave_index].
 *
 * Parameters:
 *   master/domain  — IgH master and domain handles.
 *   alias/position — slave addressing (alias=0 for most configs).
 *   vendor_id/product_code — must match the physical drive.
 *   slave_index    — MasterDevice.Position (0, 1, 2 ...).
 *   rx_entries     — array of PdoEntrySpec for output SM (RxPDO).
 *   rx_count       — number of entries in rx_entries.
 *   tx_entries     — array of PdoEntrySpec for input SM (TxPDO).
 *   tx_count       — number of entries in tx_entries.
 *   rx_pdo_index   — PDO assignment index for RxPDO (e.g. 0x1600).
 *   tx_pdo_index   — PDO assignment index for TxPDO (e.g. 0x1A00).
 *
 * Returns 0 on success, negative errno on failure.
 * ============================================================ */
int register_slave_pdos(ec_master_t  *master,
                        ec_domain_t  *domain,
                        ec_slave_config_t *sc,
                        int           slave_index,
                        PdoEntrySpec *rx_entries, int rx_count,
                        PdoEntrySpec *tx_entries, int tx_count,
                        uint16_t      rx_pdo_index,
                        uint16_t      tx_pdo_index,
                        uint16_t      alias,
                        uint16_t      position,
                        uint32_t      vendor_id,
                        uint32_t      product_code);

/*
 * get_slave_offsets — copies all computed byte offsets for slave_index
 * into *out. Call after register_slave_pdos() succeeds.
 * Returns 0 on success, -EINVAL if the slot is not valid.
 */
int get_slave_offsets(int slave_index, SlaveOffsets *out);

/* ============================================================
 * Legacy single-slave PDO offset accessors — INTEGRATED from SDO-PDO build.
 *
 * These functions existed in the SDO-PDO build to expose individual
 * PDO offsets to Go without returning the full SlaveOffsets struct.
 * In the multi-drive HAL they are re-implemented as thin wrappers
 * around get_slave_offsets(slave_index, ...).
 *
 * All accept slave_index (0-based) as their first parameter so they
 * work for any drive on the bus.
 *
 * Callers that previously passed (domain, alias, position, vendor_id,
 * product_code, &offset) now pass (slave_index, &offset).
 * ============================================================ */

/* get_digital_output_offsets — retrieves 0x60FE:01/02 byte offsets.
 * Must be called after register_slave_pdos() for slave_index. */
int get_digital_output_offsets(int slave_index,
                               unsigned int *off_mask,
                               unsigned int *off_val);

/* Individual TxPDO offset getters — thin wrappers for Go convenience. */
int get_pos_pdo_offset     (int slave_index, unsigned int *off_pos);
int get_statusword_offset  (int slave_index, unsigned int *off_status);
int get_error_code_offset  (int slave_index, unsigned int *off_error);
int get_actual_vel_offset  (int slave_index, unsigned int *off_vel);
int get_digital_in_offset  (int slave_index, unsigned int *off_di);

/* INTEGRATED: touch probe and following-error offsets (Minas A6 TxPDO).
 * Return -EINVAL if the drive's YAML did not include those objects. */
int get_touch_stat_offset  (int slave_index, unsigned int *off_touch_stat);
int get_touch_pos1_offset  (int slave_index, unsigned int *off_touch_pos1);
int get_following_err_offset(int slave_index, unsigned int *off_fe);
int get_op_mode_disp_offset(int slave_index, unsigned int *off_omd);

/* RxPDO offset getter — returns all four write-side offsets at once. */
int get_all_rx_pdo_offsets(int slave_index,
                           unsigned int *off_controlword,
                           unsigned int *off_opmode,
                           unsigned int *off_target_pos,
                           unsigned int *off_target_vel);

/* ============================================================
 * Async SDO Request API — Multiturn reset (A6, per-slave)
 * ============================================================ */
int create_mt_sdo_requests(ec_slave_config_t *sc, int slave_index);
int trigger_mt_request_step(int slave_index, int step, uint32_t value);
int get_mt_request_state(int slave_index, int step);

/* ============================================================
 * Async SDO Request — Profile Velocity 0x6081 (per-instance)
 * Go holds the void* pointer — no slave_index needed.
 * ============================================================ */
void* create_profile_vel_sdo_request(ec_slave_config_t *sc);
int   trigger_profile_vel_request(void *req_ptr, uint32_t value);
int   get_profile_vel_state(void *req_ptr);

/* ============================================================
 * DC clock synchronisation — call every cyclic tick.
 *
 * Wraps ecrt_master_application_time(), ecrt_master_sync_reference_clock(),
 * and ecrt_master_sync_slave_clocks(). Required when any slave is
 * configured with ecrt_slave_config_dc(). Safe (no-op overhead) when
 * no DC slaves are configured.
 *
 * Must be called AFTER ecrt_domain_queue() and BEFORE ecrt_master_send().
 * ============================================================ */
void sync_dc_clocks(ec_master_t *master);

/* ============================================================
 * PDO read/write helpers
 * ============================================================ */
int32_t  read_s32(uint8_t *domain_pd, unsigned int offset);
int16_t  read_s16(uint8_t *domain_pd, unsigned int offset);
uint16_t read_u16(uint8_t *domain_pd, unsigned int offset);
uint32_t read_u32(uint8_t *domain_pd, unsigned int offset);
int8_t   read_s8 (uint8_t *domain_pd, unsigned int offset);

void write_u16(uint8_t *domain_pd, unsigned int offset, uint16_t value);
void write_s16(uint8_t *domain_pd, unsigned int offset, int16_t value);
void write_s32(uint8_t *domain_pd, unsigned int offset, int32_t value);
void write_u32(uint8_t *domain_pd, unsigned int offset, uint32_t value);
void write_s8 (uint8_t *domain_pd, unsigned int offset, int8_t value);

/* ============================================================
 * Size helpers
 * ============================================================ */
size_t uint16Size(void);
size_t uint32Size(void);
size_t uint8Size(void);
size_t unintSize(void);
size_t int32Size(void);
size_t int16Size(void);
size_t int8Size(void);

const char* ec_strerror(int err);

/* ================================================================
 * Bus Scanning
 *
 * BusSlaveInfo holds the identity of one EtherCAT slave as reported
 * by ecrt_master_get_slave(). Populated by scan_bus() and passed
 * back to Go for comparison against device-configuration.yml.
 * ================================================================ */
#define BUS_SLAVE_NAME_LEN 64

typedef struct {
    uint16_t position;
    uint16_t alias;
    uint32_t vendor_id;
    uint32_t product_code;
    char     name[BUS_SLAVE_NAME_LEN];
} BusSlaveInfo;

/*
 * scan_bus — queries the IgH master for every slave on the bus.
 * Safe to call before ecrt_master_activate() — read-only query.
 *
 * Returns >= 0 (slave count) on success, negative errno on failure.
 */
int scan_bus(ec_master_t *master, BusSlaveInfo *out, int max_count);

#endif /* ETHERCATINTERFACE_H */
