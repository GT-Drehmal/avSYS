import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useParams } from "react-router";
import { Button, Field, Input, Notice, Spinner } from "../components/ui";
import { api, ApiError } from "../lib/api";
import { AuthFrame } from "./Login";

/** A link from an administrator, to set a new password with. */
export function Reset() {
  const { token = "" } = useParams();
  const link = useQuery({ queryKey: ["reset-link", token], queryFn: () => api.get<{ username: string; displayName: string }>(`/reset-links/${token}`), retry: false });
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);

  if (link.isPending) return <div className="flex h-dvh items-center justify-center"><Spinner /></div>;
  if (done) {
    return (
      <AuthFrame title="Password set" subtitle="You are signed out everywhere. Sign in with your new password.">
        <a href="/login" className="text-[13px] text-accent hover:underline">
          Sign in
        </a>
      </AuthFrame>
    );
  }
  if (!link.data) {
    const gone = link.error instanceof ApiError && link.error.status === 404;
    if (link.error instanceof ApiError && link.error.code === "passwords_off") {
      return (
        <AuthFrame title="Passwords are turned off here" subtitle="Sign in another way.">
          <Link to="/login" className="text-[13px] text-accent hover:underline">
            Go to avSYS
          </Link>
        </AuthFrame>
      );
    }
    return (
      <AuthFrame title={gone ? "This link is no longer valid" : "Could not load this link"} subtitle={gone ? "Ask for a new one." : "The panel did not answer. Reload to try again."}>
        <Link to="/login" className="text-[13px] text-accent hover:underline">
          Go to avSYS
        </Link>
      </AuthFrame>
    );
  }
  const name = link.data.displayName || link.data.username;
  return (
    <AuthFrame
      title="Set a new password"
      subtitle={
        <>
          For <span className="font-medium text-fg">{name}</span>.
        </>
      }
    >
      <form
        className="flex flex-col gap-4"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError(null);
          try {
            await api.post(`/reset-links/${token}`, { password });
            setDone(true);
          } catch (err) {
            setError(err instanceof ApiError ? err.message : "Something went wrong.");
          }
          setBusy(false);
        }}
      >
        <input type="text" name="username" autoComplete="username" value={link.data.username} readOnly hidden />
        <Field label="New password" hint="At least 8 characters.">
          {(id) => <Input id={id} type="password" required autoFocus minLength={8} autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />}
        </Field>
        {error && <Notice tone="danger">{error}</Notice>}
        <Button type="submit" variant="primary" loading={busy} className="h-9">
          Set password
        </Button>
      </form>
    </AuthFrame>
  );
}
