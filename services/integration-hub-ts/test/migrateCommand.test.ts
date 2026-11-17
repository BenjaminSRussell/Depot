import { randomUUID } from "node:crypto";
import pg from "pg";
import { describe, expect, test } from "vitest";

import { isConnectionError, MIGRATE_COMMAND, migrateFromEnv, migrateRetryMs, migrateUrl } from "../src/pgStore.ts";

// The image's migrate command, run by the chart's owner-role migration Job (D28): the same contract as dbx.RunMigrateCommand.
const ownerUrl = process.env["DEPOT_TEST_DATABASE_URL"];
const appUrl = process.env["DEPOT_TEST_APP_DATABASE_URL"];

describe("migrate command", () => {
  test("the owner URL wins over the app URL and a missing URL fails", async () => {
    expect(MIGRATE_COMMAND).toBe("migrate");
    expect(migrateUrl({ MIGRATE_DATABASE_URL: "postgres://owner", DATABASE_URL: "postgres://app" })).toBe("postgres://owner");
    expect(migrateUrl({ DATABASE_URL: "postgres://app" })).toBe("postgres://app");
    expect(() => migrateUrl({})).toThrow(/MIGRATE_DATABASE_URL or DATABASE_URL/);
    await expect(migrateFromEnv({})).rejects.toThrow(/MIGRATE_DATABASE_URL or DATABASE_URL/);
  });

  test("the retry bound is configurable", () => {
    expect(migrateRetryMs({})).toBe(300_000);
    expect(migrateRetryMs({ MIGRATE_RETRY_TIMEOUT: "90s" })).toBe(90_000);
    expect(migrateRetryMs({ MIGRATE_RETRY_TIMEOUT: "2m" })).toBe(120_000);
    expect(migrateRetryMs({ MIGRATE_RETRY_TIMEOUT: "0" })).toBe(0);
    expect(() => migrateRetryMs({ MIGRATE_RETRY_TIMEOUT: "soon" })).toThrow(/MIGRATE_RETRY_TIMEOUT/);
  });

  test("only an unreachable database is retried", () => {
    for (const code of ["ECONNREFUSED", "ENOTFOUND", "57P03", "57P01", "08006", "53300"]) expect(isConnectionError({ code })).toBe(true);
    expect(isConnectionError(new Error("Connection terminated unexpectedly"))).toBe(true);
    for (const code of ["42601", "23505", "28P01", "3D000", "42501"]) expect(isConnectionError({ code })).toBe(false);
    expect(isConnectionError(new Error("migration 0002_x.sql: syntax error"))).toBe(false);
  });

  test("an unreachable database is retried with back-off until MIGRATE_RETRY_TIMEOUT", async () => {
    const logged: unknown[] = [];
    const start = Date.now();
    await expect(
      migrateFromEnv({ MIGRATE_DATABASE_URL: "postgres://owner@127.0.0.1:1/x", MIGRATE_RETRY_TIMEOUT: "2500ms" }, undefined, (_m, f) => void logged.push(f)),
    ).rejects.toMatchObject({ code: "ECONNREFUSED" });
    expect(logged.length).toBeGreaterThanOrEqual(1);
    expect(Date.now() - start).toBeLessThan(10_000);
  });
});

function inSchema(url: string, schema: string): string {
  const u = new URL(url);
  u.searchParams.set("options", `-c search_path=${schema}`);
  return u.toString();
}

describe.skipIf(ownerUrl === undefined)("migrate command on Postgres", () => {
  test("applies every file once, under the lock, as the owner; refuses depot_app unless MIGRATE_ALLOW_APP_ROLE", async () => {
    const schema = `migcmd_${randomUUID().replaceAll("-", "").slice(0, 12)}`;
    const admin = new pg.Pool({ connectionString: ownerUrl, max: 1 });
    await admin.query(`create schema ${schema}`);
    try {
      const env = { MIGRATE_DATABASE_URL: inSchema(ownerUrl!, schema), DATABASE_URL: "postgres://depot_app@unreachable.invalid/x" };
      // Two overlapping runs (a Job retry while the previous pod still terminates) take turns on the advisory lock.
      await Promise.all([migrateFromEnv(env), migrateFromEnv(env)]);
      await migrateFromEnv(env);
      const files = (await import("node:fs/promises")).readdir;
      const { MIGRATIONS_DIR } = await import("../src/pgStore.ts");
      const want = (await files(MIGRATIONS_DIR)).filter((n: string) => n.endsWith(".sql")).length;
      const got = await admin.query(`select count(*)::int as n from ${schema}.schema_migrations`);
      expect(got.rows[0].n).toBe(want);
      if (appUrl !== undefined) {
        const empty = `migcmd_${randomUUID().replaceAll("-", "").slice(0, 12)}`;
        await admin.query(`create schema ${empty}; grant usage, create on schema ${empty} to depot_app`);
        try {
          const asApp = { MIGRATE_DATABASE_URL: inSchema(appUrl, empty), MIGRATE_RETRY_TIMEOUT: "0" };
          await expect(migrateFromEnv(asApp)).rejects.toThrow(/refusing to migrate/);
          const made = await admin.query("select count(*)::int as n from pg_tables where schemaname = $1", [empty]);
          expect(made.rows[0].n).toBe(0);
        } finally {
          await admin.query(`drop schema ${empty} cascade`);
        }
      }
    } finally {
      await admin.query(`drop schema ${schema} cascade`);
      await admin.end();
    }
  });
});
