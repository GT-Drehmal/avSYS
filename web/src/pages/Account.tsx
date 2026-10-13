import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { useState } from "react";
import { PicturePicker } from "../components/Picture";
import { Button, Chip, CopyText, Field, Input, Modal, Notice, PageHeader, Section, Textarea } from "../components/ui";
import { api, type APIToken, type SSHKey } from "../lib/api";
import { ago } from "../lib/format";
import { keys, useAction, useMe, useMeta } from "../lib/queries";
import { toast } from "../lib/toast";

const sshKeys = ["ssh-keys"] as const;
const apiTokens = ["api-tokens"] as const;

function Profile() {
  const me = useMe().data!;
  const qc = useQueryClient();
  const [displayName, setDisplayName] = useState(me.user.displayName);
  const save = useAction(() => api.patch("/me", { displayName }), [keys.me]);
  return (
    <Section title="Profile">
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate(undefined, { onSuccess: () => toast.info("Profile saved") });
        }}
      >
        <PicturePicker name={me.user.username} shape="round" path="/me/avatar" has={me.user.avatarUrl === `/api/v1/users/${me.user.id}/avatar`} url={me.user.avatarUrl || undefined} onChange={() => qc.invalidateQueries({ queryKey: keys.me })} />
        <Field label="Username" hint="It cannot be changed.">
          {(id) => <Input id={id} disabled value={me.user.username} className="font-mono" />}
        </Field>
        <Field label="Display name">{(id) => <Input id={id} value={displayName} onChange={(e) => setDisplayName(e.target.value)} />}</Field>
        <div>
          <Button type="submit" variant="primary" disabled={displayName === me.user.displayName} loading={save.isPending}>
            Save
          </Button>
        </div>
      </form>
    </Section>
  );
}

function Minecraft() {
  const me = useMe().data!;
  const [name, setName] = useState("");
  const link = useAction(() => api.put("/me/minecraft", { name }), [keys.me]);
  const unlink = useAction(() => api.del("/me/minecraft"), [keys.me]);
  return (
    <Section title="Minecraft account" description="Makes you an operator in game on servers where your role allows it.">
      {me.minecraft ? (
        <div className="flex items-center gap-3 rounded-lg border border-line bg-surface px-4 py-2.5">
          {/* Said in full: the name alone, under this heading, does not read as "the player you are". */}
          <div className="min-w-0 flex-1">
            <p className="truncate text-[13px]">
              In game you are <span className="font-mono font-medium">{me.minecraft.name}</span>
            </p>
            <p className="truncate text-xs text-faint">Not the name you play as? Unlink it and link the right one.</p>
          </div>
          <Button size="sm" loading={unlink.isPending} onClick={() => unlink.mutate(undefined)}>
            Unlink
          </Button>
        </div>
      ) : (
        <form
          className="flex items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            link.mutate(undefined, { onSuccess: () => setName("") });
          }}
        >
          <Input required value={name} onChange={(e) => setName(e.target.value)} placeholder="Your name in game" className="w-56" aria-label="Minecraft username" spellCheck={false} autoComplete="off" />
          <Button type="submit" loading={link.isPending}>
            Link
          </Button>
        </form>
      )}
    </Section>
  );
}

function SSHKeys() {
  const me = useMe().data!;
  const keysQuery = useQuery({ queryKey: sshKeys, queryFn: () => api.get<SSHKey[]>("/me/ssh-keys") });
  const [adding, setAdding] = useState(false);
  const [publicKey, setPublicKey] = useState("");
  const [name, setName] = useState("");
  const github = me.identities.find((i) => i.provider === "github")?.login ?? "";
  const [githubUser, setGithubUser] = useState(github);

  const add = useAction(() => api.post("/me/ssh-keys", { name, publicKey }), [sshKeys]);
  const remove = useAction((id: string) => api.del(`/me/ssh-keys/${id}`), [sshKeys]);
  const importKeys = useAction(() => api.post<{ added: number }>("/me/ssh-keys/import", { githubUsername: githubUser }), [sshKeys]);

  return (
    <Section title="SSH keys" description="For SSH and VS Code access to servers.">
      {keysQuery.data && keysQuery.data.length > 0 ? (
        <ul className="overflow-hidden rounded-md border border-line bg-surface">
          {keysQuery.data.map((k) => (
            <li key={k.id} className="flex items-center gap-3 border-b border-line px-3 py-2 last:border-b-0">
              <div className="min-w-0 flex-1">
                <p className="truncate text-[13px] font-medium">{k.name}</p>
                <p className="truncate font-mono text-[11px] text-faint">{k.fingerprint}</p>
              </div>
              <span className="shrink-0 text-xs text-faint">{k.lastUsedAt ? `used ${ago(k.lastUsedAt)}` : "never used"}</span>
              <Button size="sm" variant="ghost" icon={<Trash2 className="size-3.5" />} aria-label={`Delete key ${k.name}`} onClick={() => remove.mutate(k.id)} />
            </li>
          ))}
        </ul>
      ) : (
        keysQuery.data && <Notice tone="info">No keys yet. Add one to use SSH.</Notice>
      )}
      <div className="flex flex-wrap items-end gap-2">
        <Button onClick={() => setAdding(true)}>Add key</Button>
        <form
          className="flex items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            importKeys.mutate(undefined, { onSuccess: (r) => toast.info(r.added === 0 ? "No new keys found on GitHub" : `Imported ${r.added} key${r.added === 1 ? "" : "s"}`) });
          }}
        >
          <Input required value={githubUser} onChange={(e) => setGithubUser(e.target.value)} placeholder="GitHub username" className="w-44" aria-label="GitHub username" />
          <Button type="submit" loading={importKeys.isPending}>
            Import from GitHub
          </Button>
        </form>
      </div>

      <Modal open={adding} onOpenChange={setAdding} title="Add SSH key" description="Paste a public key, the contents of a file such as ~/.ssh/id_ed25519.pub.">
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            add.mutate(undefined, {
              onSuccess: () => {
                setAdding(false);
                setPublicKey("");
                setName("");
              },
            });
          }}
        >
          <Field label="Public key">
            {(id) => <Textarea id={id} required autoFocus rows={4} value={publicKey} onChange={(e) => setPublicKey(e.target.value)} placeholder="ssh-ed25519 AAAA… you@laptop" className="font-mono text-xs break-all" spellCheck={false} />}
          </Field>
          <Field label="Name" hint="Optional.">
            {(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder="Laptop" />}
          </Field>
          <div className="flex justify-end gap-2">
            <Button onClick={() => setAdding(false)}>Cancel</Button>
            <Button type="submit" variant="primary" loading={add.isPending}>
              Add key
            </Button>
          </div>
        </form>
      </Modal>
    </Section>
  );
}

function APITokens() {
  const tokens = useQuery({ queryKey: apiTokens, queryFn: () => api.get<APIToken[]>("/me/tokens") });
  const [name, setName] = useState("");
  const [secret, setSecret] = useState<string | null>(null);
  const create = useAction(() => api.post<{ secret: string }>("/me/tokens", { name }), [apiTokens]);
  const remove = useAction((id: string) => api.del(`/me/tokens/${id}`), [apiTokens]);
  return (
    <Section
      title="API tokens"
      description="For scripts and tools. A token acts as you."
    >
      {tokens.data && tokens.data.length > 0 && (
        <ul className="overflow-hidden rounded-md border border-line bg-surface">
          {tokens.data.map((t) => (
            <li key={t.id} className="flex items-center gap-3 border-b border-line px-3 py-2 last:border-b-0">
              <p className="min-w-0 flex-1 truncate text-[13px] font-medium">{t.name}</p>
              <span className="shrink-0 text-xs text-faint">{t.lastUsedAt ? `used ${ago(t.lastUsedAt)}` : "never used"}</span>
              <Button size="sm" variant="ghost" icon={<Trash2 className="size-3.5" />} aria-label={`Revoke token ${t.name}`} onClick={() => remove.mutate(t.id)} />
            </li>
          ))}
        </ul>
      )}
      <form
        className="flex items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate(undefined, {
            onSuccess: (r) => {
              setSecret(r.secret);
              setName("");
            },
          });
        }}
      >
        <Input required value={name} onChange={(e) => setName(e.target.value)} placeholder="What is it for?" className="w-56" aria-label="Token name" />
        <Button type="submit" loading={create.isPending}>
          Create token
        </Button>
      </form>
      <Modal open={!!secret} onOpenChange={() => setSecret(null)} title="Your new token" description="Copy it now. It is not shown again.">
        <div className="flex flex-col gap-4">
          <div className="rounded-md border border-line bg-bg px-3 py-2">{secret && <CopyText value={secret} className="text-xs" wrap />}</div>
          <div className="flex justify-end">
            <Button variant="primary" onClick={() => setSecret(null)}>
              Done
            </Button>
          </div>
        </div>
      </Modal>
    </Section>
  );
}

function SignIn() {
  const me = useMe().data!;
  const meta = useMeta().data;
  const qc = useQueryClient();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const change = useAction(() => api.post("/me/password", { current, new: next }));
  const linked = new Map(me.identities.map((i) => [i.provider, i.login]));
  return (
    <Section title="Sign-in">
      {meta && meta.providers.length > 0 && (
        <ul className="overflow-hidden rounded-md border border-line bg-surface">
          {meta.providers.map((p) => (
            <li key={p.id} className="flex items-center gap-3 border-b border-line px-3 py-2 text-[13px] last:border-b-0">
              <span className="flex-1 font-medium">{p.name}</span>
              {linked.has(p.id) ? (
                <Chip className="border-accent/40 text-accent">{linked.get(p.id) || "linked"}</Chip>
              ) : (
                <a href={`/api/v1/auth/${p.id}/start?next=/account`} className="text-accent hover:underline">
                  Link account
                </a>
              )}
            </li>
          ))}
        </ul>
      )}
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          change.mutate(undefined, {
            onSuccess: () => {
              setCurrent("");
              setNext("");
              qc.invalidateQueries({ queryKey: keys.me });
              toast.info("Password updated");
            },
          });
        }}
      >
        {me.hasPassword && (
          <Field label="Current password">{(id) => <Input id={id} type="password" required autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} />}</Field>
        )}
        <Field label={me.hasPassword ? "New password" : "Set a password"} hint="At least 8 characters.">
          {(id) => <Input id={id} type="password" required minLength={8} autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} />}
        </Field>
        <div>
          <Button type="submit" loading={change.isPending}>
            {me.hasPassword ? "Change password" : "Set password"}
          </Button>
        </div>
      </form>
    </Section>
  );
}

export function Account() {
  return (
    <>
      <PageHeader title="Account" />
      <Profile />
      <Minecraft />
      <SSHKeys />
      <APITokens />
      <SignIn />
    </>
  );
}
