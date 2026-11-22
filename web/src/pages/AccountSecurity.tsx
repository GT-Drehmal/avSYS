import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Monitor, Smartphone } from "lucide-react";
import { useState } from "react";
import { Navigate } from "react-router";
import { ProviderIcon } from "../components/ProviderIcon";
import { Button, Chip, CopyText, Field, Input, Modal, Notice, Section } from "../components/ui";
import { api, ApiError, qs, type Session } from "../lib/api";
import { describeDevice } from "../lib/device";
import { ago } from "../lib/format";
import { keys, useAction, useMe, useMeta } from "../lib/queries";
import { toast } from "../lib/toast";
import { AuthFrame } from "./Login";

const sessionsKey = ["sessions"] as const;

/** Codes to sign in with once each, shown the one time they are made. */
function RecoveryCodes({ codes }: { codes: string[] }) {
  return (
    <div className="flex flex-col gap-3">
      <Notice tone="warn">Save these codes. Each one signs you in once if you lose your phone. They are not shown again.</Notice>
      <ul aria-label="Recovery codes" className="grid grid-cols-2 gap-x-6 gap-y-1 rounded-md border border-line bg-bg px-4 py-3 font-mono text-[13px]">
        {codes.map((c) => (
          <li key={c}>{c}</li>
        ))}
      </ul>
      <div>
        <CopyText value={codes.join("\n")} display="Copy all" className="text-xs text-muted" />
      </div>
    </div>
  );
}

/**
 * How someone shows it is them before changing their two-factor: their
 * password, or, with none to give, having signed in a moment ago. stale is
 * set once the panel said that was too long ago.
 */
function useProof() {
  const me = useMe().data!;
  const meta = useMeta().data;
  const [stale, setStale] = useState(false);
  const password = me.hasPassword && meta?.passwords !== false;
  return {
    password,
    /** Whether they have to sign in again first. */
    again: !password && (!me.recentSignIn || stale),
    /** Notes an error that says to sign in again. */
    failed: (err: unknown) => {
      if (err instanceof ApiError && err.code === "recent_sign_in") setStale(true);
    },
  };
}

/** Links to sign in again through each provider they have, back to this page. */
function SignInAgain() {
  const me = useMe().data!;
  const meta = useMeta().data;
  const linked = new Set(me.identities.map((i) => i.provider));
  const next = window.location.pathname;
  return (
    <div className="flex flex-col gap-3">
      <Notice tone="info">Sign in again to continue.</Notice>
      {(meta?.providers ?? [])
        .filter((p) => linked.has(p.id))
        .map((p) => (
          <a
            key={p.id}
            href={`/api/v1/auth/${p.id}/start?${qs({ next })}`}
            className="flex h-9 items-center justify-center gap-2 rounded-md border border-line-strong bg-raised text-[13px] font-medium hover:bg-hover"
          >
            <ProviderIcon id={p.id} />
            Continue with {p.name}
          </a>
        ))}
    </div>
  );
}

/**
 * Turning two-factor on: the password (or a recent sign-in, for someone
 * with none), then the QR code and a code from the app, then the recovery
 * codes. onDone is called once those are put away.
 */
export function TwoFactorSetup({ onDone, onCancel }: { onDone: () => void; onCancel?: () => void }) {
  const qc = useQueryClient();
  const proof = useProof();
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [key, setKey] = useState<{ secret: string; qr: string } | null>(null);
  const [codes, setCodes] = useState<string[] | null>(null);
  const start = useAction(() => api.post<{ secret: string; qr: string }>("/me/two-factor/setup", { password }));
  const confirm = useAction(() => api.post<{ recoveryCodes: string[] }>("/me/two-factor", { code }));

  if (codes) {
    return (
      <div className="flex flex-col gap-4">
        <RecoveryCodes codes={codes} />
        <div className="flex justify-end">
          <Button
            variant="primary"
            onClick={async () => {
              await qc.invalidateQueries({ queryKey: keys.me });
              onDone();
            }}
          >
            Done
          </Button>
        </div>
      </div>
    );
  }
  if (key) {
    return (
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          confirm.mutate(undefined, { onSuccess: (r) => setCodes(r.recoveryCodes) });
        }}
      >
        <p className="text-[13px] text-muted">Scan this with an authenticator app, then enter the code it shows.</p>
        <img src={key.qr} alt="QR code" width={180} height={180} className="rounded-md bg-white p-2" />
        <div className="text-xs text-muted">
          Or enter this key: <CopyText value={key.secret} className="text-xs" wrap />
        </div>
        <Field label="Code" missing="Enter the code.">
          {(id) => <Input id={id} name="totp" required autoFocus inputMode="numeric" autoComplete="one-time-code" value={code} onChange={(e) => setCode(e.target.value)} className="w-36 font-mono" />}
        </Field>
        <div className="flex justify-end gap-2">
          {onCancel && <Button onClick={onCancel}>Cancel</Button>}
          <Button type="submit" variant="primary" loading={confirm.isPending}>
            Turn on
          </Button>
        </div>
      </form>
    );
  }
  if (proof.again) {
    return (
      <div className="flex flex-col gap-4">
        <SignInAgain />
        {onCancel && (
          <div className="flex justify-end">
            <Button onClick={onCancel}>Cancel</Button>
          </div>
        )}
      </div>
    );
  }
  return (
    <form
      className="flex flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault();
        start.mutate(undefined, { onSuccess: setKey, onError: proof.failed });
      }}
    >
      {proof.password ? (
        <Field label="Password" missing="Enter your password.">
          {(id) => <Input id={id} type="password" required autoFocus autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />}
        </Field>
      ) : (
        <p className="text-[13px] text-muted">You need an authenticator app on your phone.</p>
      )}
      <div className="flex justify-end gap-2">
        {onCancel && <Button onClick={onCancel}>Cancel</Button>}
        <Button type="submit" variant="primary" loading={start.isPending}>
          Continue
        </Button>
      </div>
    </form>
  );
}

/** Asks for the password, or a recent sign-in, before doing something to two-factor. */
function Confirming({ title, action, danger, run, onClose }: { title: string; action: string; danger?: boolean; run: (password: string) => Promise<unknown>; onClose: () => void }) {
  const proof = useProof();
  const [password, setPassword] = useState("");
  const go = useAction(() => run(password));
  return (
    <Modal open onOpenChange={onClose} title={title}>
      {proof.again ? (
        <div className="flex flex-col gap-4">
          <SignInAgain />
          <div className="flex justify-end">
            <Button onClick={onClose}>Cancel</Button>
          </div>
        </div>
      ) : (
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            go.mutate(undefined, { onError: proof.failed });
          }}
        >
          {proof.password && (
            <Field label="Password" missing="Enter your password.">
              {(id) => <Input id={id} type="password" required autoFocus autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />}
            </Field>
          )}
          <div className="flex justify-end gap-2">
            <Button onClick={onClose}>Cancel</Button>
            <Button type="submit" variant={danger ? "danger" : "primary"} loading={go.isPending}>
              {action}
            </Button>
          </div>
        </form>
      )}
    </Modal>
  );
}

export function TwoFactor() {
  const me = useMe().data!;
  const qc = useQueryClient();
  const [open, setOpen] = useState<"setup" | "off" | "codes" | null>(null);
  const [codes, setCodes] = useState<string[] | null>(null);
  const close = () => setOpen(null);
  return (
    <Section title="Two-factor" description="A code from your phone each time you sign in.">
      {me.twoFactor ? (
        <div className="flex flex-wrap items-center gap-3">
          <Chip className="border-accent/40 text-accent">On</Chip>
          <span className="text-xs text-muted">
            {me.recoveryCodes} recovery code{me.recoveryCodes === 1 ? "" : "s"} left
          </span>
          <span className="flex-1" />
          <Button size="sm" onClick={() => setOpen("codes")}>
            New recovery codes
          </Button>
          <Button size="sm" disabled={me.twoFactorRequired} title={me.twoFactorRequired ? "You need two-factor here." : undefined} onClick={() => setOpen("off")}>
            Turn off
          </Button>
        </div>
      ) : (
        <div>
          <Button onClick={() => setOpen("setup")}>Set up</Button>
        </div>
      )}

      <Modal open={open === "setup"} onOpenChange={close} title="Set up two-factor">
        <TwoFactorSetup onDone={close} onCancel={close} />
      </Modal>
      {open === "off" && (
        <Confirming
          title="Turn off two-factor?"
          action="Turn off"
          danger
          onClose={close}
          run={async (password) => {
            await api.post("/me/two-factor/off", { password });
            await qc.invalidateQueries({ queryKey: keys.me });
            toast.info("Two-factor is off");
            close();
          }}
        />
      )}
      {open === "codes" && (
        <Confirming
          title="New recovery codes"
          action="Make new codes"
          onClose={close}
          run={async (password) => {
            const r = await api.post<{ recoveryCodes: string[] }>("/me/two-factor/recovery-codes", { password });
            await qc.invalidateQueries({ queryKey: keys.me });
            setCodes(r.recoveryCodes);
            close();
          }}
        />
      )}
      <Modal open={!!codes} onOpenChange={() => setCodes(null)} title="New recovery codes" description="The old ones no longer work.">
        <div className="flex flex-col gap-4">
          {codes && <RecoveryCodes codes={codes} />}
          <div className="flex justify-end">
            <Button variant="primary" onClick={() => setCodes(null)}>
              Done
            </Button>
          </div>
        </div>
      </Modal>
    </Section>
  );
}

const mobile = (ua: string) => /iPhone|iPad|iPod|Android|Mobile/.test(ua);

export function Sessions() {
  const sessions = useQuery({ queryKey: sessionsKey, queryFn: () => api.get<Session[]>("/me/sessions") });
  const end = useAction((id: string) => api.del(`/me/sessions/${id}`), [sessionsKey]);
  const endOthers = useAction(() => api.del("/me/sessions"), [sessionsKey]);
  const others = (sessions.data ?? []).filter((s) => !s.current).length;
  return (
    <Section title="Sessions" description="Where you are signed in.">
      {sessions.data && sessions.data.length > 0 && (
        <ul className="overflow-hidden rounded-md border border-line bg-surface">
          {sessions.data.map((s) => {
            const Icon = mobile(s.userAgent) ? Smartphone : Monitor;
            return (
              <li key={s.id} className="flex items-center gap-3 border-b border-line px-3 py-2 last:border-b-0">
                <Icon className="size-4 shrink-0 text-faint" />
                <div className="min-w-0 flex-1">
                  <p className="truncate text-[13px] font-medium">
                    {describeDevice(s.userAgent)}
                    {s.current && <span className="ml-2 text-xs font-normal text-accent">this device</span>}
                  </p>
                  <p className="truncate text-xs text-faint">
                    {s.address && <span className="font-mono">{s.address} · </span>}
                    signed in {ago(s.createdAt)} · used {ago(s.lastUsedAt)}
                  </p>
                </div>
                {!s.current && (
                  <Button size="sm" variant="ghost" loading={end.isPending && end.variables === s.id} aria-label={`Sign out ${describeDevice(s.userAgent)}`} onClick={() => end.mutate(s.id)}>
                    Sign out
                  </Button>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {sessions.data && sessions.data.length === 0 && <p className="text-[13px] text-muted">No sessions.</p>}
      <div>
        <Button disabled={others === 0} loading={endOthers.isPending} onClick={() => endOthers.mutate(undefined, { onSuccess: () => toast.info("Signed out everywhere else") })}>
          Sign out everywhere else
        </Button>
      </div>
    </Section>
  );
}

/** For someone who has to set up two-factor before going on. */
export function TwoFactorRequired() {
  const me = useMe().data;
  if (!me?.mustSetUpTwoFactor) return <Navigate to="/" replace />;
  return (
    <AuthFrame title="Set up two-factor" subtitle="You need two-factor to use this panel.">
      <div className="flex flex-col gap-5">
        <TwoFactorSetup onDone={() => window.location.assign("/")} />
        <button
          className="text-left text-[13px] text-muted hover:text-fg"
          onClick={async () => {
            await api.post("/auth/logout");
            window.location.assign("/login");
          }}
        >
          Sign out
        </button>
      </div>
    </AuthFrame>
  );
}
