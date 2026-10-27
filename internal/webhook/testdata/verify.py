import hashlib
import hmac
import time


def verify(secret: str, header: str, raw_body: bytes, now: float | None = None) -> None:
    """Raise ValueError unless header (Hello-Signature) matches raw_body, the
    exact bytes received, under secret, and is less than 5 minutes old."""
    parts = [p.strip().partition("=") for p in header.split(",")]
    t = next((int(v) for k, _, v in parts if k == "t" and v.isdigit()), None)
    sigs = [v for k, _, v in parts if k == "v1"]
    if t is None or abs((time.time() if now is None else now) - t) > 300:
        raise ValueError("stale or missing timestamp")
    want = hmac.new(secret.encode(), f"{t}.".encode() + raw_body, hashlib.sha256).hexdigest()
    if not any(hmac.compare_digest(want.encode(), s.encode()) for s in sigs):
        raise ValueError("signature mismatch")
