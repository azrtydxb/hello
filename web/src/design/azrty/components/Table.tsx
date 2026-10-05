import type { CSSProperties, Key, ReactNode } from "react";
import { cx } from "./cx";

/** One table column entry. */
export interface TableColumn<R> {
  key: string;
  label: ReactNode;
  align?: "left" | "right" | "center";
  width?: number | string;
  /** Geist Mono cell (identifiers, numbers). */
  mono?: boolean;
  /** The first column is the row's primary text unless this is false. */
  primary?: boolean;
  render?: (row: R) => ReactNode;
}

/** Props for <Table>. */
export interface TableProps<R> {
  columns: ReadonlyArray<TableColumn<R>>;
  rows: ReadonlyArray<R>;
  rowKey: (row: R, index: number) => Key;
  /** Makes rows clickable; keep a real link or button in the row for keyboard users. */
  onRowClick?: (row: R) => void;
  /** Accessible table name. */
  caption?: ReactNode;
  className?: string;
  style?: CSSProperties;
}

/** A data table in a bordered card. */
export function Table<R>({
  columns,
  rows,
  rowKey,
  onRowClick,
  caption,
  className,
  style,
}: TableProps<R>) {
  return (
    <div className={cx("az-table-wrap", className)} style={style}>
      <table className="az-table">
        {caption && <caption className="visually-hidden">{caption}</caption>}
        <thead>
          <tr>
            {columns.map((c) => (
              <th
                key={c.key}
                scope="col"
                style={{ textAlign: c.align || "left", width: c.width }}
              >
                {c.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr
              key={rowKey(r, i)}
              className={onRowClick ? "az-table__row--click" : undefined}
              onClick={onRowClick ? () => onRowClick(r) : undefined}
            >
              {columns.map((c, ci) => (
                <td
                  key={c.key}
                  style={{ textAlign: c.align || "left" }}
                  className={
                    cx(
                      ci === 0 && c.primary !== false && "az-table__primary",
                      c.mono && "az-table__mono",
                    ) || undefined
                  }
                >
                  {c.render
                    ? c.render(r)
                    : ((r as Record<string, unknown>)[c.key] as ReactNode)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
