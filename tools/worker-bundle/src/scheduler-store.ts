import { MAX_VMS_PER_CONTAINER } from "./constants";
import type { ContainerRecord, DurableObjectStateLike, SQLRow, VMRecord } from "./types";

export class SchedulerStore {
  constructor(private readonly ctx: DurableObjectStateLike) {}

  ensureTables() {
    this.ctx.storage.sql.exec(`
      CREATE TABLE IF NOT EXISTS containers (
        id TEXT PRIMARY KEY,
        do_name TEXT NOT NULL,
        vm_count INTEGER DEFAULT 0,
        max_vms INTEGER DEFAULT ${MAX_VMS_PER_CONTAINER}
      );
      CREATE TABLE IF NOT EXISTS vms (
        name TEXT PRIMARY KEY,
        rootfs TEXT NOT NULL,
        ssh_pubkey TEXT,
        container_id TEXT,
        created_at TEXT NOT NULL
      );
    `);

    const info = this.all<{ name: string; notnull: number }>("PRAGMA table_info(vms)");
    const containerIDCol = info.find((c) => c.name === "container_id");
    if (containerIDCol?.notnull) {
      this.exec(`
        CREATE TABLE vms_tmp (name TEXT PRIMARY KEY, rootfs TEXT NOT NULL, ssh_pubkey TEXT, container_id TEXT, created_at TEXT NOT NULL);
        INSERT INTO vms_tmp (name, container_id, rootfs, created_at) SELECT name, container_id, rootfs, created_at FROM vms;
        DROP TABLE vms;
        ALTER TABLE vms_tmp RENAME TO vms;
      `);
    }

    const pubKeyCol = info.find((c) => c.name === "ssh_pubkey");
    if (!pubKeyCol) {
      this.exec(`ALTER TABLE vms ADD COLUMN ssh_pubkey TEXT`);
    }

    const vmInfoCol = info.find((c) => c.name === "vm_info");
    if (!vmInfoCol) {
      this.exec(`ALTER TABLE vms ADD COLUMN vm_info TEXT`);
    }

    // Add next_subnet_id to containers if missing
    const containerInfo = this.all<{ name: string }>("PRAGMA table_info(containers)");
    if (!containerInfo.find((c) => c.name === "next_subnet_id")) {
      this.exec(`ALTER TABLE containers ADD COLUMN next_subnet_id INTEGER DEFAULT 1`);
    }
  }

  findVM(name: string): VMRecord | undefined {
    return this.one<VMRecord>(
      "SELECT name, rootfs, ssh_pubkey, container_id FROM vms WHERE name = ?",
      name
    );
  }

  vmExists(name: string): boolean {
    const rows = this.all<SQLRow>("SELECT name FROM vms WHERE name = ?", name);
    return rows.length > 0;
  }

  insertVM(name: string, rootfs: string, sshPubKey: string) {
    this.exec(
      "INSERT INTO vms (name, rootfs, ssh_pubkey, container_id, created_at) VALUES (?, ?, ?, NULL, ?)",
      name,
      rootfs,
      sshPubKey,
      new Date().toISOString()
    );
  }

  deleteVM(name: string) {
    this.exec("DELETE FROM vms WHERE name = ?", name);
  }

  getVMInfo(name: string): string | null {
    const row = this.one<SQLRow>("SELECT vm_info FROM vms WHERE name = ?", name);
    return row?.vm_info ?? null;
  }

  setVMInfo(name: string, vmInfo: string) {
    this.exec("UPDATE vms SET vm_info = ? WHERE name = ?", vmInfo, name);
  }

  clearVMInfo(name: string) {
    this.exec("UPDATE vms SET vm_info = NULL WHERE name = ?", name);
  }

  allocateSubnetId(containerID: string): number {
    const row = this.one<SQLRow>(
      "SELECT next_subnet_id FROM containers WHERE id = ?",
      containerID
    );
    const id = Number(row?.next_subnet_id ?? 1);
    this.exec(
      "UPDATE containers SET next_subnet_id = ? WHERE id = ?",
      id + 1,
      containerID
    );
    return id;
  }

  updateVMPubKey(name: string, sshPubKey: string) {
    this.exec("UPDATE vms SET ssh_pubkey = ? WHERE name = ?", sshPubKey, name);
  }

  assignVMToContainer(name: string, containerID: string) {
    this.setVMContainer(name, containerID);
  }

  clearVMContainer(name: string) {
    this.setVMContainer(name, null);
  }

  clearVMsForContainer(containerID: string) {
    this.exec("UPDATE vms SET container_id = NULL WHERE container_id = ?", containerID);
  }

  listVMs() {
    return this.all(
      "SELECT v.name, v.container_id, v.rootfs, v.created_at, c.do_name FROM vms v LEFT JOIN containers c ON v.container_id = c.id"
    );
  }

  findContainer(containerID: string): ContainerRecord | undefined {
    return this.one<ContainerRecord>(
      "SELECT id, do_name FROM containers WHERE id = ?",
      containerID
    );
  }

  findContainerName(containerID: string): string | undefined {
    const row = this.one<SQLRow>(
      "SELECT do_name FROM containers WHERE id = ?",
      containerID
    );
    return row ? String(row.do_name) : undefined;
  }

  findAvailableContainer(): ContainerRecord | undefined {
    return this.one<ContainerRecord>(
      "SELECT id, do_name FROM containers WHERE vm_count < max_vms LIMIT 1"
    );
  }

  nextContainerOrdinal(): number {
    const row = this.one<SQLRow>(
      "SELECT COALESCE(MAX(CAST(SUBSTR(do_name, 6) AS INTEGER)), -1) + 1 AS next FROM containers"
    ) as SQLRow;
    return Number(row.next);
  }

  insertContainer(id: string, doName: string) {
    this.exec(
      "INSERT INTO containers (id, do_name, vm_count) VALUES (?, ?, 0)",
      id,
      doName
    );
  }

  incrementContainerVMCount(containerID: string) {
    this.adjustContainerVMCount(containerID, "vm_count + 1");
  }

  decrementContainerVMCount(containerID: string) {
    this.adjustContainerVMCount(containerID, "MAX(vm_count - 1, 0)");
  }

  setContainerVMCountToZero(containerID: string) {
    this.adjustContainerVMCount(containerID, "0");
  }

  markContainerFull(containerID: string) {
    this.adjustContainerVMCount(containerID, "max_vms");
  }

  deleteContainer(containerID: string) {
    this.exec("DELETE FROM containers WHERE id = ?", containerID);
  }

  listContainers() {
    return this.all(
      "SELECT id, do_name, vm_count, max_vms FROM containers"
    );
  }

  private setVMContainer(name: string, containerID: string | null) {
    if (containerID === null) {
      this.exec("UPDATE vms SET container_id = NULL WHERE name = ?", name);
      return;
    }
    this.exec("UPDATE vms SET container_id = ? WHERE name = ?", containerID, name);
  }

  private adjustContainerVMCount(containerID: string, expression: string) {
    this.exec(`UPDATE containers SET vm_count = ${expression} WHERE id = ?`, containerID);
  }

  private one<T extends SQLRow>(query: string, ...args: any[]): T | undefined {
    return this.all<T>(query, ...args)[0];
  }

  private all<T extends SQLRow = SQLRow>(query: string, ...args: any[]): T[] {
    return this.ctx.storage.sql.exec(query, ...args).toArray() as T[];
  }

  private exec(query: string, ...args: any[]) {
    this.ctx.storage.sql.exec(query, ...args);
  }
}
