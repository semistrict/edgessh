export type SQLRow = Record<string, any>;

export type DurableObjectStubLike = {
  fetch(request: Request): Promise<Response>;
};

export type DurableObjectNamespaceLike = {
  idFromName(name: string): { toString(): string };
  get(id: unknown): DurableObjectStubLike;
};

export type WorkerEnv = {
  EDGESSH: DurableObjectNamespaceLike;
  LOOPHOLE_STORE_URL?: string;
  AWS_ACCESS_KEY_ID?: string;
  AWS_SECRET_ACCESS_KEY?: string;
  EDGESSH_AUTH_SECRET?: string;
  VUMELA_BASE_URL?: string;
};

export type SchedulerResult = {
  do_name: string;
  container_id: string;
  rootfs: string;
  stub: DurableObjectStubLike;
};

export type VMRecord = {
  name: string;
  rootfs: string;
  ssh_pubkey: string | null;
  owner: string | null;
  container_id: string | null;
  created_at: string;
};

export type ContainerRecord = {
  id: string;
  do_name: string;
  vm_count?: number;
  max_vms?: number;
  next_subnet_id?: number;
};

export type SQLExecResult = {
  toArray(): SQLRow[];
};

export type DurableObjectStateLike = {
  id: { toString(): string };
  container?: { running: boolean };
  storage: {
    sql: {
      exec(query: string, ...args: any[]): SQLExecResult;
    };
  };
};
