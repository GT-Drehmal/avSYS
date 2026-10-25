import { useQuery } from "@tanstack/react-query";
import { ChevronLeft } from "lucide-react";
import { Link, useParams } from "react-router";
import { Loading, Notice, PageHeader } from "../components/ui";
import { api, ApiError, type Template } from "../lib/api";
import { keys } from "../lib/queries";
import { ServerForm } from "./ServerForm";

const none = new Set<string>();

/** Making a template of settings alone, or changing one: the server form, for a template. */
export function TemplateForm() {
  const { org = "", template: id = "" } = useParams();
  const template = useQuery({
    queryKey: keys.template(org, id),
    queryFn: () => api.get<Template>(`/orgs/${org}/templates/${id}`),
    enabled: !!id,
    retry: false,
    // Filled in once, from what it has now.
    refetchOnMount: "always",
  });
  if (id && template.isFetching) return <Loading />;
  if (id && !template.data?.settingsOnly) {
    return (
      <>
        <Link to={`/o/${org}/templates`} className="mb-3 inline-flex items-center gap-1 text-xs text-muted hover:text-fg">
          <ChevronLeft className="size-3.5" />
          Templates
        </Link>
        <PageHeader title="Edit template" />
        <Notice tone="danger">
          {template.error instanceof ApiError ? template.error.message : template.data ? "This template was saved from a server. Update it from a server instead." : "Could not load the template."}
        </Notice>
      </>
    );
  }
  return <ServerForm key={id || "new"} org={org} templating={{ template: template.data ?? null }} taken={none} history={none} />;
}
