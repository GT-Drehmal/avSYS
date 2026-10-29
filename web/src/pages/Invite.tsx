import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { Button, Notice, Spinner } from "../components/ui";
import { api, ApiError, roleSummary, type Role } from "../lib/api";
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
  const invite = useQuery({ queryKey: ["invite", token], queryFn: () => api.get<InvitePreview>(`/invites/${token}`) });
  const accept = useAction(() => api.post<{ slug: string }>(`/invites/${token}/accept`));

  if (invite.isPending || me.isPending || meta.isPending) return <div className="flex h-dvh items-center justify-center"><Spinner /></div>;
  // Only the panel saying so makes an invite invalid. A request that never
  // got an answer is a connection problem, and says nothing about the invite.
  const gone = invite.data ? !invite.data.usable : invite.error instanceof ApiError && invite.error.status === 404;
  if (gone) {
    return (
      <AuthFrame title="This invite is no longer valid" subtitle="The invite may have expired or been revoked. Ask for a new one.">
        <Link to="/" className="text-[13px] text-accent hover:underline">
          Go to avSYS
        </Link>
      </AuthFrame>
    );
  }
  if (!invite.data || !meta.data) {
    return (
      <AuthFrame title="Could not load this invite" subtitle="The panel did not answer. Check your connection and try again.">
        <Button
          className="h-9"
          loading={invite.isFetching || meta.isFetching}
          onClick={() => {
            if (!invite.data) invite.refetch();
            if (!meta.data) meta.refetch();
          }}
        >
          Try again
        </Button>
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
