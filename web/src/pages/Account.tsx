import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
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
  const qc = useQueryClient();
  const me = useMe().data!;
  const [name, setName] = useState("");
  // The check open, if one is: the account and until when.
  const [check, setCheck] = useState<{ uuid: string; name: string; until: string } | null>(null);
  const accounts = me.minecraftAccounts ?? (me.minecraft ? [me.minecraft] : []);
  const link = useAction(() => api.post("/me/minecraft-accounts", { name }), [keys.me]);
  const unlink = useAction((uuid: string) => api.del(`/me/minecraft-accounts/${uuid}`), [keys.me]);
  const main = useAction((uuid: string) => api.post(`/me/minecraft-accounts/${uuid}/main`), [keys.me]);
  const verify = useAction((uuid: string) => api.post<{ name: string; until: string }>(`/me/minecraft-accounts/${uuid}/verify`));
  const busy = link.isPending || unlink.isPending || main.isPending;
  const checked = check && accounts.find((a) => a.uuid === check.uuid);
  // While a check is open, the account is looked at until it is proven.
  useEffect(() => {
    if (!check) return;
    if (checked?.verified) {
      toast.info(`${check.name} is verified`);
      setCheck(null);
      return;
    }
    const left = new Date(check.until).getTime() - Date.now();
    if (left <= 0) {
      setCheck(null);
      return;
    }
    const t = setInterval(() => qc.invalidateQueries({ queryKey: keys.me }), 3000);
    const end = setTimeout(() => setCheck(null), left);
    return () => {
      clearInterval(t);
      clearTimeout(end);
    };
  }, [check, checked?.verified, qc]);
  return (
    <Section
      title="Minecraft accounts"
      description="Every account you play as: each is made an operator, or let in, on servers where your role allows it. Your main one is the name shown beside you. Below the operator role, only accounts you have verified are let in."
    >
      {accounts.length > 0 && (
        <ul className="overflow-hidden rounded-lg border border-line bg-surface">
          {accounts.map((a) => (
            <li key={a.uuid} className="flex items-center gap-3 border-b border-line px-4 py-2.5 last:border-b-0">
              <span className="min-w-0 flex-1 truncate font-mono text-[13px] font-medium">{a.name}</span>
              {a.verified ? (
                <span className="text-xs text-muted">verified</span>
              ) : (
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={busy}
                  loading={verify.isPending && verify.variables === a.uuid}
                  onClick={() => verify.mutate(a.uuid, { onSuccess: (got) => setCheck({ uuid: a.uuid, name: got.name, until: got.until }) })}
                >
                  Verify
                </Button>
              )}
              {a.main ? (
                <Chip>main</Chip>
              ) : (
                <Button size="sm" variant="ghost" disabled={busy} onClick={() => main.mutate(a.uuid)}>
                  Make main
                </Button>
              )}
              <Button size="sm" disabled={busy} loading={unlink.isPending && unlink.variables === a.uuid} aria-label={`Unlink ${a.name}`} onClick={() => unlink.mutate(a.uuid)}>
                Unlink
              </Button>
            </li>
          ))}
        </ul>
      )}
      {check && !checked?.verified && (
        <Notice tone="info">
          Open Minecraft signed in as <span className="font-mono">{check.name}</span> and join any server of this panel, at its shared address, before{" "}
          {new Date(check.until).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}. Your game proves the account is yours, and tells you it worked.
        </Notice>
      )}
      <form
        className="flex items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          link.mutate(undefined, { onSuccess: () => setName("") });
        }}
      >
        <Input
          required
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={accounts.length > 0 ? "Another name you play as" : "Your name in game"}
          className="w-56"
          aria-label="Minecraft username"
          spellCheck={false}
          autoComplete="off"
        />
        <Button type="submit" loading={link.isPending} disabled={busy && !link.isPending}>
          Link
        </Button>
      </form>
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
