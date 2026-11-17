// Signs the service tokens agents presents to workflow-ts in X-Depot-Service-Token: the wire format platform/go/tenant
// ServiceSigner.Mint writes and workflow-ts's verifier (and the Go verifier) read: Ed25519 over "depot.service-token.v1." + body,
// 60 s lifetime, tenant-level (no legal entity) because an agent run belongs to a tenant, with the run's user as the actor.
//
// The grant mirrors service_grants.agents.workflow-ts in policies/authz/data.json and the serviceGrants table in
// platform/go/tenant/service.go (test/steps-api.test.ts keeps them equal).

import { createPrivateKey, randomBytes, sign } from "node:crypto";

export const SERVICE_NAME_SELF = "agents";
export const WORKFLOW_AUDIENCE = "workflow-ts";
export const DUTY_RUN_SIGNAL = "agents.run.signal";
export const WORKFLOW_GRANT: readonly string[] = [DUTY_RUN_SIGNAL];

const TOKEN_CONTEXT = "depot.service-token.v1.";
const PKCS8_ED25519_PREFIX = Buffer.from("302e020100300506032b657004220420", "hex");
const TTL_S = 60;

export interface ServiceSigner {
  mintForWorkflow(subject: { tenantId: string; actor: string }): string;
}

export function createServiceSigner(o: { seedBase64: string; keyId?: string; now?: () => number; newJti?: () => string }): ServiceSigner {
  const seed = Buffer.from(o.seedBase64.trim(), "base64");
  if (seed.length !== 32) throw new TypeError("the service signing key must be a base64 32-byte Ed25519 seed");
  const key = createPrivateKey({ key: Buffer.concat([PKCS8_ED25519_PREFIX, seed]), format: "der", type: "pkcs8" });
  const now = o.now ?? Date.now;
  const newJti = o.newJti ?? (() => randomBytes(16).toString("hex"));
  return {
    mintForWorkflow({ tenantId, actor }) {
      if (!tenantId || !actor) throw new TypeError("a service token needs a tenant and the acting user");
      const iat = Math.floor(now() / 1000);
      const claims = { iss: SERVICE_NAME_SELF, ...(o.keyId ? { kid: o.keyId } : {}), aud: WORKFLOW_AUDIENCE, tid: tenantId, act: actor, duties: [...WORKFLOW_GRANT], iat, exp: iat + TTL_S, jti: newJti() };
      const body = Buffer.from(JSON.stringify(claims)).toString("base64url");
      return `${body}.${sign(null, Buffer.from(TOKEN_CONTEXT + body), key).toString("base64url")}`;
    },
  };
}

export function serviceSignerFromEnv(env: Record<string, string | undefined>, readFile: (path: string) => string): ServiceSigner | undefined {
  const file = env["DEPOT_SERVICE_SIGNING_KEY_FILE"];
  const seed = file ? readFile(file) : (env["DEPOT_SERVICE_SIGNING_KEY"] ?? "");
  if (!seed.trim()) return undefined;
  return createServiceSigner({ seedBase64: seed, ...(env["DEPOT_SERVICE_KEY_ID"] ? { keyId: env["DEPOT_SERVICE_KEY_ID"] } : {}) });
}
