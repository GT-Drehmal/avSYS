import { defineConfig, devices } from "@playwright/test";

// The browser tests run against a panel with nothing behind it
// (cmd/avsys-uitest): no cluster and no internet, so they are quick and
// fail only for a reason in this repository. Each test runs in both engines:
// what one forgives the other may not.
const port = 8099;

export default defineConfig({
  testDir: "tests",
  outputDir: "test-results",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: process.env.CI ? [["github"], ["list"]] : "list",
  timeout: 30_000,
  expect: { timeout: 7_000 },
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    viewport: { width: 1280, height: 900 },
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    // CI installs Playwright's own build of each; elsewhere the Chrome that
    // is already on the machine saves a download.
    { name: "chromium", use: { ...devices["Desktop Chrome"], channel: process.env.CI ? undefined : "chrome" } },
    { name: "webkit", use: { ...devices["Desktop Safari"] } },
  ],
  webServer: {
    // CI builds the panel first and names it here; otherwise it is compiled on the way.
    command: `${process.env.AVSYS_UITEST ?? "go run ../cmd/avsys-uitest"} --listen 127.0.0.1:${port} --web-dir dist --upstream tests/upstream --ready tests/.panel.json`,
    url: `http://127.0.0.1:${port}/api/v1/meta`,
    reuseExistingServer: false,
    timeout: 180_000,
    stdout: "ignore",
    stderr: "pipe",
  },
});
