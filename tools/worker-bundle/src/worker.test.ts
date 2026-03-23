import { describe, expect, test } from "bun:test";
import { buildContainerEnvVars, rewriteDurableObjectPath } from "./helpers";
import { worker } from "./router";
import { SchedulerService } from "./scheduler";
import type { SQLRow } from "./types";

class FakeSQL {
  vms = new Map<string, SQLRow>();
  containers = new Map<string, SQLRow>();

  exec(query: string, ...args: any[]) {
    const q = query.replace(/\s+/g, " ").trim();

    if (q.startsWith("CREATE TABLE") || q.startsWith("DROP TABLE") || q.startsWith("ALTER TABLE")) {
      return this.rows([]);
    }
    if (q.startsWith("PRAGMA table_info(vms)")) {
      return this.rows([
        { name: "name", notnull: 1 },
        { name: "rootfs", notnull: 1 },
        { name: "ssh_pubkey", notnull: 0 },
        { name: "owner", notnull: 0 },
        { name: "container_id", notnull: 0 },
        { name: "created_at", notnull: 1 },
      ]);
    }
    if (q.startsWith("SELECT name FROM vms WHERE name = ?")) {
      const row = this.vms.get(args[0]);
      return this.rows(row ? [{ name: row.name }] : []);
    }
    if (q.startsWith("INSERT INTO vms (name, rootfs, ssh_pubkey, owner, container_id, created_at) VALUES")) {
      this.vms.set(args[0], {
        name: args[0],
        rootfs: args[1],
        ssh_pubkey: args[2],
        owner: args[3],
        container_id: null,
        created_at: args[4],
      });
      return this.rows([]);
    }
    if (q.startsWith("DELETE FROM vms WHERE name = ?")) {
      this.vms.delete(args[0]);
      return this.rows([]);
    }
    if (q.startsWith("SELECT name, rootfs, ssh_pubkey, owner, container_id FROM vms WHERE name = ?")) {
      const row = this.vms.get(args[0]);
      return this.rows(row ? [row] : []);
    }
    if (q.startsWith("UPDATE vms SET ssh_pubkey = ?, owner = ? WHERE name = ?")) {
      const row = this.vms.get(args[2]);
      if (row) row.ssh_pubkey = args[0];
      if (row) row.owner = args[1];
      return this.rows([]);
    }
    if (q.startsWith("UPDATE vms SET container_id = ? WHERE name = ?")) {
      const row = this.vms.get(args[1]);
      if (row) row.container_id = args[0];
      return this.rows([]);
    }
    if (q.startsWith("UPDATE vms SET container_id = NULL WHERE name = ?")) {
      const row = this.vms.get(args[0]);
      if (row) row.container_id = null;
      return this.rows([]);
    }
    if (q.startsWith("UPDATE vms SET container_id = NULL WHERE container_id = ?")) {
      for (const row of this.vms.values()) {
        if (row.container_id === args[0]) row.container_id = null;
      }
      return this.rows([]);
    }
    if (q.startsWith("SELECT id, do_name FROM containers WHERE id = ?")) {
      const row = this.containers.get(args[0]);
      return this.rows(row ? [row] : []);
    }
    if (q.startsWith("SELECT do_name FROM containers WHERE id = ?")) {
      const row = this.containers.get(args[0]);
      return this.rows(row ? [{ do_name: row.do_name }] : []);
    }
    if (q.startsWith("UPDATE containers SET vm_count = MAX(vm_count - 1, 0) WHERE id = ?")) {
      const row = this.containers.get(args[0]);
      if (row) row.vm_count = Math.max(Number(row.vm_count || 0) - 1, 0);
      return this.rows([]);
    }
    if (q.startsWith("UPDATE containers SET vm_count = 0 WHERE id = ?")) {
      const row = this.containers.get(args[0]);
      if (row) row.vm_count = 0;
      return this.rows([]);
    }

    throw new Error(`Unhandled SQL in test fake: ${q}`);
  }

  private rows(rows: SQLRow[]) {
    return {
      toArray() {
        return rows;
      },
    };
  }
}

function createSchedulerHarness() {
  const sql = new FakeSQL();
  const ctx = {
    id: { toString: () => "scheduler-id" },
    container: { running: false },
    storage: { sql },
  };
  const stubs = new Map<string, { fetch(request: Request): Promise<Response> }>();
  const env = {
    EDGESSH: {
      idFromName(name: string) {
        return { toString: () => `${name}-id` };
      },
      get(id: { toString(): string }) {
        const key = id.toString();
        const stub = stubs.get(key);
        if (!stub) throw new Error(`missing stub for ${key}`);
        return stub;
      },
    },
  };
  return { sql, ctx, env, stubs };
}

describe("worker helpers", () => {
  test("buildContainerEnvVars keeps only configured secrets", () => {
    expect(
      buildContainerEnvVars({
        EDGESSH: {
          idFromName: () => ({ toString: () => "x" }),
          get: async () => new Response(),
        } as any,
        LOOPHOLE_STORE_URL: "https://store.example/bucket",
        AWS_ACCESS_KEY_ID: "key",
      })
    ).toEqual({
      AWS_REGION: "auto",
      LOOPHOLE_STORE_URL: "https://store.example/bucket",
      AWS_ACCESS_KEY_ID: "key",
    });
  });

  test("rewriteDurableObjectPath strips the durable object name prefix", () => {
    expect(rewriteDurableObjectPath("/host-7/status")).toEqual({
      name: "host-7",
      pathname: "/status",
    });
    expect(rewriteDurableObjectPath("/")).toEqual({
      name: null,
      pathname: "/",
    });
  });
});

describe("scheduler service", () => {
  test("scheduleVMCreate stores the VM ssh public key", async () => {
    const { ctx, env, sql } = createSchedulerHarness();
    const scheduler = new SchedulerService(ctx as any, env as any);
    (scheduler as any).vm.ensureVMRunning = async (_name: string) =>
      ({
        do_name: "host-0",
        container_id: "host0id",
        rootfs: "basev3",
        stub: { fetch: async () => new Response("ok") },
      }) as any;

    const url = new URL("https://example.com/api/vm/create?name=vm1&rootfs=basev3&ssh_pubkey=ssh-ed25519%20AAA");
    const resp = await scheduler.scheduleVMCreate(url);
    expect(resp.status).toBe(200);
    expect(sql.vms.get("vm1")?.ssh_pubkey).toBe("ssh-ed25519 AAA");
    expect(sql.vms.get("vm1")?.owner).toBeNull();
  });

  test("scheduleVMCreate derives owner from the ssh public key comment", async () => {
    const { ctx, env, sql } = createSchedulerHarness();
    const scheduler = new SchedulerService(ctx as any, env as any);
    (scheduler as any).vm.ensureVMRunning = async (_name: string) =>
      ({
        do_name: "host-0",
        container_id: "host0id",
        rootfs: "basev3",
        stub: { fetch: async () => new Response("ok") },
      }) as any;

    const url = new URL("https://example.com/api/vm/create?name=vm2&rootfs=basev3&ssh_pubkey=ssh-ed25519%20AAA%20ramon@mbp");
    const resp = await scheduler.scheduleVMCreate(url);
    expect(resp.status).toBe(200);
    expect(sql.vms.get("vm2")?.owner).toBe("ramon");
  });

  test("vmSSHInfo forwards the requested ssh key into ensureVMRunning", async () => {
    const { ctx, env, sql } = createSchedulerHarness();
    sql.vms.set("vm1", {
      name: "vm1",
      rootfs: "basev3",
      ssh_pubkey: "old-key",
      owner: null,
      container_id: null,
      created_at: new Date().toISOString(),
    });

    const scheduler = new SchedulerService(ctx as any, env as any);
    let seenKey = "";
    (scheduler as any).vm.ensureVMRunning = async (_name: string, requestedPubKey = "") =>
      ({
        do_name: "host-0",
        container_id: "host0id",
        rootfs: "basev3",
        stub: { fetch: async () => new Response("ok") },
        ...(seenKey = requestedPubKey, {}),
      }) as any;

    const resp = await scheduler.vmSSHInfo(
      new URL("https://example.com/api/vm/ssh-info?name=vm1&ssh_pubkey=new-key")
    );
    expect(resp.status).toBe(200);
    expect(seenKey).toBe("new-key");
  });

  test("proxyTCPToVM requires websocket upgrade", async () => {
    const { ctx, env } = createSchedulerHarness();
    const scheduler = new SchedulerService(ctx as any, env as any);
    const req = new Request("https://example.com/api/vm/tcp?name=vm1&port=22");
    const resp = await scheduler.proxyTCPToVM(req, new URL(req.url));
    expect(resp.status).toBe(426);
    expect(await resp.text()).toContain("expected websocket upgrade");
  });
});

describe("worker entrypoint", () => {
  test("routes /api requests to the scheduler durable object", async () => {
    const seen: string[] = [];
    const env = {
      EDGESSH: {
        idFromName(name: string) {
          return { toString: () => `${name}-id` };
        },
        get() {
          return {
            async fetch(request: Request) {
              seen.push(new URL(request.url).pathname);
              return new Response("scheduler");
            },
          };
        },
      },
    };

    const resp = await worker.fetch(new Request("https://example.com/api/version"), env as any);
    expect(await resp.text()).toBe("scheduler");
    expect(seen).toEqual(["/api/version"]);
  });

  test("strips the durable object prefix before forwarding non-api requests", async () => {
    let forwarded = "";
    const env = {
      EDGESSH: {
        idFromName(name: string) {
          return { toString: () => `${name}-id` };
        },
        get() {
          return {
            async fetch(request: Request) {
              forwarded = new URL(request.url).pathname;
              return new Response("ok");
            },
          };
        },
      },
    };

    const resp = await worker.fetch(new Request("https://example.com/host-3/status"), env as any);
    expect(resp.status).toBe(200);
    expect(forwarded).toBe("/status");
  });
});
