import type { CSSProperties, InputHTMLAttributes, ReactNode } from "react";
import { cx } from "./cx";
import { Icon } from "./Icon";

/** Props for <Input>. */
export interface InputProps extends Omit<
  InputHTMLAttributes<HTMLInputElement>,
  "size"
> {
  label?: ReactNode;
  /** Help text under the field (replaced by `error` when set). */
  hint?: ReactNode;
  error?: ReactNode;
  /** Lucide icon inside the field, on the left. */
  icon?: string;
  mono?: boolean;
  size?: "sm" | "md";
  /** Required when `label` is not a plain string, so the label can point at the input. */
  id?: string;
  className?: string;
  style?: CSSProperties;
}

/** Derives an input id from a string label, as the design system does. */
export function fieldId(
  id: string | undefined,
  label: ReactNode,
): string | undefined {
  if (id) return id;
  return typeof label === "string"
    ? "az-" + label.toLowerCase().replace(/[^a-z0-9]+/g, "-")
    : undefined;
}

/** A labelled text input with optional hint, error and icon. */
export function Input({
  label,
  hint,
  error,
  icon,
  mono,
  size,
  id,
  className,
  style,
  ...rest
}: InputProps) {
  const iid = fieldId(id, label);
  const hintId = iid && (error || hint) ? `${iid}-hint` : undefined;
  return (
    <div
      className={cx("az-field", Boolean(error) && "az-field--error", className)}
      style={style}
    >
      {label && (
        <label className="az-field__label" htmlFor={iid}>
          {label}
        </label>
      )}
      <span className={cx("az-input-wrap", icon && "az-input-wrap--icon")}>
        {icon && <Icon name={icon} size={15} />}
        <input
          id={iid}
          className={cx(
            "az-input",
            mono && "az-input--mono",
            size === "sm" && "az-input--sm",
          )}
          aria-invalid={error ? true : undefined}
          aria-describedby={hintId}
          {...rest}
        />
      </span>
      {(error || hint) && (
        <span className="az-field__hint" id={hintId}>
          {error || hint}
        </span>
      )}
    </div>
  );
}
