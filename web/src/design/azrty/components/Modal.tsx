import { useEffect, useId, type ReactNode } from "react";
import { cx } from "./cx";
import { IconButton } from "./IconButton";

/** Props for <Modal>. */
export interface ModalProps {
  open?: boolean;
  title: string;
  description?: ReactNode;
  /** Close on Escape, backdrop click and the X button. */
  onClose?: () => void;
  children?: ReactNode;
  /** Footer buttons, right-aligned. */
  actions?: ReactNode;
  /** Render in place without a backdrop (specimens). */
  inline?: boolean;
  width?: number | string;
  className?: string;
}

/** A centred dialog over a blurred backdrop. */
export function Modal({
  open = true,
  title,
  description,
  onClose,
  children,
  actions,
  inline,
  width,
  className,
}: ModalProps) {
  const titleId = useId();
  useEffect(() => {
    if (!open || inline || !onClose) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, inline, onClose]);
  if (!open) return null;
  const box = (
    <div
      className={cx("az-modal", inline && "az-modal--inline", className)}
      role="dialog"
      aria-modal={inline ? undefined : true}
      aria-labelledby={titleId}
      style={{ width }}
      onClick={(e) => e.stopPropagation()}
    >
      <div className="az-modal__head">
        <div>
          <h2 className="az-modal__title" id={titleId}>
            {title}
          </h2>
          {description && <p className="az-modal__desc">{description}</p>}
        </div>
        {onClose && <IconButton icon="x" label="Close" onClick={onClose} />}
      </div>
      <div className="az-modal__body">{children}</div>
      {actions && <div className="az-modal__actions">{actions}</div>}
    </div>
  );
  if (inline) return box;
  return (
    <div className="az-backdrop" onClick={onClose}>
      {box}
    </div>
  );
}
