import { createServer } from "node:net";
import { fileURLToPath } from "node:url";
import { defineConfig } from "@playwright/test";

async function freePort(): Promise<number> {
  const socket = createServer();
  return new Promise((resolve, reject) => {
    socket.once("error", reject);
    socket.listen(0, "127.0.0.1", () => {
      const address = socket.address();
      socket.close((error) => {
        if (error) reject(error);
        else if (address && typeof address !== "string") resolve(address.port);
        else reject(new Error("Could not allocate a port for rua demo"));
      });
    });
  });
}

// Workers reload this module. They must use the port picked by the runner.
const port = process.env.RUA_E2E_PORT ?? String(await freePort());
process.env.RUA_E2E_PORT = port;
const address = `127.0.0.1:${port}`;
const baseURL = `http://${address}`;

export default defineConfig({
  testDir: "./tests",
  workers: 1,
  timeout: 30_000,
  expect: { timeout: 8_000 },
  use: {
    baseURL,
    browserName: "chromium",
    timezoneId: "UTC",
    trace: "retain-on-failure",
  },
  webServer: {
    command: "./.bin/rua demo",
    cwd: fileURLToPath(new URL(".", import.meta.url)),
    env: { LISTEN_ADDR: address, METRICS_ADDR: "" },
    url: `${baseURL}/healthz`,
    reuseExistingServer: false,
  },
  projects: (["light", "dark"] as const).flatMap((colorScheme) => [
    {
      name: `desktop-${colorScheme}`,
      use: { viewport: { width: 1280, height: 800 }, colorScheme },
    },
    {
      name: `phone-${colorScheme}`,
      use: {
        viewport: { width: 390, height: 844 },
        isMobile: true,
        hasTouch: true,
        deviceScaleFactor: 1,
        colorScheme,
      },
    },
  ]),
});
