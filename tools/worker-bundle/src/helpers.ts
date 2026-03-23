import { SCHEDULER_NAME } from "./constants";
import type { WorkerEnv } from "./types";

export function buildContainerEnvVars(env: WorkerEnv): Record<string, string> {
  const vars: Record<string, string> = { AWS_REGION: "auto" };
  if (env.LOOPHOLE_STORE_URL) vars.LOOPHOLE_STORE_URL = env.LOOPHOLE_STORE_URL;
  if (env.AWS_ACCESS_KEY_ID) vars.AWS_ACCESS_KEY_ID = env.AWS_ACCESS_KEY_ID;
  if (env.AWS_SECRET_ACCESS_KEY) vars.AWS_SECRET_ACCESS_KEY = env.AWS_SECRET_ACCESS_KEY;
  return vars;
}

export function rewriteDurableObjectPath(pathname: string): { name: string | null; pathname: string } {
  const parts = pathname.split("/").filter(Boolean);
  const name = parts[0] ?? null;
  return {
    name,
    pathname: "/" + parts.slice(1).join("/"),
  };
}

export function schedulerStub(env: WorkerEnv) {
  const id = env.EDGESSH.idFromName(SCHEDULER_NAME);
  return env.EDGESSH.get(id);
}
