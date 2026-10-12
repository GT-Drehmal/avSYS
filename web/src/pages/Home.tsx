import { useState } from "react";
import { Navigate } from "react-router";
import { lastOrg, NewOrgDialog } from "../components/Shell";
import { Button, Empty } from "../components/ui";
import { useMe } from "../lib/queries";

/** The landing route: straight to the last organization used, if there is one. */
export function Home() {
  const me = useMe().data!;
  const [creating, setCreating] = useState(false);
  const org = me.orgs.find((o) => o.slug === lastOrg()) ?? me.orgs[0];
  if (org) return <Navigate to={`/o/${org.slug}`} replace />;
  return (
    <div className="pt-16">
      {me.user.isAdmin ? (
        <Empty
          title="Create your first organization"
          action={
            <Button variant="primary" onClick={() => setCreating(true)}>
              New organization
            </Button>
          }
        >
          Servers and members belong to an organization.
        </Empty>
      ) : (
        <Empty title="You are not in an organization yet">Ask an organization admin for an invite link.</Empty>
      )}
      <NewOrgDialog open={creating} onOpenChange={setCreating} />
    </div>
  );
}
