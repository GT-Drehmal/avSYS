import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useOrg } from "../components/Shell";
import { Button, Confirm, Field, Input, PageHeader, Section } from "../components/ui";
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
          <Field label="Name">{(id) => <Input id={id} required value={name} onChange={(e) => setName(e.target.value)} />}</Field>
          <Field label="URL name" hint="It cannot be changed.">
            {(id) => <Input id={id} disabled value={membership.slug} className="font-mono" />}
          </Field>
          <div>
            <Button type="submit" variant="primary" disabled={name === membership.name} loading={rename.isPending}>
              Save
            </Button>
          </div>
        </form>
      </Section>
      <Section title="Delete organization" description="Its servers must be deleted first. Members keep their accounts. Its snapshots are deleted with it.">
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
        description={`The organization and its memberships are removed${
          snapshots === undefined ? ", along with any snapshots it has" : snapshots > 0 ? `, along with its ${snapshots} snapshot${snapshots === 1 ? "" : "s"}` : ""
        }. This cannot be undone.`}
        action="Delete organization"
        typeToConfirm={membership.slug}
        loading={remove.isPending}
        onConfirm={() => remove.mutate(undefined, { onSuccess: () => navigate("/") })}
      />
    </>
  );
}
