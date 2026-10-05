import type { ReactNode } from "react";
import { Link } from "react-router";
import type { ButtonVariant } from "./Button";
import { cx } from "./cx";
import { Icon } from "./Icon";
import "./hello.css";

/** Props for <LinkButton>. */
export interface LinkButtonProps {
  /** A console route, query included: "/routes/test?number=…". */
  to: string;
  variant?: ButtonVariant;
  size?: "sm" | "md";
  icon?: string;
  iconRight?: string;
  className?: string;
  "aria-label"?: string;
  children: ReactNode;
}

/** A router link drawn as a Button: navigation to another page, not an action. */
export function LinkButton({
  to,
  variant = "secondary",
  size = "md",
  icon,
  iconRight,
  className,
  children,
  ...rest
}: LinkButtonProps) {
  const is = size === "sm" ? 13 : 15;
  return (
    <Link
      to={to}
      className={cx(
        "az-btn",
        `az-btn--${variant}`,
        size === "sm" && "az-btn--sm",
        className,
      )}
      {...rest}
    >
      {icon && <Icon name={icon} size={is} />}
      {children}
      {iconRight && <Icon name={iconRight} size={is} />}
    </Link>
  );
}
