import type { ButtonHTMLAttributes, ReactNode } from "react";
import { cx } from "./cx";
import { Icon } from "./Icon";

/** The allowed button variant values. */
export type ButtonVariant =
  "primary" | "secondary" | "accent" | "ghost" | "danger";

/** Props for <Button>. */
export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: "sm" | "md" | "lg";
  /** Lucide icon before the label. */
  icon?: string;
  /** Lucide icon after the label. */
  iconRight?: string;
  /** Full width. */
  block?: boolean;
  children?: ReactNode;
}

/** The Azrty button: primary follows the pillar, the rest are neutral. */
export function Button({
  variant = "primary",
  size = "md",
  icon,
  iconRight,
  block,
  children,
  className,
  type = "button",
  ...rest
}: ButtonProps) {
  const is = size === "sm" ? 13 : size === "lg" ? 17 : 15;
  return (
    <button
      type={type}
      className={cx(
        "az-btn",
        `az-btn--${variant}`,
        size !== "md" && `az-btn--${size}`,
        block && "az-btn--block",
        !children && "az-btn--icon-only",
        className,
      )}
      {...rest}
    >
      {icon && <Icon name={icon} size={is} />}
      {children}
      {iconRight && <Icon name={iconRight} size={is} />}
    </button>
  );
}
