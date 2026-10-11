import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { Button, Notice, Spinner } from "../components/ui";
import { api, roleSummary, type Role } from "../lib/api";
import { keys, useAction, useMe, useMeta } from "../lib/queries";
import { AuthForm, AuthFrame } from "./Login";

interface InvitePreview {
  org: { name: string; slug: string };
  role: Role;
  usable: boolean;
}

export function Invite() {
  const { token = "" } = useParams();
  const me = useMe();
  const meta = useMeta();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [mode, setMode] = useState<"register" | "signin">("register");
  const invite = useQuery({ queryKey: ["invite", token], queryFn: () => api.get<InvitePreview>(`/invites/${token}`), retry: false });
  const accept = useAction(() => api.post<{ slug: string }>(`/invites/${token}/accept`));

  if (invite.isPending || me.isPending || meta.isPending) return <div className="flex h-dvh items-center justify-center"><Spinner /></div>;
  if (!invite.data || !invite.data.usable || !meta.data) {
    return (
      <AuthFrame title="This invite is no longer valid" subtitle="It may have expired, been used up, or been revoked. Ask whoever sent it for a new one.">
        <Link to="/" className="text-[13px] text-accent hover:underline">
          Go to avSYS
        </Link>
      </AuthFrame>
    );
  }
  const { org, role } = invite.data;
  const subtitle = (
    <>
      You have been invited as <span className="font-medium text-fg">{role}</span>: {roleSummary[role].toLowerCase()}.
    </>
  );

  if (me.data) {
    return (
      <AuthFrame title={`Join ${org.name}`} subtitle={subtitle}>
        <div className="flex flex-col gap-4">
          <Notice tone="info">
            Signed in as <span className="font-mono text-fg">{me.data.user.username}</span>
          </Notice>
          <Button
            variant="primary"
            className="h-9"
            loading={accept.isPending}
            onClick={() =>
              accept.mutate(undefined, {
                onSuccess: async (joined) => {
                  await qc.invalidateQueries({ queryKey: keys.me });
                  navigate(`/o/${joined.slug}`);
                },
              })
            }
          >
            Join {org.name}
          </Button>
        </div>
      </AuthFrame>
    );
  }
  return (
    <AuthFrame title={`Join ${org.name}`} subtitle={subtitle}>
      <div className="flex flex-col gap-5">
        {/* Signing in lands back here, where the invite is then accepted. */}
        <AuthForm meta={meta.data} mode={mode} invite={mode === "register" ? token : undefined} next={mode === "register" ? `/o/${org.slug}` : `/invite/${token}`} />
        <button className="text-left text-[13px] text-muted hover:text-fg" onClick={() => setMode(mode === "register" ? "signin" : "register")}>
          {mode === "register" ? "Already have an account? Sign in" : "New here? Create an account"}
        </button>
      </div>
    </AuthFrame>
  );
}
