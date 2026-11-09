import qrcode from "qrcode-generator";
import { useMemo } from "react";

/**
 * A QR code for `value`, drawn as SVG in the browser from a local library:
 * the secret never leaves the page for a third-party image service.
 */
export function QRCode({
  value,
  label,
  size = 192,
}: {
  value: string;
  label: string;
  size?: number;
}) {
  const cells = useMemo(() => {
    const qr = qrcode(0, "M");
    qr.addData(value, "Byte");
    qr.make();
    const n = qr.getModuleCount();
    const dark: string[] = [];
    for (let r = 0; r < n; r++)
      for (let c = 0; c < n; c++)
        if (qr.isDark(r, c)) dark.push(`M${c} ${r}h1v1h-1z`);
    return { n, path: dark.join("") };
  }, [value]);
  const quiet = 4;
  const view = cells.n + quiet * 2;
  return (
    <svg
      role="img"
      aria-label={label}
      width={size}
      height={size}
      viewBox={`${-quiet} ${-quiet} ${view} ${view}`}
      shapeRendering="crispEdges"
      style={{ background: "#fff", borderRadius: 8 }}
    >
      <path d={cells.path} fill="#000" />
    </svg>
  );
}
