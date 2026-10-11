import { useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Navigate, useSearchParams } from "react-router";
import { Wordmark } from "../components/Shell";
import { Button, Field, Input, Notice, Spinner } from "../components/ui";
import { api, ApiError, qs, type Meta } from "../lib/api";
import { keys, useMe, useMeta } from "../lib/queries";

/** The centred frame shared by the sign-in and invite pages. */
export function AuthFrame({ title, subtitle, children }: { title: string; subtitle?: ReactNode; children: ReactNode }) {
  return (
    <div className="relative flex min-h-dvh flex-col items-center justify-center overflow-hidden px-4 py-12">
      {/* A single soft light from above; the only decoration on the page. */}
      <div className="pointer-events-none absolute -top-64 left-1/2 size-[640px] -translate-x-1/2 rounded-full bg-accent/[0.07] blur-3xl" />
      <div className="relative w-full max-w-[340px]">
        <Wordmark className="mb-8 text-lg" />
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        {subtitle && <p className="mt-1.5 text-[13px] leading-relaxed text-muted">{subtitle}</p>}
        <div className="mt-6">{children}</div>
      </div>
    </div>
  );
}

const safeNext = (next: string | null) => (next && next.startsWith("/") && !next.startsWith("//") ? next : "/");

interface AuthFormProps {
  meta: Meta;
  mode: "signin" | "register";
  invite?: string;
  next: string;
}

export function AuthForm({ meta, mode, invite, next }: AuthFormProps) {
  const qc = useQueryClient();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      if (mode === "register") await api.post("/auth/register", { username, password, invite });
      else await api.post("/auth/login", { username, password });
      await qc.invalidateQueries({ queryKey: keys.meta });
      await qc.invalidateQueries({ queryKey: keys.me });
      // A full navigation, so every cached answer from the signed-out state is gone.
      window.location.assign(next);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Something went wrong.");
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-5">
      {meta.providers.length > 0 && (
        <>
          <div className="flex flex-col gap-2">
            {meta.providers.map((p) => (
              <a
                key={p.id}
                href={`/api/v1/auth/${p.id}/start?${qs({ invite, next })}`}
                className="flex h-9 items-center justify-center rounded-md border border-line-strong bg-raised text-[13px] font-medium hover:bg-hover"
              >
                Continue with {p.name}
              </a>
            ))}
          </div>
          <div className="flex items-center gap-3 text-faint">
            <span className="h-px flex-1 bg-line" />
            <span className="label-mono">or</span>
            <span className="h-px flex-1 bg-line" />
          </div>
        </>
      )}
      <form onSubmit={submit} className="flex flex-col gap-4">
        <Field label="Username">
          {(id) => <Input id={id} autoFocus required autoComplete="username" autoCapitalize="none" spellCheck={false} value={username} onChange={(e) => setUsername(e.target.value)} />}
        </Field>
        <Field label="Password" hint={mode === "register" ? "At least 8 characters." : undefined}>
          {(id) => (
            <Input id={id} type="password" required minLength={mode === "register" ? 8 : undefined} autoComplete={mode === "register" ? "new-password" : "current-password"} value={password} onChange={(e) => setPassword(e.target.value)} />
          )}
        </Field>
        {error && <Notice tone="danger">{error}</Notice>}
        <Button type="submit" variant="primary" loading={busy} className="h-9">
          {mode === "register" ? "Create account" : "Sign in"}
        </Button>
      </form>
    </div>
  );
}

export function Login() {
  const meta = useMeta();
  const me = useMe();
  const [params] = useSearchParams();
  const next = safeNext(params.get("next"));
  const [registering, setRegistering] = useState(false);

  if (meta.isPending || me.isPending) return <div className="flex h-dvh items-center justify-center"><Spinner /></div>;
  if (me.data) return <Navigate to={next} replace />;
  if (!meta.data) return <AuthFrame title="Panel unreachable" subtitle="The avSYS server did not answer. Reload to try again."><span /></AuthFrame>;

  if (meta.data.setupRequired) {
    return (
      <AuthFrame title="Set up avSYS" subtitle="Create the first account. It becomes the administrator of this instance.">
        <AuthForm meta={meta.data} mode="register" next="/" />
      </AuthFrame>
    );
  }
  const mode = registering && meta.data.allowSignups ? "register" : "signin";
  return (
    <AuthFrame title={mode === "register" ? "Create an account" : "Sign in"}>
      <div className="flex flex-col gap-5">
        {params.get("error") && <Notice tone="danger">{params.get("error")}</Notice>}
        <AuthForm meta={meta.data} mode={mode} next={next} />
        {meta.data.allowSignups ? (
          <button className="text-left text-[13px] text-muted hover:text-fg" onClick={() => setRegistering(!registering)}>
            {registering ? "Have an account? Sign in" : "New here? Create an account"}
          </button>
        ) : (
          <p className="text-xs leading-relaxed text-faint">Accounts here are created from an invite link. Ask an organization admin for one.</p>
        )}
      </div>
    </AuthFrame>
  );
}
