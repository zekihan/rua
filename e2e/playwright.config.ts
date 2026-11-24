import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./tests",
  workers: 1,
  timeout: 30_000,
  expect: { timeout: 8_000 },
  use: {
    browserName: "chromium",
    trace: "retain-on-failure",
  },
});
