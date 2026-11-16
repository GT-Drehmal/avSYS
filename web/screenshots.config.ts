import { defineConfig, devices } from "@playwright/test";
import tests from "./playwright.config";

// The documentation's pictures of the panel (docs/images), taken from the
// same test panel as the browser tests. Not a test: `make docs-screenshots`
// runs it, and the pictures it writes are committed.
const [panel] = [tests.webServer].flat();

export default defineConfig({
  ...tests,
  testDir: "screenshots",
  outputDir: "test-results/screenshots",
  fullyParallel: false,
  workers: 1,
  reporter: "list",
  timeout: 60_000,
  webServer: panel,
  projects: [
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        channel: process.env.CI ? undefined : "chrome",
        viewport: { width: 1200, height: 750 },
        deviceScaleFactor: 2,
        colorScheme: "dark",
      },
    },
  ],
});
