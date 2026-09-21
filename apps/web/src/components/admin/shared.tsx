import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";

// ---- notices: the last thing that happened, always visible in the status strip ----
interface Notice {
  at: number;
  text: string;
  ok: boolean;
}
interface AdminCtx {
  notice: Notice | null;
  notify: (text: string, ok?: boolean) => void;
}
const Ctx = createContext<AdminCtx>({ notice: null, notify: () => {} });
export const useAdmin = () => useContext(Ctx);

export function AdminProvider({ children }: { children: React.ReactNode }) {
  const [notice, setNotice] = useState<Notice | null>(null);
  const notify = useCallback((text: string, ok = true) => setNotice({ at: Date.now(), text, ok }), []);
  return <Ctx.Provider value={{ notice, notify }}>{children}</Ctx.Provider>;
}

// ---- polling: organiser screens are few, so polling keeps them simple and always correct ----
export function usePoll<T>(path: string, ms: number) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [at, setAt] = useState<number | null>(null);

  const load = useCallback(() => {
    if (document.hidden) return Promise.resolve();
    return api
      .get<T>(path)
      .then((d) => {
        setData(d);
        setError(null);
        setAt(Date.now());
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : "request failed"));
  }, [path]);

  useEffect(() => {
    load();
    const t = setInterval(load, ms);
    document.addEventListener("visibilitychange", load);
    return () => {
      clearInterval(t);
      document.removeEventListener("visibilitychange", load);
    };
  }, [load, ms]);

  return { data, error, at, reload: load };
}

/** Re-renders every `ms` so countdowns move between polls. */
export function useNow(ms = 1000): number {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(t);
  }, [ms]);
  return now;
}

/** Shown when a screen's data cannot be fetched: an organiser must never act on stale numbers unknowingly. */
export function LoadError({ error, at }: { error: string | null; at: number | null }) {
  if (!error) return null;
  return (
    <div role="alert" className="banner" style={{ margin: "0 0 14px" }}>
      Can’t reach the server ({error}). What you see is{at ? ` from ${new Date(at).toLocaleTimeString()} and` : ""} may be out of date.
    </div>
  );
}

// ---- confirm dialog: every state-changing action asks "are you sure?" and nothing more ----
export function ConfirmDialog({
  open,
  title,
  description,
  danger,
  confirmLabel = "Confirm",
  onConfirm,
  onCancel,
}: {
  open: boolean;
  title: string;
  description?: React.ReactNode;
  danger?: boolean;
  confirmLabel?: string;
  onConfirm: () => Promise<void>;
  onCancel: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) {
      setError(null);
      d.showModal();
    } else if (!open && d.open) d.close();
  }, [open]);

  async function go(e: React.FormEvent) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      await onConfirm();
    } catch (err) {
      setError(err instanceof Error ? err.message : "the action failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <dialog ref={ref} className="dialog" onCancel={(e) => (e.preventDefault(), !busy && onCancel())} onClose={() => open && onCancel()}>
      <form onSubmit={go} className="stack">
        <h3 className={danger ? "down" : undefined}>{title}</h3>
        {description && <div className="dim">{description}</div>}
        {error && <div className="down">{error}</div>}
        <div className="row-end">
          {/* Cancel has the focus, so pressing Enter by accident never confirms something drastic. */}
          <button type="button" onClick={onCancel} disabled={busy} autoFocus>
            Cancel
          </button>
          <button type="submit" className={danger ? "solid danger" : "solid"} disabled={busy}>
            {busy ? "Working…" : confirmLabel}
          </button>
        </div>
      </form>
    </dialog>
  );
}

/** A button that asks first. `run` should throw on failure. */
export function ActionButton({
  label,
  title,
  description,
  danger,
  disabled,
  className,
  run,
}: {
  label: React.ReactNode;
  title: string;
  description?: React.ReactNode;
  danger?: boolean;
  disabled?: boolean;
  className?: string;
  run: () => Promise<unknown>;
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button className={className} disabled={disabled} onClick={() => setOpen(true)}>
        {label}
      </button>
      <ConfirmDialog
        open={open}
        title={title}
        description={description}
        danger={danger}
        onCancel={() => setOpen(false)}
        onConfirm={async () => {
          await run();
          setOpen(false);
        }}
      />
    </>
  );
}

/** Segmented choice whose changes each go through a confirm dialog. */
export function ChoiceControl<T extends string>({
  value,
  options,
  title,
  describe,
  onChoose,
}: {
  value: T;
  options: { value: T; label: string }[];
  title: string;
  describe?: (next: T) => React.ReactNode;
  onChoose: (next: T) => Promise<unknown>;
}) {
  const [pending, setPending] = useState<T | null>(null);
  return (
    <>
      <div className="seg" role="group" aria-label={title}>
        {options.map((o) => (
          <button key={o.value} aria-pressed={o.value === value} onClick={() => o.value !== value && setPending(o.value)}>
            {o.label}
          </button>
        ))}
      </div>
      <ConfirmDialog
        open={pending !== null}
        title={title}
        description={pending !== null && describe?.(pending)}
        onCancel={() => setPending(null)}
        onConfirm={async () => {
          if (pending !== null) await onChoose(pending);
          setPending(null);
        }}
      />
    </>
  );
}

/** Runs an admin POST, tells the organiser what happened, and refreshes the screen. */
export function useDo(reload: () => unknown) {
  const { notify } = useAdmin();
  return useCallback(
    async (path: string, body: Record<string, unknown>, doneText: string) => {
      try {
        await api.post(path, body);
        notify(doneText);
        await reload();
      } catch (err) {
        notify(err instanceof Error ? err.message : "the action failed", false);
        throw err;
      }
    },
    [notify, reload]
  );
}

export function Badge({ children, tone }: { children: React.ReactNode; tone?: "up" | "down" | "flag" }) {
  return <span className={`badge ${tone ?? ""}`}>{children}</span>;
}
