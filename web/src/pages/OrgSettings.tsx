import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { PicturePicker } from "../components/Picture";
import { useOrg } from "../components/Shell";
import { Button, Confirm, Field, Input, Notice, PageHeader, Section, Textarea } from "../components/ui";
import { api } from "../lib/api";
import { keys, useAction, useOrgSnapshots } from "../lib/queries";
import { toast } from "../lib/toast";

export function OrgSettings() {
  const { org = "" } = useParams();
  const membership = useOrg();
  const navigate = useNavigate();
  const [name, setName] = useState(membership?.name ?? "");
  const [deleting, setDeleting] = useState(false);
  const rename = useAction(() => api.patch(`/orgs/${org}`, { name }), [keys.me]);
  // Snapshots outlive servers, so an organization with no servers left can
  // still hold a long history. The dialog says what goes with it, and typing
  // the name there is the confirmation the API asks for. That holds when the
  // snapshots could not be counted, too: the API then assumes there are some.
  const listed = useOrgSnapshots(org);
  const snapshots = listed.data?.available ? listed.data.snapshots.length : undefined;
  const remove = useAction(() => api.del(`/orgs/${org}?snapshots=delete`), [keys.me]);
  const qc = useQueryClient();
  if (!membership) return null;

  return (
    <>
      <PageHeader title="Organization settings" description={membership.name} />
      <Section title="Name">
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            rename.mutate(undefined, { onSuccess: () => toast.info("Organization renamed") });
          }}
        >
          <PicturePicker name={membership.name} path={`/orgs/${org}/icon`} has={membership.hasIcon} onChange={() => qc.invalidateQueries({ queryKey: keys.me })} />
          <Field label="Name">{(id) => <Input id={id} required value={name} onChange={(e) => setName(e.target.value)} />}</Field>
          <Field label="Short name" hint="Used in links and SSH logins. It cannot be changed.">
            {(id) => <Input id={id} disabled value={membership.slug} className="font-mono" />}
          </Field>
          <div>
            <Button type="submit" variant="primary" disabled={name === membership.name} loading={rename.isPending}>
              Save
            </Button>
          </div>
        </form>
      </Section>
      <Domains org={org} />
      <Section title="Delete organization" description="Delete its servers first. Its snapshots are deleted too.">
        <div>
          <Button variant="danger" onClick={() => setDeleting(true)}>
            Delete {membership.name}
          </Button>
        </div>
      </Section>
      <Confirm
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete ${membership.name}?`}
        description={`This deletes the organization${
          snapshots === undefined ? " and any snapshots it has" : snapshots > 0 ? ` and its ${snapshots} snapshot${snapshots === 1 ? "" : "s"}` : ""
        }. This cannot be undone.`}
        action="Delete organization"
        typeToConfirm={membership.slug}
        loading={remove.isPending}
        onConfirm={() => remove.mutate(undefined, { onSuccess: () => navigate("/") })}
      />
    </>
  );
}

interface DomainsAnswer {
  domains: string[];
  editable: boolean;
  inUse?: { org: string; server: string; hostname: string }[];
}

/**
 * The domains an organization has: a hostname under one of them is its own
 * servers' alone. Given by whoever runs the panel, who points their DNS at it.
 */
function Domains({ org }: { org: string }) {
  const qc = useQueryClient();
  const key = ["domains", org];
  const domains = useQuery({ queryKey: key, queryFn: () => api.get<DomainsAnswer>(`/orgs/${org}/domains`) });
  const [text, setText] = useState<string | null>(null);
  const [inUse, setInUse] = useState<DomainsAnswer["inUse"]>([]);
  const save = useAction(() => api.put<DomainsAnswer>(`/orgs/${org}/domains`, { domains: (text ?? "").split(/[\s,]+/).filter(Boolean) }));
  const d = domains.data;
  if (!d) return null;
  const shown = text ?? d.domains.join("\n");
  return (
    <Section title="Domains" description="Hostnames under these are for this organization's servers only.">
      {d.editable ? (
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate(undefined, {
              onSuccess: (answer) => {
                qc.setQueryData(key, answer);
                setText(null);
                setInUse(answer.inUse ?? []);
                toast.info("Domains saved");
              },
            });
          }}
        >
          <Field label="One per line" hint="Each covers every name under it.">
            {(id) => <Textarea id={id} rows={3} value={shown} onChange={(e) => setText(e.target.value)} placeholder="play.example.org" className="font-mono" spellCheck={false} />}
          </Field>
          {inUse && inUse.length > 0 && (
            <Notice>
              Already used by other organizations, who keep them:{" "}
              {inUse.map((u) => `${u.hostname} (${u.org}/${u.server})`).join(", ")}.
            </Notice>
          )}
          <div>
            <Button type="submit" variant="primary" disabled={text === null} loading={save.isPending}>
              Save domains
            </Button>
          </div>
        </form>
      ) : (
        <p className="text-[13px] text-muted">
          {d.domains.length > 0 ? <span className="font-mono text-fg">{d.domains.join(", ")}</span> : "None."} Set by the panel's administrator.
        </p>
      )}
    </Section>
  );
}
