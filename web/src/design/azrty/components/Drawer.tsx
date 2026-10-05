import { useEffect, useId, type CSSProperties, type ReactNode } from "react";
import { cx } from "./cx";
import { IconButton } from "./IconButton";

/** Props for <Drawer>. */
export interface DrawerProps {
  open?: boolean;
  title: string;
  description?: ReactNode;
  onClose?: () => void;
  children?: ReactNode;
  footer?: ReactNode;
  width?: number | string;
  inline?: boolean;
  className?: string;
  style?: CSSProperties;
}

/** A detail panel that slides in from the right. */
export function Drawer({
  open = true,
  title,
  description,
  onClose,
  children,
  footer,
  width,
  inline,
  className,
  style,
}: DrawerProps) {
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
  const panel = (
    <aside
      className={cx("az-drawer", inline && "az-drawer--inline", className)}
      role="dialog"
      aria-modal={inline ? undefined : true}
      aria-labelledby={titleId}
      style={{ width, ...style }}
      onClick={(e) => e.stopPropagation()}
    >
      <div className="az-drawer__head">
        <div>
          <h2 className="az-drawer__title" id={titleId}>
            {title}
          </h2>
          {description && <p className="az-drawer__desc">{description}</p>}
        </div>
        {onClose && <IconButton icon="x" label="Close" onClick={onClose} />}
      </div>
      <div className="az-drawer__body">{children}</div>
      {footer && <div className="az-drawer__foot">{footer}</div>}
    </aside>
  );
  if (inline) return panel;
  return (
    <div className="az-drawer-backdrop" onClick={onClose}>
      {panel}
    </div>
  );
}
