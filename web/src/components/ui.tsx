import * as Dialog from "@radix-ui/react-dialog";
import * as Dropdown from "@radix-ui/react-dropdown-menu";
import { Check, Copy, LoaderCircle, X } from "lucide-react";
import {
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from "react";
import type { Phase } from "../lib/api";
import { dismiss, toast, useToasts } from "../lib/toast";

export const cx = (...parts: (string | false | null | undefined)[]) => parts.filter(Boolean).join(" ");

type ButtonVariant = "primary" | "default" | "ghost" | "danger";

const buttonVariants: Record<ButtonVariant, string> = {
  primary: "bg-accent text-accent-ink hover:bg-accent-strong",
  default: "border border-line-strong bg-raised text-fg hover:bg-hover",
  ghost: "text-muted hover:bg-raised hover:text-fg",
  danger: "border border-danger/30 bg-danger-wash text-danger hover:bg-danger/20",
};

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: "sm" | "md";
  loading?: boolean;
  icon?: ReactNode;
}

export function Button({ variant = "default", size = "md", loading, icon, children, className, disabled, ...rest }: ButtonProps) {
  return (
    <button
      type="button"
      {...rest}
      disabled={disabled || loading}
      className={cx(
        "inline-flex shrink-0 items-center justify-center gap-1.5 rounded-md font-medium whitespace-nowrap transition-colors select-none disabled:opacity-50",
        size === "sm" ? "h-7 px-2 text-xs" : "h-8 px-3 text-[13px]",
        !children && (size === "sm" ? "w-7 px-0" : "w-8 px-0"),
        buttonVariants[variant],
        className,
      )}
    >
      {loading ? <LoaderCircle className="size-3.5 animate-spin" /> : icon}
      {children}
    </button>
  );
}

const controlClass =
  "rounded-md border border-line-strong bg-bg px-2.5 text-[13px] text-fg placeholder:text-faint transition-colors hover:border-faint focus:border-accent focus:outline-none disabled:opacity-50";

/**
 * A control fills its container unless it is given a width of its own. The
 * two cannot simply both be named: which of two width classes wins is
 * decided by the stylesheet's order, not by which comes last here.
 */
const widthOf = (className?: string) => (/(^|\s)w-/.test(className ?? "") ? "" : "w-full");

export function Input({ className, ...rest }: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...rest} className={cx(controlClass, widthOf(className), "h-8", className)} />;
}

export function Textarea({ className, ...rest }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...rest} className={cx(controlClass, widthOf(className), "py-1.5 leading-relaxed", className)} />;
}

export function Select({ className, children, ...rest }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select {...rest} className={cx(controlClass, widthOf(className), "h-8 appearance-none bg-[length:16px] bg-[right_6px_center] bg-no-repeat pr-7", className)} style={{ backgroundImage: chevron }}>
      {children}
    </select>
  );
}

const chevron = `url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16' fill='none' stroke='%238d989b' stroke-width='1.5'%3E%3Cpath d='m4 6 4 4 4-4'/%3E%3C/svg%3E")`;

interface FieldProps {
  label: string;
  hint?: ReactNode;
  children: (id: string) => ReactNode;
  className?: string;
}

/** A labelled form control. The child receives the id to put on its input. */
export function Field({ label, hint, children, className }: FieldProps) {
  const id = useId();
  return (
    <div className={cx("flex flex-col gap-1.5", className)}>
      <label htmlFor={id} className="text-[13px] font-medium text-fg">
        {label}
      </label>
      {children(id)}
      {hint && <p className="text-xs leading-relaxed text-muted empty:hidden">{hint}</p>}
    </div>
  );
}

interface ToggleProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label: string;
  hint?: ReactNode;
  disabled?: boolean;
}

export function Toggle({ checked, onChange, label, hint, disabled }: ToggleProps) {
  const id = useId();
  return (
    <div className="flex items-start gap-3">
      <button
        id={id}
        type="button"
        role="switch"
        aria-checked={checked}
        disabled={disabled}
        onClick={() => onChange(!checked)}
        className={cx(
          "relative mt-0.5 h-[18px] w-8 shrink-0 rounded-full border transition-colors disabled:opacity-50",
          checked ? "border-accent bg-accent" : "border-line-strong bg-raised",
        )}
      >
        <span className={cx("absolute top-[2px] size-3 rounded-full transition-all", checked ? "left-[15px] bg-accent-ink" : "left-[2px] bg-muted")} />
      </button>
      <label htmlFor={id} className="flex flex-col gap-0.5">
        <span className="text-[13px] font-medium text-fg">{label}</span>
        {hint && <span className="text-xs leading-relaxed text-muted">{hint}</span>}
      </label>
    </div>
  );
}

const phaseStyle: Record<Phase, { label: string; dot: string; text: string; pulse?: boolean }> = {
  Running: { label: "Online", dot: "bg-accent shadow-[0_0_8px] shadow-accent/60", text: "text-accent" },
  Starting: { label: "Starting", dot: "bg-warn", text: "text-warn", pulse: true },
  Stopping: { label: "Stopping", dot: "bg-warn", text: "text-warn", pulse: true },
  Sleeping: { label: "Asleep", dot: "bg-dormant", text: "text-dormant" },
  Stopped: { label: "Offline", dot: "bg-faint", text: "text-muted" },
  Error: { label: "Error", dot: "bg-danger", text: "text-danger" },
};

export function StatusDot({ phase }: { phase?: Phase }) {
  const s = phaseStyle[phase ?? "Stopped"];
  return <span className={cx("inline-block size-2 shrink-0 rounded-full", s.dot, s.pulse && "animate-pulse-soft")} />;
}

export function Status({ phase }: { phase?: Phase }) {
  const s = phaseStyle[phase ?? "Stopped"];
  return (
    <span className={cx("inline-flex items-center gap-2 text-[13px] font-medium", s.text)}>
      <StatusDot phase={phase} />
      {s.label}
    </span>
  );
}

export function Chip({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <span className={cx("inline-flex h-5 items-center rounded-sm border border-line-strong bg-raised px-1.5 font-mono text-[11px] text-muted", className)}>
      {children}
    </span>
  );
}

export function Spinner({ className }: { className?: string }) {
  return <LoaderCircle className={cx("size-4 animate-spin text-faint", className)} />;
}

/** A centred spinner for a page or panel that is still loading. */
export const Loading = () => (
  <div className="flex h-40 items-center justify-center">
    <Spinner />
  </div>
);

/**
 * Puts text on the clipboard. navigator.clipboard only exists on https and
 * localhost, and a panel on a home network is often plain http, so there the
 * text goes through a hidden field next to `near` (inside any open dialog,
 * which would otherwise take the focus back).
 */
async function copyToClipboard(value: string, near: HTMLElement) {
  if (window.isSecureContext && navigator.clipboard) return navigator.clipboard.writeText(value);
  const field = document.createElement("textarea");
  field.value = value;
  field.readOnly = true;
  field.style.cssText = "position:fixed;top:0;left:0;opacity:0;font-size:16px";
  (near.parentElement ?? document.body).appendChild(field);
  field.select();
  field.setSelectionRange(0, value.length);
  const ok = document.execCommand("copy");
  field.remove();
  near.focus();
  if (!ok) throw new Error("copy was refused");
}

/** Text that copies itself when clicked: addresses, commands, tokens. */
export function CopyText({ value, display, className, wrap }: { value: string; display?: ReactNode; className?: string; wrap?: boolean }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      title="Copy"
      onClick={(e) => {
        e.preventDefault();
        e.stopPropagation();
        copyToClipboard(value, e.currentTarget).then(
          () => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1200);
          },
          () => toast.error("Could not copy. Select the text and copy it by hand."),
        );
      }}
      className={cx("group inline-flex max-w-full items-center gap-1.5 rounded-sm text-left font-mono text-[13px] text-fg hover:text-accent", className)}
    >
      <span className={wrap ? "break-all" : "truncate"}>{display ?? value}</span>
      {copied ? <Check className="size-3.5 shrink-0 text-accent" /> : <Copy className="size-3.5 shrink-0 text-faint opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100" />}
    </button>
  );
}

interface ModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: ReactNode;
  children: ReactNode;
  size?: "md" | "lg";
}

export function Modal({ open, onOpenChange, title, description, children, size = "md" }: ModalProps) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/60 backdrop-blur-[2px]" />
        <Dialog.Content
          // Start in the first field rather than on the close button.
          onOpenAutoFocus={(e) => {
            const field = (e.currentTarget as HTMLElement).querySelector<HTMLElement>("input:not([type=hidden]), select, textarea");
            if (field) {
              e.preventDefault();
              field.focus();
            }
          }}
          className={cx(
            "fixed top-[10vh] left-1/2 z-50 flex max-h-[80vh] w-[calc(100vw-2rem)] -translate-x-1/2 animate-rise flex-col rounded-lg border border-line-strong bg-surface shadow-2xl shadow-black/60 outline-none",
            size === "lg" ? "max-w-2xl" : "max-w-md",
          )}
        >
          <div className="flex items-start justify-between gap-4 px-5 pt-4 pb-3">
            <div>
              <Dialog.Title className="text-[15px] font-semibold">{title}</Dialog.Title>
              {description && <Dialog.Description className="mt-1 text-[13px] leading-relaxed text-muted">{description}</Dialog.Description>}
            </div>
            <Dialog.Close asChild>
              <Button variant="ghost" size="sm" icon={<X className="size-4" />} aria-label="Close" />
            </Dialog.Close>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto px-5 pb-5">{children}</div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

interface ConfirmProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: ReactNode;
  action: string;
  /** If set, the action stays disabled until this exact text is typed. */
  typeToConfirm?: string;
  onConfirm: () => void;
  loading?: boolean;
  /** Anything else to decide before confirming, shown above the buttons. */
  children?: ReactNode;
}

export function Confirm({ open, onOpenChange, title, description, action, typeToConfirm, onConfirm, loading, children }: ConfirmProps) {
  const [typed, setTyped] = useState("");
  const ready = !typeToConfirm || typed === typeToConfirm;
  return (
    <Modal
      open={open}
      onOpenChange={(o) => {
        setTyped("");
        onOpenChange(o);
      }}
      title={title}
      description={description}
    >
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (ready) onConfirm();
        }}
      >
        {typeToConfirm && (
          <Field label={`Type ${typeToConfirm} to confirm`}>
            {(id) => <Input id={id} autoFocus value={typed} onChange={(e) => setTyped(e.target.value)} className="font-mono" autoComplete="off" />}
          </Field>
        )}
        {children}
        <div className="flex justify-end gap-2">
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button type="submit" variant="danger" disabled={!ready} loading={loading}>
            {action}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

export const Menu = Dropdown.Root;
export const MenuTrigger = Dropdown.Trigger;

export function MenuContent({ children, align = "end" }: { children: ReactNode; align?: "start" | "end" }) {
  return (
    <Dropdown.Portal>
      <Dropdown.Content
        align={align}
        sideOffset={6}
        className="z-50 min-w-44 animate-rise rounded-md border border-line-strong bg-surface p-1 shadow-xl shadow-black/50"
      >
        {children}
      </Dropdown.Content>
    </Dropdown.Portal>
  );
}

interface MenuItemProps {
  children: ReactNode;
  icon?: ReactNode;
  onSelect?: () => void;
  danger?: boolean;
  disabled?: boolean;
}

export function MenuItem({ children, icon, onSelect, danger, disabled }: MenuItemProps) {
  return (
    <Dropdown.Item
      disabled={disabled}
      onSelect={onSelect}
      className={cx(
        "flex h-8 cursor-pointer items-center gap-2 rounded-sm px-2 text-[13px] outline-none select-none data-[disabled]:cursor-not-allowed data-[disabled]:opacity-40 data-[highlighted]:bg-hover",
        danger ? "text-danger" : "text-fg",
      )}
    >
      <span className="flex size-4 items-center justify-center text-muted [&>svg]:size-4">{icon}</span>
      {children}
    </Dropdown.Item>
  );
}

export const MenuSeparator = () => <Dropdown.Separator className="my-1 h-px bg-line" />;
export const MenuLabel = ({ children }: { children: ReactNode }) => <Dropdown.Label className="label-mono px-2 py-1.5">{children}</Dropdown.Label>;

export function Toaster() {
  const toasts = useToasts();
  return (
    // Above a bar along the bottom of the page, where there is one, and not over its buttons.
    <div className="pointer-events-none fixed right-4 bottom-4 z-[60] flex w-80 flex-col gap-2 [body:has([data-bottom-bar])_&]:bottom-18" role="status" aria-live="polite">
      {toasts.map((t) => (
        <div
          key={t.id}
          className={cx(
            "pointer-events-auto flex animate-rise items-start gap-3 rounded-md border bg-surface px-3 py-2.5 text-[13px] leading-relaxed shadow-xl shadow-black/50",
            t.kind === "error" ? "border-danger/40 text-fg" : "border-line-strong text-fg",
          )}
        >
          {/* The dot and the button each sit in a box one line of text high, so they are level with the first line however many there are. */}
          <span className="flex h-lh shrink-0 items-center">
            <span className={cx("size-1.5 rounded-full", t.kind === "error" ? "bg-danger" : "bg-accent")} />
          </span>
          <p className="min-w-0 flex-1 wrap-break-word">{t.message}</p>
          <button onClick={() => dismiss(t.id)} className="flex h-lh shrink-0 items-center text-faint hover:text-fg" aria-label="Dismiss">
            <X className="size-3.5" />
          </button>
        </div>
      ))}
    </div>
  );
}

/** A page's title row. */
interface RowCheckboxProps {
  checked: boolean;
  /** Some but not all of what it stands for is selected. */
  partial?: boolean;
  /** Called with whether Shift was held, which selects a range. */
  onChange: (range: boolean) => void;
  label: string;
  /** Faint until its row is pointed at or something is selected. */
  quiet?: boolean;
  className?: string;
}

/** The tick box at the start of a row that can be selected with others. */
export function RowCheckbox({ checked, partial, onChange, label, quiet, className }: RowCheckboxProps) {
  return (
    <input
      type="checkbox"
      aria-label={label}
      checked={checked}
      ref={(el) => {
        if (el) el.indeterminate = !!partial && !checked;
      }}
      // The click says whether Shift was held; the change event does not.
      onClick={(e) => {
        e.stopPropagation();
        onChange(e.shiftKey);
      }}
      onChange={() => {}}
      className={cx(
        "size-3.5 shrink-0 cursor-pointer accent-(--color-accent) transition-opacity",
        // Always there, so the column is never an unexplained gap, but out of the way of a list that is mostly read.
        quiet && !checked && "opacity-40 group-hover:opacity-100 hover:opacity-100 focus-visible:opacity-100",
        className,
      )}
    />
  );
}

/**
 * The bar that appears along the bottom while rows are selected, with what
 * can be done to them.
 */
export function SelectionBar({ count, noun, onClear, children }: { count: number; noun: string; onClear: () => void; children: ReactNode }) {
  // The bar is fixed to the bottom of the window and wraps to as many rows
  // as its buttons need. As much room as it takes is left where it is
  // placed, after the page's content, so that it never covers the end of it.
  const bar = useRef<HTMLDivElement>(null);
  const [height, setHeight] = useState(0);
  useLayoutEffect(() => {
    const el = bar.current;
    if (!el) return;
    setHeight(el.offsetHeight);
    const watch = new ResizeObserver(() => setHeight(el.offsetHeight));
    watch.observe(el);
    return () => watch.disconnect();
  }, [count]);
  if (count === 0) return null;
  return (
    <>
      <div aria-hidden style={{ height }} />
      <div ref={bar} role="region" aria-label="Selection" data-bottom-bar className="fixed inset-x-0 bottom-0 z-30 border-t border-line-strong bg-surface/95 backdrop-blur md:left-56">
        <div className="mx-auto flex max-w-5xl flex-wrap items-center justify-between gap-x-4 gap-y-2 px-5 py-3 md:px-10">
          <p className="flex items-center gap-3 text-[13px]">
            <span className="font-medium" aria-live="polite">
              {count} {noun}
              {count === 1 ? "" : "s"} selected
            </span>
            <button type="button" onClick={onClear} className="text-muted hover:text-fg">
              Clear
            </button>
          </p>
          <div className="flex flex-wrap gap-2">{children}</div>
        </div>
      </div>
    </>
  );
}

export function PageHeader({ title, description, actions }: { title: ReactNode; description?: ReactNode; actions?: ReactNode }) {
  return (
    <header className="flex flex-wrap items-end justify-between gap-4 pb-6">
      <div className="min-w-0">
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        {description && <p className="mt-1 text-[13px] text-muted">{description}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </header>
  );
}

/** A settings block: what it is on the left, the controls on the right. */
export function Section({ title, description, children }: { title: string; description?: ReactNode; children: ReactNode }) {
  return (
    <section className="grid gap-x-10 gap-y-4 border-t border-line py-7 first:border-t-0 first:pt-0 md:grid-cols-[220px_minmax(0,1fr)]">
      <div>
        <h2 className="text-[13px] font-semibold">{title}</h2>
        {description && <p className="mt-1 text-xs leading-relaxed text-muted">{description}</p>}
      </div>
      <div className="flex max-w-xl flex-col gap-4">{children}</div>
    </section>
  );
}

export function Empty({ title, children, action }: { title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed border-line-strong px-6 py-14 text-center">
      <p className="text-[15px] font-medium">{title}</p>
      {children && <p className="max-w-sm text-[13px] leading-relaxed text-muted">{children}</p>}
      {action && <div className="mt-1">{action}</div>}
    </div>
  );
}

export function Notice({ tone = "warn", children, action }: { tone?: "warn" | "danger" | "info"; children: ReactNode; action?: ReactNode }) {
  const tones = {
    warn: "border-warn/30 bg-warn-wash text-warn",
    danger: "border-danger/30 bg-danger-wash text-danger",
    info: "border-line-strong bg-raised text-muted",
  };
  return (
    <div className={cx("flex flex-wrap items-center justify-between gap-3 rounded-md border px-3 py-2 text-[13px]", tones[tone])}>
      <p className="leading-relaxed">{children}</p>
      {action}
    </div>
  );
}
