import { createRemoteJWKSet, jwtVerify, SignJWT } from "jose";
import type { JWTPayload } from "jose";
import type { WorkerEnv } from "./types";

export type SessionClaims = JWTPayload & {
  sub: string;
  name?: string;
  typ: "edgessh_session";
};

const textEncoder = new TextEncoder();
const jwksByBaseURL = new Map<string, ReturnType<typeof createRemoteJWKSet>>();

function vumelaBaseURL(env: WorkerEnv): string {
  return env.VUMELA_BASE_URL || "https://vumela.dev";
}

function authSecret(env: WorkerEnv): Uint8Array {
  if (!env.EDGESSH_AUTH_SECRET) {
    throw new Error("missing EDGESSH_AUTH_SECRET");
  }
  return textEncoder.encode(env.EDGESSH_AUTH_SECRET);
}

function jwks(env: WorkerEnv) {
  const baseURL = vumelaBaseURL(env);
  let remote = jwksByBaseURL.get(baseURL);
  if (!remote) {
    remote = createRemoteJWKSet(new URL("/api/auth/jwks", baseURL));
    jwksByBaseURL.set(baseURL, remote);
  }
  return remote;
}

export function isInternalRequest(request: Request): boolean {
  return new URL(request.url).hostname === "internal";
}

export function bearerToken(request: Request): string | null {
  const authorization = request.headers.get("Authorization") || request.headers.get("authorization") || "";
  if (!authorization.toLowerCase().startsWith("bearer ")) {
    return null;
  }
  return authorization.slice("Bearer ".length).trim();
}

export async function exchangeVumelaToken(
  token: string,
  audience: string,
  env: WorkerEnv,
): Promise<{ sessionToken: string; claims: SessionClaims }> {
  const { payload } = await jwtVerify(token, jwks(env), {
    issuer: vumelaBaseURL(env),
    audience,
  });

  const claims: SessionClaims = {
    sub: String(payload.sub || ""),
    name: typeof payload.name === "string" ? payload.name : "",
    typ: "edgessh_session",
  };

  const sessionToken = await new SignJWT(claims)
    .setProtectedHeader({ alg: "HS256" })
    .setIssuer(audience)
    .setAudience(audience)
    .setIssuedAt()
    .setExpirationTime("30d")
    .sign(authSecret(env));

  return { sessionToken, claims };
}

export async function verifySessionToken(
  token: string,
  audience: string,
  env: WorkerEnv,
): Promise<SessionClaims> {
  const { payload } = await jwtVerify(token, authSecret(env), {
    issuer: audience,
    audience,
  });
  if (payload.typ !== "edgessh_session" || typeof payload.sub !== "string") {
    throw new Error("invalid session token");
  }
  return payload as SessionClaims;
}

export async function authenticateRequest(
  request: Request,
  env: WorkerEnv,
): Promise<SessionClaims | null> {
  if (isInternalRequest(request)) {
    return null;
  }
  const token = bearerToken(request);
  if (!token) {
    throw new Error("missing authorization");
  }
  return verifySessionToken(token, new URL(request.url).origin, env);
}
