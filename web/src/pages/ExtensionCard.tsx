import { useEffect, useState } from "react";
import { Alert, Badge } from "../design/azrty/components";

/** What /downloads/hello-extension.json says about the package in this image. */
export interface ExtensionInfo {
  version: string;
  id: string;
  signed: boolean;
}

/**
 * The browser extension's download card (docs/browser-extension.md): the
 * package is built into the web image, so the card reads the build's own
 * description and links the files next to it. Without a package (a dev server,
 * an image built without it) the card says so instead of linking nothing.
 */
export function ExtensionCard() {
  const [info, setInfo] = useState<ExtensionInfo | null | undefined>(undefined);
  useEffect(() => {
    const ctl = new AbortController();
    fetch("/downloads/hello-extension.json", { signal: ctl.signal })
      .then((r) => (r.ok ? (r.json() as Promise<ExtensionInfo>) : null))
      .then(setInfo, () => {
        if (!ctl.signal.aborted) setInfo(null);
      });
    return () => ctl.abort();
  }, []);

  return (
    <div className="az-card pf-stack">
      <h2>Browser extension</h2>
      <p className="az-field__hint">
        For Chrome and Edge. It turns phone links, and optionally the numbers in
        the text of the sites you allow, into click-to-call, and shows who is
        calling. It asks you to confirm every call.
      </p>
      {info === undefined ? null : info === null ? (
        <Alert tone="info" title="No package in this installation">
          This installation was built without the browser extension.
        </Alert>
      ) : (
        <>
          <p>
            Version <span className="media-mono">{info.version}</span>{" "}
            {info.signed ? (
              <Badge tone="good">signed</Badge>
            ) : (
              <Badge tone="neutral">unsigned</Badge>
            )}
          </p>
          <ol>
            <li>
              Download{" "}
              <a href="/downloads/hello-extension.zip" download>
                hello-extension.zip
              </a>
              {info.signed && (
                <>
                  {" "}
                  (or the signed{" "}
                  <a href="/downloads/hello-extension.crx" download>
                    hello-extension.crx
                  </a>{" "}
                  for a managed browser)
                </>
              )}{" "}
              and unpack the zip.
            </li>
            <li>
              Open <span className="media-mono">chrome://extensions</span> (
              <span className="media-mono">edge://extensions</span>), turn on
              Developer mode and choose Load unpacked.
            </li>
            <li>
              Open the extension&apos;s settings, enter this address, and sign
              in: <span className="media-mono">{window.location.origin}</span>
            </li>
          </ol>
          <p className="az-field__hint">
            Extension id <span className="media-mono">{info.id}</span>. It is
            the same for every Hello installation.
          </p>
        </>
      )}
    </div>
  );
}
