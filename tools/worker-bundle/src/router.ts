import { authenticateRequest, exchangeVumelaToken } from "./auth";
import { rewriteDurableObjectPath, schedulerStub } from "./helpers";
import type { WorkerEnv } from "./types";

export const worker = {
  async fetch(request: Request, env: WorkerEnv) {
    const url = new URL(request.url);

    if (url.pathname === "/api/auth/exchange" && request.method === "POST") {
      return handleAuthExchange(request, env, url);
    }
    if (url.pathname === "/api/auth/me" && request.method === "GET") {
      try {
        const claims = await authenticateRequest(request, env);
        return Response.json({
          session_token: "",
          sub: claims?.sub || "",
          name: claims?.name || "",
          expires_in: 0,
        });
      } catch (error: any) {
        return new Response(error.message || "unauthorized", { status: 401 });
      }
    }
    if (url.pathname.startsWith("/api/")) {
      if (url.pathname !== "/api/version") {
        try {
          await authenticateRequest(request, env);
        } catch (error: any) {
          return new Response(error.message || "unauthorized", { status: 401 });
        }
      }
      return schedulerStub(env).fetch(request);
    }

    const { name, pathname } = rewriteDurableObjectPath(url.pathname);
    if (!name) {
      return new Response("edgessh worker v3", { status: 200 });
    }

    const id = env.EDGESSH.idFromName(name);
    const stub = env.EDGESSH.get(id);
    const newURL = new URL(request.url);
    newURL.pathname = pathname;
    return stub.fetch(new Request(newURL, request));
  },
};

async function handleAuthExchange(request: Request, env: WorkerEnv, url: URL) {
  try {
    const body = await request.json() as { token?: string };
    const token = body.token?.trim();
    if (!token) {
      return new Response("missing token", { status: 400 });
    }
    const { sessionToken, claims } = await exchangeVumelaToken(token, url.origin, env);
    return Response.json({
      session_token: sessionToken,
      sub: claims.sub,
      name: claims.name || "",
      expires_in: 30 * 24 * 60 * 60,
    });
  } catch (error: any) {
    return new Response(error.message || "invalid token", { status: 401 });
  }
}
