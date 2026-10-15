import { expect, test, type Page } from "@playwright/test";
import { buildSurfaceFrameDocument } from "../packages/views/plugins/surface-document";

// A routed hosted-document fixture, not a real launch API. Mirror the relevant
// bootstrap contract: transfer a guest-created port, register error reporting,
// and execute plugin code in a dynamically inserted script. Chromium supplies
// the actual opaque sandbox, script errors, and document lifecycle events.
function hostedDocument(code: string, challenge: string): string {
  return `<!doctype html><body><script>
    let failed = false;
    function reportError() {
      if (failed) return;
      failed = true;
      parent.postMessage({ type: "multica:plugin-surface-error" }, "*");
    }
    addEventListener("error", reportError);
    addEventListener("unhandledrejection", reportError);
    addEventListener("pagehide", () => {
      parent.postMessage({ type: "multica:plugin-surface-navigated" }, "*");
    });
    const channel = new MessageChannel();
    window.pluginPort = channel.port2;
    parent.postMessage({
      type: "multica:plugin-bridge-connect", version: 2,
      challenge: ${JSON.stringify(challenge)}
    }, "*", [channel.port1]);
    const plugin = document.createElement("script");
    plugin.textContent = ${JSON.stringify(code)};
    document.body.appendChild(plugin);
  </script>`;
}

interface SurfaceState {
  launches: string[];
  ran: number;
  errors: number;
  navigated: number;
  port?: MessagePort;
}

async function mountHost(page: Page, srcdoc: string): Promise<void> {
  await page.setContent('<!doctype html><body><iframe id="surface" sandbox="allow-scripts allow-same-origin"></iframe>');
  await page.evaluate((document) => {
    const frame = window.document.querySelector<HTMLIFrameElement>("#surface")!;
    const state: SurfaceState = { launches: [], ran: 0, errors: 0, navigated: 0 };
    (window as unknown as { __surfaceState: SurfaceState }).__surfaceState = state;
    // Match PluginSurfaceFrame: arm the host listener BEFORE assigning srcdoc.
    window.addEventListener("message", (event) => {
      if (event.source !== frame.contentWindow) return;
      const data = event.data as { type?: string; challenge?: string } | null;
      if (data?.type === "multica:plugin-bridge-connect" && event.ports[0]) {
        state.port?.close();
        state.port = event.ports[0];
        state.launches.push(data.challenge!);
        state.port.onmessage = (message) => {
          if (message.data === "plugin-test-ran") state.ran++;
        };
        state.port.start();
      }
      if (data?.type === "multica:plugin-surface-error") state.errors++;
      if (data?.type === "multica:plugin-surface-navigated" ||
          data?.type === "multica:plugin-surface-navigation-blocked") state.navigated++;
    });
    frame.srcdoc = document;
  }, srcdoc);
}

async function surfaceState(page: Page): Promise<Omit<SurfaceState, "port">> {
  return page.evaluate(() => {
    const { launches, ran, errors, navigated } =
      (window as unknown as { __surfaceState: SurfaceState }).__surfaceState;
    return { launches, ran, errors, navigated };
  });
}

test.describe("plugin surface document (real Chromium, hosted opaque guest)", () => {
  test("a host-authored wrapper reload is not reported as hostile navigation", async ({ page }) => {
    const code = 'addEventListener("load", () => window.pluginPort.postMessage("plugin-test-ran"));';
    await page.route("https://plugin-content.example.test/**", async (route) => {
      const challenge = new URL(route.request().url()).pathname.slice(1);
      await route.fulfill({
        contentType: "text/html",
        headers: { "Content-Security-Policy": "default-src 'none'; script-src 'unsafe-inline'" },
        body: hostedDocument(code, challenge),
      });
    });
    const first = buildSurfaceFrameDocument({
      url: "https://plugin-content.example.test/first", bridgeToken: "first",
    });
    const replacement = buildSurfaceFrameDocument({
      url: "https://plugin-content.example.test/replacement", bridgeToken: "replacement",
    });
    await mountHost(page, first);
    await expect.poll(() => surfaceState(page)).toEqual({
      launches: ["first"], ran: 1, errors: 0, navigated: 0,
    });

    await page.locator("#surface").evaluate((frame, srcdoc) => {
      (frame as HTMLIFrameElement).srcdoc = srcdoc;
    }, replacement);
    await expect.poll(() => surfaceState(page)).toEqual({
      launches: ["first", "replacement"], ran: 2, errors: 0, navigated: 0,
    });
  });

  test("reports a first-line synchronous plugin error to the prearmed host", async ({ page }) => {
    await page.route("https://plugin-content.example.test/**", async (route) => {
      await route.fulfill({
        contentType: "text/html",
        headers: { "Content-Security-Policy": "default-src 'none'; script-src 'unsafe-inline'" },
        body: hostedDocument("throw new Error('plugin failed during bootstrap');", "proof"),
      });
    });
    await mountHost(page, buildSurfaceFrameDocument({
      url: "https://plugin-content.example.test/error", bridgeToken: "proof",
    }));
    await expect.poll(() => surfaceState(page)).toEqual({
      launches: ["proof"], ran: 0, errors: 1, navigated: 0,
    });
  });
});
