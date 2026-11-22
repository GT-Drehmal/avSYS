import { useState } from "react";
import { api, ApiError } from "../lib/api";
import { Button, Field, Input, Notice } from "./ui";

/**
 * The second step of signing in, with a password or through a provider, for an account with
 * two-factor: a code from the app, or a recovery code. onRestart goes back
 * to the password, with why, when the sign-in has to start again.
 */
export function SignInCode({ next, onRestart }: { next: string; onRestart: (why?: string) => void }) {
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.post("/auth/login/code", { code });
      // A full navigation, so every cached answer from the signed-out state is gone.
      window.location.assign(next);
    } catch (err) {
      setBusy(false);
      if (err instanceof ApiError && err.code === "sign_in_again") return onRestart(err.message);
      setError(err instanceof ApiError ? err.message : "Something went wrong.");
    }
  };
  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      <Field label="Code" hint="From your authenticator app, or a recovery code." missing="Enter the code.">
        {(id) => <Input id={id} autoFocus required autoComplete="one-time-code" spellCheck={false} value={code} onChange={(e) => setCode(e.target.value)} className="font-mono" />}
      </Field>
      {error && <Notice tone="danger">{error}</Notice>}
      <Button type="submit" variant="primary" loading={busy} className="h-9">
        Sign in
      </Button>
      <button type="button" className="text-left text-[13px] text-muted hover:text-fg" onClick={() => onRestart()}>
        Back
      </button>
    </form>
  );
}
