import * as Dialog from "@radix-ui/react-dialog";
import * as Dropdown from "@radix-ui/react-dropdown-menu";
import { Check, ChevronRight, Copy, ExternalLink, LoaderCircle, Search, X } from "lucide-react";
import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type AnchorHTMLAttributes,
  type ButtonHTMLAttributes,
  type HTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from "react";
import { createPortal } from "react-dom";
import type { Phase } from "../lib/api";
import type { Selection } from "../lib/selection";
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

const buttonClass = (variant: ButtonVariant, size: "sm" | "md", labelled: boolean, className?: string) =>
  cx(
    "inline-flex shrink-0 items-center justify-center gap-1.5 rounded-md font-medium whitespace-nowrap transition-colors select-none disabled:opacity-50",
    size === "sm" ? "h-7 text-xs" : "h-8 text-[13px]",
    labelled ? (size === "sm" ? "px-2" : "px-3") : size === "sm" ? "w-7" : "w-8",
    buttonVariants[variant],
    className,
  );

export function Button({ variant = "default", size = "md", loading, icon, children, className, disabled, ...rest }: ButtonProps) {
  return (
    <button type="button" {...rest} disabled={disabled || loading} className={buttonClass(variant, size, !!children, className)}>
      {loading ? <LoaderCircle className="size-3.5 animate-spin" /> : icon}
      {children}
    </button>
  );
}

/** A link that looks like a Button, such as one that downloads a file. */
export function ButtonLink({ variant = "default", size = "md", icon, children, className, ...rest }: AnchorHTMLAttributes<HTMLAnchorElement> & { variant?: ButtonVariant; size?: "sm" | "md"; icon?: ReactNode }) {
  return (
    <a {...rest} className={buttonClass(variant, size, !!children, className)}>
      {icon}
      {children}
    </a>
  );
}

const controlClass =
  "rounded-md border border-line-strong bg-bg px-2.5 text-[13px] text-fg placeholder:text-faint transition-colors hover:border-faint focus:border-accent focus:outline-none aria-invalid:border-danger disabled:opacity-50";

/**
 * A control fills its container unless it is given a width of its own. The
 * two cannot simply both be named: which of two width classes wins is
 * decided by the stylesheet's order, not by which comes last here.
 */
const widthOf = (className?: string) => (/(^|\s)w-/.test(className ?? "") ? "" : "w-full");

export function Input({ className, ...rest }: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...rest} className={cx(controlClass, widthOf(className), "h-8", className)} />;
}

/** A box to narrow a list by what is typed, with a way to clear it. */
export function SearchBox({ value, onChange, label, className }: { value: string; onChange: (value: string) => void; label: string; className?: string }) {
  return (
    <div className={cx("relative", className ?? "w-full sm:w-64")}>
      <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-faint" />
      <Input type="search" aria-label={label} placeholder="Search" value={value} onChange={(e) => onChange(e.target.value)} onKeyDown={(e) => {
          if (e.key !== "Escape" || !value) return;
          e.preventDefault();
          e.stopPropagation();
          onChange("");
        }} className="w-full pl-8" spellCheck={false} />
    </div>
  );
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
  /** A page elsewhere that tells more, linked at the end of the label's line. */
  docs?: { href: string; about: string };
  children: (id: string) => ReactNode;
  className?: string;
  /** What is said when it is left empty, where "Fill this in." is not enough. */
  missing?: string;
}

/** A labelled form control. The child receives the id to put on its input. */
export function Field({ label, hint, docs, children, className, missing }: FieldProps) {
  const id = useId();
  const { ref, problem } = useProblem(missing);
  const name = (
    <label htmlFor={id} className="text-[13px] font-medium text-fg">
      {label}
    </label>
  );
  return (
    <div ref={ref} className={cx("flex flex-col gap-1.5", className)}>
      {docs ? (
        <div className="flex items-baseline justify-between gap-3">
          {name}
          <DocsLink {...docs} />
        </div>
      ) : (
        name
      )}
      {children(id)}
      {problem && <p className="text-xs text-danger">{problem}</p>}
      {hint && <p className="text-xs leading-relaxed text-muted empty:hidden">{hint}</p>}
    </div>
  );
}

type Control = HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement;

const isControl = (t: EventTarget | null): t is Control => t instanceof HTMLInputElement || t instanceof HTMLSelectElement || t instanceof HTMLTextAreaElement;

/** What is wrong with a control, in a few plain words. */
function problemOf(c: Control, missing?: string): string {
  const v = c.validity;
  const input = c instanceof HTMLInputElement ? c : null;
  if (v.valueMissing) return missing ?? (input?.type === "checkbox" ? "Check this box." : c instanceof HTMLSelectElement ? "Pick one." : "Fill this in.");
  if (v.badInput) return "Enter a number.";
  if (v.patternMismatch || v.typeMismatch) return c.title || "Check what is typed here.";
  if (v.tooShort && !(c instanceof HTMLSelectElement)) return `Use at least ${c.minLength} characters.`;
  if (v.tooLong && !(c instanceof HTMLSelectElement)) return `Use at most ${c.maxLength} characters.`;
  if (v.rangeUnderflow && input) return `Use ${input.min} or more.`;
  if (v.rangeOverflow && input) return `Use ${input.max} or less.`;
  if (v.stepMismatch && input) return `Use steps of ${input.step}.`;
  return c.validationMessage;
}

/**
 * Says what is wrong with a control in the element given the ref, once a
 * form with it is sent: the problem, said under it, and the control marked,
 * until it is put right. Field has this; a control without a Field is put
 * in a Checked.
 */
function useProblem(missing?: string) {
  const ref = useRef<HTMLDivElement>(null);
  const [problem, setProblem] = useState<string | null>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    // Invalid does not bubble: it is caught on its way down.
    const invalid = (e: Event) => {
      if (!isControl(e.target)) return;
      e.target.setAttribute("aria-invalid", "true");
      setProblem(problemOf(e.target, missing));
    };
    const changed = (e: Event) => {
      const c = e.target;
      if (!isControl(c) || c.getAttribute("aria-invalid") !== "true") return;
      // Once the change has reached the page: a render before it would put
      // the value it had back in the control, and the change would be lost.
      setTimeout(() => {
        if (c.validity.valid) {
          c.removeAttribute("aria-invalid");
          setProblem(null);
        } else setProblem(problemOf(c, missing));
      });
    };
    el.addEventListener("invalid", invalid, true);
    el.addEventListener("input", changed, true);
    el.addEventListener("change", changed, true);
    return () => {
      el.removeEventListener("invalid", invalid, true);
      el.removeEventListener("input", changed, true);
      el.removeEventListener("change", changed, true);
    };
  }, [missing]);
  return { ref, problem };
}

/** A control without a Field, whose problem is said under it as a Field's is. */
export function Checked({ missing, className, children }: { missing?: string; className?: string; children: ReactNode }) {
  const { ref, problem } = useProblem(missing);
  return (
    <div ref={ref} className={cx("flex flex-col gap-1", className)}>
      {children}
      {problem && <p className="text-xs text-danger">{problem}</p>}
    </div>
  );
}

let reporting = false;

/**
 * In place of the browser's bubble on a form sent with something missing or
 * wrong, the panel's own: the problem is said under each such control (see
 * Field and Checked), and the first is scrolled to and focused. Set up once,
 * for every form.
 */
export function sayProblemsInPlace() {
  document.addEventListener(
    "invalid",
    (e) => {
      e.preventDefault();
      // The invalid controls of one form sent are told of one after another,
      // at once: the first is the one gone to.
      if (reporting || !isControl(e.target)) return;
      reporting = true;
      setTimeout(() => (reporting = false));
      e.target.focus({ preventScroll: true });
      e.target.scrollIntoView({ block: "center" });
    },
    true,
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

/**
 * A page elsewhere that tells more about a setting, opened in a tab of its
 * own. about says what it is about to a screen reader: "Docs" alone does not.
 */
export function DocsLink({ href, about }: { href: string; about: string }) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      aria-label={`Docs: ${about}`}
      className="inline-flex w-fit shrink-0 items-center gap-1 text-xs font-normal text-accent hover:underline"
    >
      Docs
      <ExternalLink className="size-3" />
    </a>
  );
}

/** What a Tip gives the control it is about. */
export interface TipTarget {
  "aria-describedby": string;
}

/**
 * A few words about a control that has none beside it, shown while it is
 * pointed at, focused from the keyboard, or tapped. children is given what
 * ties the words to the control, to put on the one that takes the focus.
 * With tapShows, a tap only shows the words, and does not also do what a
 * click on it would, such as following the link it is in. With inLink, it
 * is about part of a link, which takes the focus for it: the words show
 * while the keyboard is on that link.
 */
export function Tip({
  text,
  tapShows,
  inLink,
  className,
  children,
}: {
  text: string;
  tapShows?: boolean;
  inLink?: boolean;
  className?: string;
  children: (target: TipTarget) => ReactNode;
}) {
  const id = useId();
  const box = useRef<HTMLSpanElement>(null);
  const bubble = useRef<HTMLSpanElement>(null);
  const [open, setOpen] = useState(false);
  const [at, setAt] = useState<{ left: number; top: number } | null>(null);
  const hovering = useRef<ReturnType<typeof setTimeout>>(undefined);
  const tapped = useRef(false);

  useEffect(() => {
    const link = inLink ? box.current?.closest("a") : null;
    if (!link) return;
    const focused = () => link.matches(":focus-visible") && setOpen(true);
    const blurred = () => setOpen(false);
    link.addEventListener("focus", focused);
    link.addEventListener("blur", blurred);
    return () => {
      link.removeEventListener("focus", focused);
      link.removeEventListener("blur", blurred);
    };
  }, [inLink]);

  useLayoutEffect(() => {
    if (!open) {
      setAt(null);
      return;
    }
    // Above the control, or below it where there is no room above; never off the side of the window.
    const place = () => {
      const r = box.current?.getBoundingClientRect();
      const b = bubble.current?.getBoundingClientRect();
      if (!r || !b) return;
      const above = r.top - b.height - 6;
      const left = Math.min(Math.max(8, r.left + r.width / 2 - b.width / 2), window.innerWidth - b.width - 8);
      setAt({ left, top: above >= 4 ? above : r.bottom + 6 });
    };
    place();
    const away = (e: PointerEvent) => {
      if (!box.current?.contains(e.target as Node)) setOpen(false);
    };
    const escape = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    window.addEventListener("scroll", place, true);
    window.addEventListener("resize", place);
    document.addEventListener("pointerdown", away);
    document.addEventListener("keydown", escape);
    return () => {
      window.removeEventListener("scroll", place, true);
      window.removeEventListener("resize", place);
      document.removeEventListener("pointerdown", away);
      document.removeEventListener("keydown", escape);
    };
  }, [open]);
  useEffect(() => () => clearTimeout(hovering.current), []);

  const pointer = (e: { pointerType: string }) => e.pointerType === "mouse" || e.pointerType === "pen";
  return (
    <span
      ref={box}
      className={cx("inline-flex", className)}
      onPointerEnter={(e) => {
        if (!pointer(e)) return;
        clearTimeout(hovering.current);
        hovering.current = setTimeout(() => setOpen(true), 250);
      }}
      onPointerLeave={(e) => {
        if (!pointer(e)) return;
        clearTimeout(hovering.current);
        setOpen(false);
      }}
      onPointerDown={(e) => {
        tapped.current = e.pointerType === "touch";
        if (tapped.current) setOpen((o) => !o);
      }}
      onClickCapture={(e) => {
        if (!tapShows || !tapped.current) return;
        e.preventDefault();
        e.stopPropagation();
      }}
      // Focus the keyboard moved. A click focuses too, and the pointer is already showing it.
      onFocus={(e) => e.target.matches(":focus-visible") && setOpen(true)}
      onBlur={() => setOpen(false)}
    >
      {children({ "aria-describedby": id })}
      {createPortal(
        <span
          ref={bubble}
          id={id}
          role="tooltip"
          hidden={!open}
          style={at ? { left: at.left, top: at.top } : { left: 0, top: 0, visibility: "hidden" }}
          className="pointer-events-none fixed z-[70] w-max max-w-60 rounded-md border border-line-strong bg-surface px-2 py-1 text-xs leading-snug font-normal text-fg shadow-lg shadow-shade/50"
        >
          {text}
        </span>,
        document.body,
      )}
    </span>
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

/**
 * A server's status dot at the start of the link to it, which says what it
 * means when pointed at or tapped, and while the keyboard is on the link. A
 * screen reader is not told: the link says the status in words.
 */
export function LinkStatusDot({ phase }: { phase?: Phase }) {
  return (
    <Tip text={phaseStyle[phase ?? "Stopped"].label} tapShows inLink className="-m-1.5 shrink-0">
      {() => (
        <span aria-hidden data-status-dot className="flex p-1.5">
          <StatusDot phase={phase} />
        </span>
      )}
    </Tip>
  );
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
          () => toast.error("Could not copy. Copy it manually."),
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
        {/* Dimmed, not blurred: a blur behind the whole page is drawn again for
            every frame of anything that moves in front of it, and a browser
            drawing without a graphics card falls to a few frames a second. */}
        <Dialog.Overlay className="fixed inset-0 z-40 bg-scrim/60" />
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
            "fixed top-[10vh] left-1/2 z-50 flex max-h-[80vh] w-[calc(100vw-2rem)] -translate-x-1/2 animate-rise flex-col rounded-lg border border-line-strong bg-surface shadow-2xl shadow-shade/60 outline-none",
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
          // Opened from within another form, as React sees it: that one is not sent with it.
          e.stopPropagation();
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
        className="z-50 max-h-[var(--radix-dropdown-menu-content-available-height)] min-w-44 animate-rise overflow-y-auto rounded-md border border-line-strong bg-surface p-1 shadow-xl shadow-shade/50"
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

const choiceClass = "flex h-8 cursor-pointer items-center gap-2 rounded-sm px-2 text-[13px] text-fg outline-none select-none data-[highlighted]:bg-hover";
const ticked = (
  <span className="flex size-4 items-center justify-center text-accent">
    <Dropdown.ItemIndicator>
      <Check className="size-4" />
    </Dropdown.ItemIndicator>
  </span>
);

export const MenuRadioGroup = Dropdown.RadioGroup;

/** One of several, of which one is chosen. */
export function MenuRadio({ value, children }: { value: string; children: ReactNode }) {
  return (
    <Dropdown.RadioItem value={value} className={choiceClass}>
      {ticked}
      {children}
    </Dropdown.RadioItem>
  );
}

/** One that is on or off. The menu stays open, for others to be ticked too. */
export function MenuCheck({ checked, onChange, children }: { checked: boolean; onChange: () => void; children: ReactNode }) {
  return (
    <Dropdown.CheckboxItem checked={checked} onCheckedChange={onChange} onSelect={(e) => e.preventDefault()} className={choiceClass}>
      {ticked}
      {children}
    </Dropdown.CheckboxItem>
  );
}

/** One choice of a SelectMenu: what it is called, and a few words on what it means. */
export interface Choice<T extends string> {
  value: T;
  label: string;
  description: string;
}

/**
 * Looks like a Select, but each choice has a few words under it on what it
 * means, which a Select's choices cannot. For choices whose names do not say
 * enough by themselves, such as roles.
 */
export function SelectMenu<T extends string>({
  value,
  choices,
  onChange,
  className,
  ...rest
}: { value: T; choices: Choice<T>[]; onChange: (value: T) => void; className?: string; id?: string; "aria-label"?: string }) {
  const base = useId();
  const current = choices.find((c) => c.value === value);
  return (
    <Dropdown.Root>
      <Dropdown.Trigger
        {...rest}
        className={cx(controlClass, widthOf(className), "h-8 bg-[length:16px] bg-[right_6px_center] bg-no-repeat pr-7 text-left", className)}
        style={{ backgroundImage: chevron }}
      >
        {current?.label ?? value}
      </Dropdown.Trigger>
      <Dropdown.Portal>
        <Dropdown.Content
          align="start"
          sideOffset={4}
          className="z-50 max-h-[var(--radix-dropdown-menu-content-available-height)] min-w-[var(--radix-dropdown-menu-trigger-width)] animate-rise overflow-y-auto rounded-md border border-line-strong bg-surface p-1 shadow-xl shadow-shade/50"
        >
          <Dropdown.RadioGroup value={value} onValueChange={(v) => v !== value && onChange(v as T)}>
            {choices.map((c) => (
              <Dropdown.RadioItem
                key={c.value}
                value={c.value}
                aria-labelledby={`${base}-${c.value}`}
                aria-describedby={`${base}-${c.value}-about`}
                className="flex cursor-pointer items-start gap-2 rounded-sm px-2 py-1.5 text-[13px] text-fg outline-none select-none data-[highlighted]:bg-hover"
              >
                <span className="flex h-lh items-center">{ticked}</span>
                <span className="flex flex-col">
                  <span id={`${base}-${c.value}`}>{c.label}</span>
                  <span id={`${base}-${c.value}-about`} className="text-xs text-muted">
                    {c.description}
                  </span>
                </span>
              </Dropdown.RadioItem>
            ))}
          </Dropdown.RadioGroup>
        </Dropdown.Content>
      </Dropdown.Portal>
    </Dropdown.Root>
  );
}

export const MenuSub = Dropdown.Sub;

/** Opens a menu of its own beside this one, with what is chosen in it now shown after its name. */
export function MenuSubTrigger({ icon, children, value }: { icon?: ReactNode; children: ReactNode; value?: ReactNode }) {
  return (
    <Dropdown.SubTrigger className="flex h-8 cursor-pointer items-center gap-2 rounded-sm px-2 text-[13px] text-fg outline-none select-none data-[highlighted]:bg-hover data-[state=open]:bg-hover">
      <span className="flex size-4 items-center justify-center text-muted [&>svg]:size-4">{icon}</span>
      {children}
      <span className="ml-auto flex items-center gap-1.5 pl-4 text-xs text-muted">
        {value}
        <ChevronRight className="size-3.5" />
      </span>
    </Dropdown.SubTrigger>
  );
}

export function MenuSubContent({ children }: { children: ReactNode }) {
  return (
    <Dropdown.Portal>
      <Dropdown.SubContent
        sideOffset={4}
        className="z-50 max-h-[var(--radix-dropdown-menu-content-available-height)] min-w-40 animate-rise overflow-y-auto rounded-md border border-line-strong bg-surface p-1 shadow-xl shadow-shade/50"
      >
        {children}
      </Dropdown.SubContent>
    </Dropdown.Portal>
  );
}

export const MenuSeparator = () =><Dropdown.Separator className="my-1 h-px bg-line" />;
export const MenuLabel = ({ children }: { children: ReactNode }) => <Dropdown.Label className="label-mono px-2 py-1.5">{children}</Dropdown.Label>;

export function Toaster({ children }: { children?: ReactNode }) {
  const toasts = useToasts();
  return (
    // Above a bar along the bottom of the page, where there is one, and not over its buttons.
    <div className="pointer-events-none fixed right-4 bottom-4 z-[60] flex w-80 flex-col gap-2 [body:has([data-bottom-bar])_&]:bottom-18" role="status" aria-live="polite">
      {children}
      {toasts.map((t) => (
        <div
          key={t.id}
          className={cx(
            "pointer-events-auto flex animate-rise items-start gap-3 rounded-md border bg-surface px-3 py-2.5 text-[13px] leading-relaxed shadow-xl shadow-shade/50",
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
 * Above a list whose rows can be ticked, in the same box: ticks every one
 * of them, or none, and says how many there are.
 */
export function SelectAllRow({ selection, ids, noun, className }: { selection: Selection; ids: string[]; noun: string; className?: string }) {
  const ticked = ids.filter((id) => selection.has(id)).length;
  const all = ids.length > 0 && ticked === ids.length;
  const plural = `${noun}${ids.length === 1 ? "" : "s"}`;
  return (
    <div className={cx("group flex h-9 items-center gap-4 border-b border-line px-4 text-xs text-muted", className)}>
      <label className="flex cursor-pointer items-center gap-4">
        <RowCheckbox checked={all} partial={ticked > 0} quiet={selection.count === 0} label={`Select every ${noun}`} onChange={() => selection.toggleSome(ids)} />
        <span>{ticked > 0 ? `${ticked} of ${ids.length} ${plural} selected` : `${ids.length} ${plural}`}</span>
      </label>
    </div>
  );
}

/**
 * A bar fixed to the bottom of the window, as tall as what is in it needs. As
 * much room as it takes is left where it is placed, after the page's content,
 * so that it never covers the end of it.
 */
export function BottomBar({ children, ...rest }: HTMLAttributes<HTMLDivElement>) {
  const bar = useRef<HTMLDivElement>(null);
  const [height, setHeight] = useState(0);
  useLayoutEffect(() => {
    const el = bar.current;
    if (!el) return;
    setHeight(el.offsetHeight);
    const watch = new ResizeObserver(() => setHeight(el.offsetHeight));
    watch.observe(el);
    return () => watch.disconnect();
  }, []);
  return (
    <>
      <div aria-hidden style={{ height }} />
      <div ref={bar} {...rest} data-bottom-bar className="fixed inset-x-0 bottom-0 z-30 border-t border-line-strong bg-surface/95 backdrop-blur md:left-56">
        {children}
      </div>
    </>
  );
}

/**
 * The bar that appears along the bottom while rows are selected, with what
 * can be done to them.
 */
export function SelectionBar({ count, noun, onClear, children }: { count: number; noun: string; onClear: () => void; children: ReactNode }) {
  if (count === 0) return null;
  return (
      <BottomBar role="region" aria-label="Selection">
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
      </BottomBar>
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
export function Section({ title, description, region, children }: { title: string; description?: ReactNode; region?: boolean; children: ReactNode }) {
  return (
    <section aria-label={region ? title : undefined} className="grid gap-x-10 gap-y-4 border-t border-line py-7 first:border-t-0 first:pt-0 md:grid-cols-[220px_minmax(0,1fr)]">
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
