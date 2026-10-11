import { useQuery } from "@tanstack/react-query";
import { Link2, Trash2 } from "lucide-react";
import { useState } from "react";
import { useParams } from "react-router";
import { useOrg } from "../components/Shell";
import { Avatar, Button, Chip, Confirm, CopyText, Field, Input, Modal, PageHeader, Select, Loading } from "../components/ui";
import { api, atLeast, roles, roleSummary, type Invite, type Member, type Role } from "../lib/api";
import { ago } from "../lib/format";
import { keys, useAction, useMe } from "../lib/queries";

const inviteURL = (token: string) => `${location.origin}/invite/${token}`;

function InviteDialog({ org, myRole, onClose }: { org: string; myRole: Role; onClose: () => void }) {
  const [role, setRole] = useState<Role>("operator");
  const [expires, setExpires] = useState("168");
  const [maxUses, setMaxUses] = useState("1");
  const [created, setCreated] = useState<Invite | null>(null);
  const create = useAction(
    () => api.post<Invite>(`/orgs/${org}/invites`, { role, expiresInHours: Number(expires), maxUses: Number(maxUses) || 0 }),
    [keys.invites(org)],
  );
  if (created) {
    return (
      <Modal open onOpenChange={onClose} title="Invite link" description="Send this link to the person you are inviting. Anyone who has it can use it.">
        <div className="flex flex-col gap-4">
          <div className="rounded-md border border-line bg-bg px-3 py-2">
            <CopyText value={inviteURL(created.token)} className="text-xs" wrap />
          </div>
          <div className="flex justify-end">
            <Button variant="primary" onClick={onClose}>
              Done
            </Button>
          </div>
        </div>
      </Modal>
    );
  }
  return (
    <Modal open onOpenChange={onClose} title="Invite people" description="Creates a link that adds whoever opens it to this organization.">
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate(undefined, { onSuccess: setCreated });
        }}
      >
        <Field label="Role" hint={roleSummary[role] + "."}>
          {(id) => (
            <Select id={id} value={role} onChange={(e) => setRole(e.target.value as Role)}>
              {roles.filter((r) => atLeast(myRole, r)).map((r) => (
                <option key={r} value={r}>
                  {r[0].toUpperCase() + r.slice(1)}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <div className="grid grid-cols-2 gap-4">
          <Field label="Expires">
            {(id) => (
              <Select id={id} value={expires} onChange={(e) => setExpires(e.target.value)}>
                <option value="24">In a day</option>
                <option value="168">In a week</option>
                <option value="720">In 30 days</option>
                <option value="0">Never</option>
              </Select>
            )}
          </Field>
          <Field label="Uses" hint="0 for unlimited.">
            {(id) => <Input id={id} type="number" min={0} value={maxUses} onChange={(e) => setMaxUses(e.target.value)} />}
          </Field>
        </div>
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" loading={create.isPending}>
            Create link
          </Button>
        </div>
      </form>
    </Modal>
  );
}

export function Members() {
  const { org = "" } = useParams();
  const membership = useOrg();
  const me = useMe().data!;
  const canManage = atLeast(membership?.role, "admin");
  const isOwner = membership?.role === "owner";
  const members = useQuery({ queryKey: keys.members(org), queryFn: () => api.get<Member[]>(`/orgs/${org}/members`) });
  const invites = useQuery({ queryKey: keys.invites(org), queryFn: () => api.get<Invite[]>(`/orgs/${org}/invites`), enabled: canManage });
  const [inviting, setInviting] = useState(false);
  const [removing, setRemoving] = useState<Member | null>(null);

  const setRole = useAction(({ userId, role }: { userId: string; role: Role }) => api.patch(`/orgs/${org}/members/${userId}`, { role }), [keys.members(org)]);
  const remove = useAction((userId: string) => api.del(`/orgs/${org}/members/${userId}`), [keys.members(org), keys.me]);
  const revoke = useAction((token: string) => api.del(`/orgs/${org}/invites/${token}`), [keys.invites(org)]);

  if (members.isPending) return <Loading />;
  return (
    <>
      <PageHeader
        title="Members"
        description={membership?.name}
        actions={
          canManage && (
            <Button variant="primary" icon={<Link2 className="size-4" />} onClick={() => setInviting(true)}>
              Invite
            </Button>
          )
        }
      />
      <ul className="overflow-hidden rounded-lg border border-line bg-surface">
        {members.data?.map((m) => {
          // Admins manage everyone below owner; owners manage everyone.
          const editable = canManage && (isOwner || m.role !== "owner");
          return (
            <li key={m.userId} className="flex items-center gap-3 border-b border-line px-4 py-2.5 last:border-b-0">
              <Avatar name={m.username} url={m.avatarUrl} size={28} />
              <div className="min-w-0 flex-1">
                <p className="truncate text-[13px] font-medium">
                  {m.displayName || m.username}
                  {m.userId === me.user.id && <span className="ml-2 text-xs font-normal text-faint">you</span>}
                </p>
                <p className="truncate font-mono text-xs text-faint">{m.username}</p>
              </div>
              <span className="text-xs text-faint max-sm:hidden">joined {ago(m.joinedAt)}</span>
              {editable ? (
                <Select value={m.role} onChange={(e) => setRole.mutate({ userId: m.userId, role: e.target.value as Role })} className="w-28" aria-label={`Role of ${m.username}`}>
                  {roles.filter((r) => isOwner || r !== "owner").map((r) => (
                    <option key={r} value={r}>
                      {r[0].toUpperCase() + r.slice(1)}
                    </option>
                  ))}
                </Select>
              ) : (
                <Chip>{m.role}</Chip>
              )}
              {(editable || m.userId === me.user.id) && (
                <Button size="sm" variant="ghost" icon={<Trash2 className="size-3.5" />} aria-label={m.userId === me.user.id ? "Leave organization" : `Remove ${m.username}`} onClick={() => setRemoving(m)} />
              )}
            </li>
          );
        })}
      </ul>

      {canManage && invites.data && invites.data.length > 0 && (
        <section className="mt-10">
          <h2 className="mb-3 text-[13px] font-semibold">Invite links</h2>
          <ul className="overflow-hidden rounded-lg border border-line bg-surface">
            {invites.data.map((inv) => {
              const expired = inv.expiresAt !== null && new Date(inv.expiresAt) < new Date();
              const used = inv.maxUses > 0 && inv.uses >= inv.maxUses;
              return (
                <li key={inv.token} className="flex items-center gap-4 border-b border-line px-4 py-2.5 last:border-b-0">
                  <div className="min-w-0 flex-1">
                    <CopyText value={inviteURL(inv.token)} display={`…/invite/${inv.token.slice(0, 10)}…`} className="text-xs text-muted" />
                    <p className="mt-0.5 text-xs text-faint">
                      {expired ? "Expired" : used ? "Used up" : inv.expiresAt ? `Expires ${ago(inv.expiresAt)}` : "Never expires"} · used {inv.uses}
                      {inv.maxUses > 0 ? ` of ${inv.maxUses}` : " times"} · by {inv.createdBy}
                    </p>
                  </div>
                  <Chip>{inv.role}</Chip>
                  <Button size="sm" variant="ghost" loading={revoke.isPending && revoke.variables === inv.token} onClick={() => revoke.mutate(inv.token)}>
                    Revoke
                  </Button>
                </li>
              );
            })}
          </ul>
        </section>
      )}

      {inviting && membership && <InviteDialog org={org} myRole={membership.role} onClose={() => setInviting(false)} />}
      <Confirm
        open={!!removing}
        onOpenChange={() => setRemoving(null)}
        title={removing?.userId === me.user.id ? `Leave ${membership?.name}?` : `Remove ${removing?.username}?`}
        description="They lose access to this organization's servers, including SSH. Their account is not deleted."
        action={removing?.userId === me.user.id ? "Leave" : "Remove"}
        loading={remove.isPending}
        onConfirm={() => removing && remove.mutate(removing.userId, { onSuccess: () => setRemoving(null) })}
      />
    </>
  );
}
