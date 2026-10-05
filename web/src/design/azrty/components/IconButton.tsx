import type { ButtonHTMLAttributes } from "react";
import { cx } from "./cx";
import { Icon } from "./Icon";

/** Props for <IconButton>. */
export interface IconButtonProps extends Omit<
  ButtonHTMLAttributes<HTMLButtonElement>,
  "children"
> {
  icon: string;
  /** Accessible name and tooltip; required, the button has no text. */
  label: string;
  size?: number;
}

/** A square icon-only button. */
export function IconButton({
  icon,
  label,
  size = 16,
  className,
  type = "button",
  ...rest
}: IconButtonProps) {
  return (
    <button
      type={type}
      className={cx("az-iconbtn", className)}
      aria-label={label}
      title={label}
      {...rest}
    >
      <Icon name={icon} size={size} />
    </button>
  );
}
