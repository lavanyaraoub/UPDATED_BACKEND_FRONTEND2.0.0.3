/*
 * ethercatinterface.c — Integrated multi-drive EtherCAT HAL
 *
 * Combines:
 *   HAL build  (EtherCAT_HAL8)       — generic multi-drive PDO engine,
 *                                       per-slave offset table, bus scan,
 *                                       DC clock sync, per-slave SDO requests.
 *   SDO-PDO build (Ethercat_sdo-pdo) — individual PDO offset accessors,
 *                                       op_mode_disp / touch probe /
 *                                       following-error field support,
 *                                       get_digital_output_offsets,
 *                                       legacy setup_*_pdo wrappers.
 *
 * Compile:
 *   gcc -o libethercatinterface.so -Wall -g -shared -fPIC ethercatinterface.c \
 *       -I/opt/etherlab/include /opt/etherlab/lib/libethercat.a
 */

#include <ecrt.h>
#include <stdio.h>
#include <stdint.h>
#include <string.h>
#include <stdlib.h>
#include <errno.h>
#include <time.h>
#include "ethercatinterface.h"

/* ── Unified per-slave offset table ─────────────────────────────────────────
 * All drives write into this same table, keyed by slave_index (0-based).
 * The 'valid' flag guards every accessor — call register_slave_pdos() first. */
static SlaveOffsets slave_offsets[MAX_SLAVES];

/* ── Async SDO request objects — multiturn reset (A6, per-slave) ─────────── */
static ec_sdo_request_t *g_req_mt_func [MAX_SLAVES];
static ec_sdo_request_t *g_req_mt_start[MAX_SLAVES];

/* ============================================================
 * Master / SDO helpers
 * ============================================================ */
ec_master_t *requestMaster(int index) {
    ec_master_t *master0 = ecrt_request_master(index);
    if (master0 == NULL) return NULL;

    /* Probe slave 0 digital-output object — non-fatal, result discarded.
     * Original behaviour retained from both builds. */
    uint32_t abortCode = 0;
    unsigned long int value = 0xFFFFFFFF;
    size_t resultSize;
    ecrt_master_sdo_upload(master0, 0, 0x60FE, 0x01,
                           (unsigned char *)&value, sizeof(value),
                           &resultSize, &abortCode);
    return master0;
}

int sdo_upload(ec_master_t *master, uint16_t slave_position,
               uint16_t index, uint8_t subindex,
               uint8_t *data, size_t data_size, uint32_t *abort_code) {
    size_t resultSize = data_size;
    return ecrt_master_sdo_upload(master, slave_position, index, subindex,
                                  (unsigned char *)data, data_size,
                                  &resultSize, abort_code);
}

int sdo_download(ec_master_t *master, uint16_t slave_position,
                 uint16_t index, uint8_t subindex,
                 uint8_t *data, size_t data_size, uint32_t *abort_code) {
    return ecrt_master_sdo_download(master, slave_position, index, subindex,
                                    (unsigned char *)data, data_size, abort_code);
}

int drivePosition(ec_master_t *master, uint16_t slave_position,
                  uint16_t index, uint8_t subindex, uint8_t data) {
    size_t res_size = 0;
    unsigned int stat = data;
    uint32_t abort_code = 0;
    int errorCode = 0;
    for (int i = 0; i < 3; i++) {
        errorCode = ecrt_master_sdo_upload(master, slave_position, index, subindex,
                                           (unsigned char *)&stat, sizeof(stat),
                                           &res_size, &abort_code);
        if (errorCode >= 0) break;
    }
    return stat;
}

/* sdo_upload2 — retained for HAL CGo compatibility.
 * Prefer sdo_upload() for all new call sites. See header for caveats. */
int sdo_upload2(ec_master_t *master, uint16_t slave_position,
                uint16_t index, uint8_t subindex,
                uint8_t data, size_t data_size) {
    uint32_t stat = 0;
    uint32_t abort_code = 0;
    size_t resultSize = data_size;
    ecrt_master_sdo_upload(master, slave_position, index, subindex,
                           (unsigned char *)&stat, data_size,
                           &resultSize, &abort_code);
    return (int)stat;
}

/* ============================================================
 * name_to_offset_ptr — maps a PDO entry name string to the
 * corresponding field pointer in slave_offsets[slave_index].
 *
 * This is the core of the generic engine: the name in the YAML
 * is the only link between a CoE object and its SlaveOffsets field.
 * Adding a new CoE object requires only a YAML entry + a field in
 * SlaveOffsets + one strcmp line here — zero other C changes.
 *
 * INTEGRATED: Added op_mode_disp, touch_stat, touch_pos1,
 * following_err to match the SDO-PDO build's Minas A6 TxPDO map.
 * ============================================================ */
static unsigned int *name_to_offset_ptr(const char *name, int slave_index) {
    SlaveOffsets *s = &slave_offsets[slave_index];

    if (!name || name[0] == '_') return NULL; /* padding — discard */

    /* RxPDO fields */
    if (strcmp(name, "ctrl_word")    == 0) return &s->ctrl_word;
    if (strcmp(name, "op_mode")      == 0) return &s->op_mode;
    if (strcmp(name, "target_pos")   == 0) return &s->target_pos;
    if (strcmp(name, "target_vel")   == 0) return &s->target_vel;
    if (strcmp(name, "dig_out_mask") == 0) return &s->dig_out_mask;
    if (strcmp(name, "dig_out_val")  == 0) return &s->dig_out_val;

    /* TxPDO fields */
    if (strcmp(name, "error_code")    == 0) return &s->error_code;
    if (strcmp(name, "status_word")   == 0) return &s->status_word;
    if (strcmp(name, "op_mode_disp")  == 0) return &s->op_mode_disp;   /* INTEGRATED */
    if (strcmp(name, "actual_pos")    == 0) return &s->actual_pos;
    if (strcmp(name, "actual_vel")    == 0) return &s->actual_vel;
    if (strcmp(name, "touch_stat")    == 0) return &s->touch_stat;      /* INTEGRATED */
    if (strcmp(name, "touch_pos1")    == 0) return &s->touch_pos1;      /* INTEGRATED */
    if (strcmp(name, "following_err") == 0) return &s->following_err;   /* INTEGRATED */
    if (strcmp(name, "digital_in")    == 0) return &s->digital_in;

    fprintf(stderr, "[PDO-GEN] WARNING: unknown entry name '%s' — offset discarded\n", name);
    return NULL;
}

/* ============================================================
 * register_slave_pdos — Generic PDO registration engine.
 *
 * Replaces configure_*_pdos() + setup_*_domain_sizing() for ALL
 * drive types. The PDO layout comes from Go (parsed from YAML).
 *
 * Implementation steps:
 *   1. Validate inputs.
 *   2. Allocate ec_pdo_entry_info_t arrays from rx/tx specs.
 *   3. Build ec_pdo_info_t[2] (one RxPDO, one TxPDO).
 *   4. Build ec_sync_info_t[5].
 *   5. Get slave config, call ecrt_slave_config_pdos().
 *   6. Disable SM watchdog (prevents A6 Error 80 under Linux jitter).
 *   7. Build ec_pdo_entry_reg_t[] via name_to_offset_ptr().
 *   8. Call ecrt_domain_reg_pdo_entry_list().
 *   9. Mark slave_offsets[slave_index].valid = 1.
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
                        uint32_t      product_code) {

    /* ── Step 1: Validate ─────────────────────────────────────── */
    if (!master || !domain) {
        fprintf(stderr, "[PDO-GEN] register_slave_pdos: NULL master or domain\n");
        return -EINVAL;
    }
    if (slave_index < 0 || slave_index >= MAX_SLAVES) {
        fprintf(stderr, "[PDO-GEN] slave_index %d out of range [0,%d)\n",
                slave_index, MAX_SLAVES);
        return -EINVAL;
    }
    if (!rx_entries || rx_count <= 0 || !tx_entries || tx_count <= 0) {
        fprintf(stderr, "[PDO-GEN] register_slave_pdos: empty entry arrays\n");
        return -EINVAL;
    }

    int rc = 0;

    /* ── Step 2: Build ec_pdo_entry_info_t arrays ─────────────── */
    ec_pdo_entry_info_t *rx_info = malloc(rx_count * sizeof(ec_pdo_entry_info_t));
    ec_pdo_entry_info_t *tx_info = malloc(tx_count * sizeof(ec_pdo_entry_info_t));
    if (!rx_info || !tx_info) {
        fprintf(stderr, "[PDO-GEN] register_slave_pdos: malloc failed\n");
        free(rx_info); free(tx_info);
        return -ENOMEM;
    }
    for (int i = 0; i < rx_count; i++) {
        rx_info[i].index      = rx_entries[i].index;
        rx_info[i].subindex   = rx_entries[i].subindex;
        rx_info[i].bit_length = rx_entries[i].bits;
    }
    for (int i = 0; i < tx_count; i++) {
        tx_info[i].index      = tx_entries[i].index;
        tx_info[i].subindex   = tx_entries[i].subindex;
        tx_info[i].bit_length = tx_entries[i].bits;
    }

    /* ── Step 3 & 4: Build PDO and sync manager descriptors ────── */
    ec_pdo_info_t pdos[2] = {
        {rx_pdo_index, (unsigned int)rx_count, rx_info},
        {tx_pdo_index, (unsigned int)tx_count, tx_info},
    };
    ec_sync_info_t syncs[5] = {
        {0, EC_DIR_OUTPUT, 0, NULL,     EC_WD_DISABLE},
        {1, EC_DIR_INPUT,  0, NULL,     EC_WD_DISABLE},
        {2, EC_DIR_OUTPUT, 1, &pdos[0], EC_WD_ENABLE},
        {3, EC_DIR_INPUT,  1, &pdos[1], EC_WD_DISABLE},
        {0xFF}
    };

    /* ── Step 5: Configure PDOs on the sc handle supplied by Go ─────
     *
     * FIX (Bug 5 — duplicate sc handle):
     * The old code called ecrt_master_slave_config() here to obtain a
     * second sc handle for the same slave. Go's setupPDOPositionGeneric
     * already called ecrt_master_slave_config() and stored the handle in
     * dev.SlaveConfig. IgH returns the same internal object on repeated
     * calls for the same (alias, position, vendor, product) tuple, but
     * relying on that behaviour is fragile and makes the call sequence
     * hard to reason about. We now receive the sc pointer as a parameter.
     *
     * The function signature change:
     *   OLD: register_slave_pdos(master, domain, alias, position,
     *                            vendor_id, product_code, slave_index, ...)
     *   NEW: register_slave_pdos(master, domain, sc, slave_index, ...)
     *
     * Go passes dev.SlaveConfig (set just before this call). The watchdog
     * disable below uses the same handle — no duplication, no confusion.  */
    if (!sc) {
        fprintf(stderr, "[PDO-GEN] slave[%d]: NULL sc passed — "
                "ecrt_master_slave_config must be called before register_slave_pdos\n",
                slave_index);
        free(rx_info); free(tx_info);
        return -EINVAL;
    }

    rc = ecrt_slave_config_pdos(sc, EC_END, syncs);
    free(rx_info); rx_info = NULL;  /* IgH copies internally — safe to free */
    free(tx_info); tx_info = NULL;
    if (rc != 0) {
        fprintf(stderr, "[PDO-GEN] slave[%d]: ecrt_slave_config_pdos failed rc=%d\n",
                slave_index, rc);
        return rc;
    }

    /* ── Step 6: Disable SM watchdog ────────────────────────────
     * Setting intervals to 0 turns off the sync-manager watchdog.
     * Without this, the Panasonic A6 throws Error 80 whenever the
     * Linux scheduler pauses the Go application (e.g. GC, preemption).
     * The SDO-PDO build discovered this fix; it is applied here to
     * all drives — harmless for drives that have no watchdog error. */
    ecrt_slave_config_watchdog(sc, 0, 0);

    /* ── Step 7: Build registration array ────────────────────────
     * Allocate worst-case (rx + tx + 1 terminator). Padding entries
     * (name starts '_') and unknown names are skipped.             */
    int total = rx_count + tx_count;
    ec_pdo_entry_reg_t *regs = calloc(total + 1, sizeof(ec_pdo_entry_reg_t));
    if (!regs) {
        fprintf(stderr, "[PDO-GEN] slave[%d]: calloc failed\n", slave_index);
        return -ENOMEM;
    }

    memset(&slave_offsets[slave_index], 0, sizeof(SlaveOffsets));

    int reg_count = 0;

    for (int i = 0; i < rx_count; i++) {
        unsigned int *ptr = name_to_offset_ptr(rx_entries[i].name, slave_index);
        if (!ptr) continue;
        regs[reg_count].alias        = alias;
        regs[reg_count].position     = position;
        regs[reg_count].vendor_id    = vendor_id;
        regs[reg_count].product_code = product_code;
        regs[reg_count].index        = rx_entries[i].index;
        regs[reg_count].subindex     = rx_entries[i].subindex;
        regs[reg_count].offset       = ptr;
        regs[reg_count].bit_position = NULL;
        reg_count++;
    }

    for (int i = 0; i < tx_count; i++) {
        unsigned int *ptr = name_to_offset_ptr(tx_entries[i].name, slave_index);
        if (!ptr) continue;
        regs[reg_count].alias        = alias;
        regs[reg_count].position     = position;
        regs[reg_count].vendor_id    = vendor_id;
        regs[reg_count].product_code = product_code;
        regs[reg_count].index        = tx_entries[i].index;
        regs[reg_count].subindex     = tx_entries[i].subindex;
        regs[reg_count].offset       = ptr;
        regs[reg_count].bit_position = NULL;
        reg_count++;
    }

    /* Terminator — all-zeros entry signals end of list to IgH */
    memset(&regs[reg_count], 0, sizeof(ec_pdo_entry_reg_t));

    /* ── Step 8: Register with domain ───────────────────────────── */
    rc = ecrt_domain_reg_pdo_entry_list(domain, regs);
    free(regs);

    if (rc != 0) {
        fprintf(stderr,
                "[PDO-GEN] slave[%d]: ecrt_domain_reg_pdo_entry_list failed rc=%d (%s)\n",
                slave_index, rc, strerror(-rc));
        return rc;
    }

    /* ── Step 9: Mark valid and log ─────────────────────────────── */
    slave_offsets[slave_index].valid = 1;

    SlaveOffsets *s = &slave_offsets[slave_index];
    fprintf(stdout,
            "[PDO-GEN] slave[%d] registered. "
            "CW=%u Op=%u TP=%u TV=%u DOMask=%u DOVal=%u "
            "EC=%u SW=%u OpDisp=%u AP=%u AV=%u "
            "TouchStat=%u TouchPos1=%u FE=%u DI=%u\n",
            slave_index,
            s->ctrl_word, s->op_mode, s->target_pos, s->target_vel,
            s->dig_out_mask, s->dig_out_val,
            s->error_code, s->status_word, s->op_mode_disp,
            s->actual_pos, s->actual_vel,
            s->touch_stat, s->touch_pos1, s->following_err,
            s->digital_in);
    fflush(stdout);

    return 0;
}

/* ============================================================
 * get_slave_offsets — copies all offsets for slave_index into *out.
 * ============================================================ */
int get_slave_offsets(int slave_index, SlaveOffsets *out) {
    if (slave_index < 0 || slave_index >= MAX_SLAVES) return -EINVAL;
    if (!slave_offsets[slave_index].valid) return -EINVAL;
    if (!out) return -EINVAL;
    *out = slave_offsets[slave_index];
    return 0;
}

/* ============================================================
 * Individual PDO offset accessors — INTEGRATED from SDO-PDO build.
 *
 * The SDO-PDO build exposed offsets to Go as separate C calls
 * (setup_pos_pdo, setup_statusword_pdo, …). Those functions were
 * single-drive; here they are re-implemented as per-slave wrappers
 * around the unified slave_offsets[] table.
 *
 * Naming: get_*_offset(slave_index, out_ptr) — cleaner than the
 * old setup_*_pdo(domain, alias, position, vendor_id, product_code, out).
 * ============================================================ */

int get_digital_output_offsets(int slave_index,
                               unsigned int *off_mask,
                               unsigned int *off_val) {
    if (slave_index < 0 || slave_index >= MAX_SLAVES) return -EINVAL;
    if (!slave_offsets[slave_index].valid)             return -EINVAL;
    *off_mask = slave_offsets[slave_index].dig_out_mask;
    *off_val  = slave_offsets[slave_index].dig_out_val;
    return 0;
}

int get_pos_pdo_offset(int slave_index, unsigned int *off_pos) {
    if (slave_index < 0 || slave_index >= MAX_SLAVES) return -EINVAL;
    if (!slave_offsets[slave_index].valid)             return -EINVAL;
    *off_pos = slave_offsets[slave_index].actual_pos;
    return 0;
}

int get_statusword_offset(int slave_index, unsigned int *off_status) {
    if (slave_index < 0 || slave_index >= MAX_SLAVES) return -EINVAL;
    if (!slave_offsets[slave_index].valid)             return -EINVAL;
    *off_status = slave_offsets[slave_index].status_word;
    return 0;
}

int get_error_code_offset(int slave_index, unsigned int *off_error) {
    if (slave_index < 0 || slave_index >= MAX_SLAVES) return -EINVAL;
    if (!slave_offsets[slave_index].valid)             return -EINVAL;
    *off_error = slave_offsets[slave_index].error_code;
    return 0;
}

int get_actual_vel_offset(int slave_index, unsigned int *off_vel) {
    if (slave_index < 0 || slave_index >= MAX_SLAVES) return -EINVAL;
    if (!slave_offsets[slave_index].valid)             return -EINVAL;
    *off_vel = slave_offsets[slave_index].actual_vel;
    return 0;
}

int get_digital_in_offset(int slave_index, unsigned int *off_di) {
    if (slave_index < 0 || slave_index >= MAX_SLAVES) return -EINVAL;
    if (!slave_offsets[slave_index].valid)             return -EINVAL;
    *off_di = slave_offsets[slave_index].digital_in;
    return 0;
}

/* ---- INTEGRATED: Touch probe and following-error offsets (Minas A6) ---
 * These are zero (and valid==0 for the field) when the drive's YAML
 * did not include those CoE objects in its tx_entries. The caller
 * (Go) should only use these offsets for drives where they are mapped. */

int get_touch_stat_offset(int slave_index, unsigned int *off_touch_stat) {
    if (slave_index < 0 || slave_index >= MAX_SLAVES) return -EINVAL;
    if (!slave_offsets[slave_index].valid)             return -EINVAL;
    *off_touch_stat = slave_offsets[slave_index].touch_stat;
    return 0;
}

int get_touch_pos1_offset(int slave_index, unsigned int *off_touch_pos1) {
    if (slave_index < 0 || slave_index >= MAX_SLAVES) return -EINVAL;
    if (!slave_offsets[slave_index].valid)             return -EINVAL;
    *off_touch_pos1 = slave_offsets[slave_index].touch_pos1;
    return 0;
}

int get_following_err_offset(int slave_index, unsigned int *off_fe) {
    if (slave_index < 0 || slave_index >= MAX_SLAVES) return -EINVAL;
    if (!slave_offsets[slave_index].valid)             return -EINVAL;
    *off_fe = slave_offsets[slave_index].following_err;
    return 0;
}

int get_op_mode_disp_offset(int slave_index, unsigned int *off_omd) {
    if (slave_index < 0 || slave_index >= MAX_SLAVES) return -EINVAL;
    if (!slave_offsets[slave_index].valid)             return -EINVAL;
    *off_omd = slave_offsets[slave_index].op_mode_disp;
    return 0;
}

/* Convenience: return all four RxPDO write-side offsets in one call. */
int get_all_rx_pdo_offsets(int slave_index,
                           unsigned int *off_controlword,
                           unsigned int *off_opmode,
                           unsigned int *off_target_pos,
                           unsigned int *off_target_vel) {
    if (slave_index < 0 || slave_index >= MAX_SLAVES) return -EINVAL;
    if (!slave_offsets[slave_index].valid)             return -EINVAL;
    SlaveOffsets *s = &slave_offsets[slave_index];
    *off_controlword = s->ctrl_word;
    *off_opmode      = s->op_mode;
    *off_target_pos  = s->target_pos;
    *off_target_vel  = s->target_vel;
    return 0;
}

/* ============================================================
 * Async SDO Request objects — Multiturn reset (A6, per-slave)
 *
 * HAL build extended the SDO-PDO single-drive implementation to a
 * per-slave array. Signature change from SDO-PDO build:
 *   OLD: create_mt_sdo_requests(sc)          — global, single drive
 *   NEW: create_mt_sdo_requests(sc, slave_index) — per-slave
 *
 *   OLD: trigger_mt_request_step(step, value)
 *   NEW: trigger_mt_request_step(slave_index, step, value)
 *
 *   OLD: get_mt_request_state(step)
 *   NEW: get_mt_request_state(slave_index, step)
 * ============================================================ */
int create_mt_sdo_requests(ec_slave_config_t *sc, int slave_index) {
    if (!sc) return -EINVAL;
    if (slave_index < 0 || slave_index >= MAX_SLAVES) return -EINVAL;

    g_req_mt_func[slave_index] =
        ecrt_slave_config_create_sdo_request(sc, 0x4D01, 0x00, 2);
    if (!g_req_mt_func[slave_index]) {
        fprintf(stderr, "[SDO-REQ] slave[%d]: failed to create 0x4D01:00\n", slave_index);
        return -1;
    }
    ecrt_sdo_request_timeout(g_req_mt_func[slave_index], 500);

    /* 0x4D00:01 is UINT32 on the Panasonic MINAS A6 — 4 bytes, NOT 2.
     * Using size=2 causes an SDO abort code (data length mismatch).
     * This was the root cause of "SDO ERROR" on the trigger step in the
     * SDO-PDO build. Fixed here with size=4. */
    g_req_mt_start[slave_index] =
        ecrt_slave_config_create_sdo_request(sc, 0x4D00, 0x01, 4);
    if (!g_req_mt_start[slave_index]) {
        fprintf(stderr, "[SDO-REQ] slave[%d]: failed to create 0x4D00:01\n", slave_index);
        return -1;
    }
    ecrt_sdo_request_timeout(g_req_mt_start[slave_index], 500);

    fprintf(stdout, "[SDO-REQ] slave[%d]: multiturn SDO requests created\n", slave_index);
    fflush(stdout);
    return 0;
}

int trigger_mt_request_step(int slave_index, int step, uint32_t value) {
    if (slave_index < 0 || slave_index >= MAX_SLAVES) return -EINVAL;
    ec_sdo_request_t *req = (step == 0) ? g_req_mt_func[slave_index]
                                        : g_req_mt_start[slave_index];
    if (!req) return -EINVAL;
    if (ecrt_sdo_request_state(req) == EC_REQUEST_BUSY) return -EBUSY;

    /* Data width MUST match the object dictionary size or the drive
     * returns an SDO abort (EC_REQUEST_ERROR).
     *   step 0 → 0x4D01:00  UINT16  — write 2 bytes
     *   step 1 → 0x4D00:01  UINT32  — write 4 bytes               */
    if (step == 0) {
        EC_WRITE_U16(ecrt_sdo_request_data(req), (uint16_t)(value & 0xFFFF));
    } else {
        EC_WRITE_U32(ecrt_sdo_request_data(req), value);
    }
    ecrt_sdo_request_write(req);
    fprintf(stdout, "[MT-SDO] slave[%d] step %d: armed value=0x%08X\n",
            slave_index, step, value);
    fflush(stdout);
    return 0;
}

/* Returns: 1=success, 0=busy/queued/unused, -1=error, -2=not created */
int get_mt_request_state(int slave_index, int step) {
    if (slave_index < 0 || slave_index >= MAX_SLAVES) return -2;
    ec_sdo_request_t *req = (step == 0) ? g_req_mt_func[slave_index]
                                        : g_req_mt_start[slave_index];
    if (!req) return -2;
    ec_request_state_t s = ecrt_sdo_request_state(req);
    if (s == EC_REQUEST_SUCCESS) return 1;
    if (s == EC_REQUEST_ERROR) {
        fprintf(stderr, "[MT-SDO] slave[%d] step %d: ERROR\n", slave_index, step);
        fflush(stderr);
        return -1;
    }
    return 0; /* EC_REQUEST_QUEUED, EC_REQUEST_BUSY, or EC_REQUEST_UNUSED */
}

/* ============================================================
 * Async SDO — Profile Velocity 0x6081 (per-instance, Go holds ptr)
 *
 * Unchanged from both builds — signature is pointer-based so it
 * already supports multiple drives without modification.
 * ============================================================ */
void* create_profile_vel_sdo_request(ec_slave_config_t *sc) {
    if (!sc) return NULL;
    ec_sdo_request_t *req =
        ecrt_slave_config_create_sdo_request(sc, 0x6081, 0x00, 4);
    if (req) ecrt_sdo_request_timeout(req, 500);
    return (void*)req;
}

int trigger_profile_vel_request(void *req_ptr, uint32_t value) {
    if (!req_ptr) return -EINVAL;
    ec_sdo_request_t *req = (ec_sdo_request_t *)req_ptr;
    /* Tell Go to retry if the mailbox is still processing a prior update */
    if (ecrt_sdo_request_state(req) == EC_REQUEST_BUSY) return -16; /* EBUSY */
    EC_WRITE_U32(ecrt_sdo_request_data(req), value);
    ecrt_sdo_request_write(req);
    return 0;
}

/* Returns: 1=success, 0=busy/queued/unused, -1=error, -2=null */
int get_profile_vel_state(void *req_ptr) {
    if (!req_ptr) return -2;
    ec_sdo_request_t *req = (ec_sdo_request_t *)req_ptr;
    ec_request_state_t s = ecrt_sdo_request_state(req);
    if (s == EC_REQUEST_SUCCESS) return  1;
    if (s == EC_REQUEST_ERROR)   return -1;
    return 0;
}

/* ============================================================
 * sync_dc_clocks — Distributed Clock synchronisation.
 *
 * Must be called EVERY cyclic tick, AFTER ecrt_domain_queue()
 * for all domains and BEFORE ecrt_master_send().
 *
 * Safe when no DC slaves are configured — IgH makes both sync
 * calls no-ops in that case.
 * ============================================================ */
void sync_dc_clocks(ec_master_t *master) {
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    uint64_t app_time_ns = (uint64_t)ts.tv_sec * 1000000000ULL
                         + (uint64_t)ts.tv_nsec;
    ecrt_master_application_time(master, app_time_ns);
    ecrt_master_sync_reference_clock(master);
    ecrt_master_sync_slave_clocks(master);
}

/* ============================================================
 * PDO read/write helpers
 * ============================================================ */
int32_t  read_s32(uint8_t *d, unsigned int o) { return EC_READ_S32(d+o); }
uint16_t read_u16(uint8_t *d, unsigned int o) { return EC_READ_U16(d+o); }
uint32_t read_u32(uint8_t *d, unsigned int o) { return EC_READ_U32(d+o); }
int8_t   read_s8 (uint8_t *d, unsigned int o) { return EC_READ_S8(d+o);  }
int16_t  read_s16(uint8_t *d, unsigned int o) { return EC_READ_S16(d+o); }

void write_u16(uint8_t *d, unsigned int o, uint16_t v) { EC_WRITE_U16(d+o, v); }
void write_s32(uint8_t *d, unsigned int o, int32_t  v) { EC_WRITE_S32(d+o, v); }
void write_u32(uint8_t *d, unsigned int o, uint32_t v) { EC_WRITE_U32(d+o, v); }
void write_s8 (uint8_t *d, unsigned int o, int8_t   v) { EC_WRITE_S8(d+o, v);  }
void write_s16(uint8_t *d, unsigned int o, int16_t  v) { EC_WRITE_S16(d+o, v); }

const char* ec_strerror(int err) { return strerror(err); }

/* ============================================================
 * Bus Scanning — scan_bus()
 * Safe to call before ecrt_master_activate() — read-only query.
 * ============================================================ */
int scan_bus(ec_master_t *master, BusSlaveInfo *out, int max_count) {
    if (!master || !out || max_count <= 0) return -EINVAL;

    int count = 0;
    for (uint16_t pos = 0; pos < (uint16_t)max_count; pos++) {
        ec_slave_info_t info;
        int rc = ecrt_master_get_slave(master, pos, &info);
        if (rc != 0) break; /* no slave at this position — end of bus */

        out[count].position     = info.position;
        out[count].alias        = info.alias;
        out[count].vendor_id    = info.vendor_id;
        out[count].product_code = info.product_code;

        strncpy(out[count].name, info.name, BUS_SLAVE_NAME_LEN - 1);
        out[count].name[BUS_SLAVE_NAME_LEN - 1] = '\0';

        fprintf(stdout,
            "[BUS-SCAN] slave[%d] vendor=0x%08X product=0x%08X alias=%d name=\"%s\"\n",
            count, out[count].vendor_id, out[count].product_code,
            out[count].alias, out[count].name);
        fflush(stdout);

        count++;
    }

    fprintf(stdout, "[BUS-SCAN] total: %d slave(s) found on bus\n", count);
    fflush(stdout);
    return count;
}

/* ============================================================
 * Size helpers
 * ============================================================ */
size_t uint16Size(void) { return sizeof(uint16_t);     }
size_t uint32Size(void) { return sizeof(uint32_t);     }
size_t uint8Size(void)  { return sizeof(uint8_t);      }
size_t unintSize(void)  { return sizeof(unsigned int); }
size_t int32Size(void)  { return sizeof(int32_t);      }
size_t int16Size(void)  { return sizeof(int16_t);      }
size_t int8Size(void)   { return sizeof(int8_t);       }
