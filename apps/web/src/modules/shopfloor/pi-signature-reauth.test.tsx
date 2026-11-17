import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AccountProvider } from "../../shell/auth/account";
import type { Auth } from "../../shell/auth/auth";
import type { Session } from "../../shell/auth/oidc";
import { SESSION_EXPIRED_EVENT } from "../../shell/api/http";
import { createProcessMessage, ShopfloorApiError, type RecipeDocument } from "./api/client";
import { PiSheetRunner } from "./PiSheetRunner";

const session: Session = { accessToken: "t", refreshToken: undefined, idToken: undefined, expiresAt: 0, subject: "u-1", tenantId: "t-1", duties: [], name: "Ana Ruiz" };

const doc: RecipeDocument = {
  procedure: {
    name: "Batch",
    unitProcedures: [
      {
        name: "Reactor",
        operations: [
          {
            name: "Heat",
            phases: [
              {
                name: "Ramp",
                instructions: [
                  { seq: 1, category: "instruction-text", text: "Check the vessel is empty." },
                  { seq: 2, category: "signature-request", text: "Verify the charge.", meaning: "verified" },
                ],
              },
            ],
          },
        ],
      },
    ],
  },
};

const RECIPE = "0195f0a0-0000-7000-8000-0000000000aa";

afterEach(() => sessionStorage.clear());

function mount(onRecord: (m: unknown) => Promise<void>, withAccount: boolean) {
  const auth: Auth = { ensureSession: vi.fn(), accessToken: vi.fn(), login: vi.fn(), logout: vi.fn(), reauthenticate: vi.fn().mockResolvedValue(undefined) };
  const runner = <PiSheetRunner doc={doc} recipeId={RECIPE} onRecord={onRecord} />;
  const view = render(withAccount ? <AccountProvider auth={auth} session={session}>{runner}</AccountProvider> : runner);
  return { auth, view };
}

async function signStep() {
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Next step" }));
  await user.type(await screen.findByLabelText(/your name/), "Ana Ruiz");
  await user.click(screen.getByRole("checkbox"));
  await user.click(screen.getByRole("button", { name: "Finish" }));
  return user;
}

describe("signing a PI step needs a fresh sign-in", () => {
  it("offers to sign in again when the service refuses the signature, and returns to the same step", async () => {
    const onRecord = vi.fn().mockRejectedValue(new ShopfloorApiError(403, "SF-0188", "you signed in 12 minutes ago; sign in again"));
    const { auth, view } = mount(onRecord, true);
    const user = await signStep();
    expect(await screen.findByText("Sign in again to sign this step")).toBeInTheDocument();
    expect(screen.queryByText("The step was not saved")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Sign in again" }));
    expect(auth.reauthenticate).toHaveBeenCalledOnce();
    expect(sessionStorage.getItem(`depot.pi.resume.${RECIPE}`)).toBe("1");
    view.unmount();

    // After the sign-in the page loads again: the sheet opens on the signature step, once.
    mount(vi.fn().mockResolvedValue(undefined), true);
    expect(await screen.findByText("Step 2 of 2")).toBeInTheDocument();
    expect(sessionStorage.getItem(`depot.pi.resume.${RECIPE}`)).toBeNull();
  });

  it("says to sign in again by hand where the build has no sign-in, and shows other failures as before", async () => {
    mount(vi.fn().mockRejectedValue(new ShopfloorApiError(403, "SF-0188", "sign in again")), false);
    await signStep();
    expect(await screen.findByText("Sign in again to sign this step")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Sign in again" })).not.toBeInTheDocument();
    expect(screen.getByText(/press the button once more/)).toBeInTheDocument();
  });

  it("keeps the ordinary failure message for any other error", async () => {
    mount(vi.fn().mockRejectedValue(new ShopfloorApiError(500, "COM-0500", "internal error")), true);
    await signStep();
    await waitFor(() => expect(screen.getByText("The step was not saved")).toBeInTheDocument());
    expect(screen.queryByText("Sign in again to sign this step")).not.toBeInTheDocument();
  });
});

describe("the refusal goes through the shell's request path", () => {
  it("is a 403 the kiosk handles itself: it does not raise the session-expired banner, whose sign-in would not refresh auth_time", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ code: "SF-0188", detail: "sign in again" }), { status: 403, headers: { "Content-Type": "application/problem+json" } })));
    const expired = vi.fn();
    window.addEventListener(SESSION_EXPIRED_EVENT, expired);
    try {
      await expect(
        createProcessMessage({ source: "kiosk", sourceMessageId: "r:s", categoryCode: "process-data", orderId: "o", phasePath: "p", occurredAt: "2026-11-01T00:00:00Z", values: [] }),
      ).rejects.toMatchObject({ code: "SF-0188", status: 403 });
      expect(expired).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
      vi.unstubAllGlobals();
    }
  });
});
