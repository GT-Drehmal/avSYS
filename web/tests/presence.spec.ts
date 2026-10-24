import { expect, test } from "./panel";

// Who else is on a server now: their faces on its page and in the list, and
// in the file editor, who else has the file open.

test("members on a server see each other there, and the file each has open", async ({ page, as, api, org, pageAs }) => {
  const slug = await org(["alpha", "beta"]);
  const invite = await api("owner").post<{ token: string }>(`/orgs/${slug}/invites`, { role: "operator" });
  await api("operator").post(`/invites/${invite.token}/accept`);
  await as("owner");

  await page.goto(`/o/${slug}/s/alpha`);
  await expect(page.getByRole("heading", { name: "alpha" })).toBeVisible();
  // Not one's own face.
  await expect(page.getByRole("img", { name: /^Also here/ })).toHaveCount(0);

  const friend = await pageAs("operator");
  await friend.goto(`/o/${slug}/s/alpha/files?file=%2Fserver.properties`);
  await expect(page.getByRole("img", { name: /^Also here: / })).toBeVisible();
  await expect(friend.getByRole("img", { name: /^Also here: / })).toBeVisible();

  // In the list, on its row only.
  await page.goto(`/o/${slug}`);
  const row = (name: string) => page.getByRole("listitem").filter({ has: page.getByRole("link", { name: new RegExp(`^${name}`) }) });
  await expect(row("alpha").getByRole("img", { name: /^On it now: / })).toBeVisible();
  await expect(row("beta").getByRole("img", { name: /^On it now/ })).toHaveCount(0);

  // The same file open: said in the editor.
  await page.goto(`/o/${slug}/s/alpha/files?file=%2Fserver.properties`);
  await expect(page.getByText(/also has it open$/)).toBeVisible();

  // Gone to another server, then closed: gone from here.
  await friend.goto(`/o/${slug}/s/beta`);
  await expect(page.getByText(/also has it open$/)).toHaveCount(0);
  await expect(page.getByRole("img", { name: /^Also here/ })).toHaveCount(0);
  await page.goto(`/o/${slug}/s/beta`);
  await expect(page.getByRole("img", { name: /^Also here: / })).toBeVisible();
  await friend.close();
  await expect(page.getByRole("img", { name: /^Also here/ })).toHaveCount(0);
});

test("a file open in a folder is said too", async ({ page, as, api, org, pageAs }) => {
  const slug = await org(["alpha"]);
  const invite = await api("owner").post<{ token: string }>(`/orgs/${slug}/invites`, { role: "operator" });
  await api("operator").post(`/invites/${invite.token}/accept`);
  await as("owner");
  const friend = await pageAs("operator");
  await friend.goto(`/o/${slug}/s/alpha/files/plugins?file=%2Fplugins%2Fconfig.yml`);
  await page.goto(`/o/${slug}/s/alpha/files/plugins?file=%2Fplugins%2Fconfig.yml`);
  await expect(page.getByText(/also has it open$/)).toBeVisible();
});

test("after the stream may have missed some, who is here is asked for again", async ({ page, as }) => {
  await as("owner");
  let asked = 0;
  const friend = { user: { id: "friend-id", name: "Friend" }, server: "survival" };
  await page.route("**/api/v1/orgs/friends/presence", (route) => {
    asked++;
    return route.fulfill({ json: asked > 1 ? [friend] : [] });
  });
  // The stream says only that it fell behind.
  await page.route("**/api/v1/events?*", (route) => route.fulfill({ status: 200, headers: { "Content-Type": "text/event-stream" }, body: "event: resync\ndata: null\n\n" }));
  await page.goto("/o/friends/s/survival");
  await expect(page.getByRole("img", { name: "Also here: Friend" })).toBeVisible();
});
