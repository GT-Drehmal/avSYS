import { cx, Field, Textarea } from "./ui";

/** The most notes may be, as the panel takes them. */
export const maxNotes = 500;

/**
 * Notes on a server, template or world: a few lines for those who look
 * after them, kept by the panel and never shown in game.
 */
export function NotesField({ value, onChange, rows = 2 }: { value: string; onChange: (notes: string) => void; rows?: number }) {
  return (
    <Field label="Notes" hint="Only seen in the panel.">
      {(id) => <Textarea id={id} rows={rows} maxLength={maxNotes} value={value} onChange={(e) => onChange(e.target.value)} />}
    </Field>
  );
}

/** Notes as they are shown with what they are on, their lines kept. */
export function NotesText({ notes, className }: { notes?: string; className?: string }) {
  if (!notes) return null;
  return <p className={cx("text-xs whitespace-pre-line text-muted", className)}>{notes}</p>;
}
