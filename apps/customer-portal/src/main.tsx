import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { App } from "./App";

// The demo build answers the BFF in the browser (src/demo.ts, not in any other build); every other build talks to the real BFF.
const ready = import.meta.env.MODE === "demo" ? import("./demo").then((m) => m.installDemo()) : Promise.resolve();

void ready.then(() =>
  createRoot(document.getElementById("root") as HTMLElement).render(
    <StrictMode>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </StrictMode>,
  ),
);

// An installable shell: the worker keeps the app's static files and an offline page, never data (public/sw.js). Production only, so
// the dev server and the tests never see a worker.
if (import.meta.env.PROD && import.meta.env.MODE !== "demo" && "serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js", { scope: "/" }).catch(() => undefined);
  });
}
