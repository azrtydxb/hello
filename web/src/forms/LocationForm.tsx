import { useState, type FormEvent } from "react";
import { errorMessage, fieldErrors, type FieldError } from "../api";
import {
  createLocation,
  putLocation,
  type Location,
  type LocationFields,
  type LocationKind,
} from "../api/location";
import {
  Button,
  Drawer,
  Input,
  useRestoreFocus,
} from "../design/azrty/components";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { FormAlert } from "../pages/callflow/ui";
import { Can } from "../role";

const FORM_ID = "location-form";
const COUNTRY = /^([A-Z]{2})?$/;

interface Draft {
  name: string;
  country: string;
  state: string;
  city: string;
  street: string;
  houseNumber: string;
  postalCode: string;
  latitude: string;
  longitude: string;
  radiusM: string;
}

const draftOf = (l: Location | null): Draft => ({
  name: l?.name ?? "",
  country: l?.country ?? "",
  state: l?.state ?? "",
  city: l?.city ?? "",
  street: l?.street ?? "",
  houseNumber: l?.houseNumber ?? "",
  postalCode: l?.postalCode ?? "",
  latitude: l?.latitude == null ? "" : String(l.latitude),
  longitude: l?.longitude == null ? "" : String(l.longitude),
  radiusM: l?.radiusM == null ? "" : String(l.radiusM),
});

const KEYS = [
  "name",
  "parentId",
  "country",
  "state",
  "city",
  "street",
  "houseNumber",
  "postalCode",
  "latitude",
  "longitude",
  "radiusM",
];

const num = (s: string): number | null => (s.trim() === "" ? null : Number(s));

function validate(kind: LocationKind, d: Draft): Record<string, string> {
  const out: Record<string, string> = {};
  if (d.name.trim() === "" || d.name.length > 100) {
    out.name = "Use 1 to 100 characters.";
  } else if (/[<>&"'\n\r]/.test(d.name)) {
    out.name = "Do not use <, >, &, quotes or line breaks.";
  }
  if (!COUNTRY.test(d.country)) {
    out.country = "Use a two-letter country code in capitals, for example NL.";
  }
  const lat = num(d.latitude);
  const lon = num(d.longitude);
  const rad = num(d.radiusM);
  if ((lat === null) !== (lon === null)) {
    out.latitude = "Give the latitude and the longitude together.";
  }
  if (lat !== null && (Number.isNaN(lat) || lat < -90 || lat > 90)) {
    out.latitude = "Use degrees from -90 to 90.";
  }
  if (lon !== null && (Number.isNaN(lon) || lon < -180 || lon > 180)) {
    out.longitude = "Use degrees from -180 to 180.";
  }
  if (rad !== null && (Number.isNaN(rad) || rad <= 0 || rad > 1_000_000)) {
    out.radiusM = "Use metres above 0.";
  } else if (rad !== null && lat === null) {
    out.radiusM = "A radius needs a latitude and longitude.";
  }
  if (
    kind === "site" &&
    d.country === "" &&
    d.city.trim() === "" &&
    d.street.trim() === "" &&
    d.postalCode.trim() === "" &&
    lat === null
  ) {
    out.country = "A site needs an address or a position.";
  }
  return out;
}

/** The drawer that adds or edits a site, floor or room. */
export function LocationDrawer({
  location,
  kind,
  parentId,
  parentName,
  onClose,
  onSaved,
}: {
  location: Location | null;
  kind: LocationKind;
  parentId: number | null;
  parentName: string;
  onClose: () => void;
  onSaved: (l: Location, created: boolean) => void;
}) {
  useRestoreFocus();
  const [d, setD] = useState(() => draftOf(location));
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const set = <K extends keyof Draft>(k: K, v: string) =>
    setD((prev) => ({ ...prev, [k]: v }));
  const title = location
    ? `Edit ${kind} ${location.name}`
    : `New ${kind}${parentName ? ` in ${parentName}` : ""}`;

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    setUnmatched([]);
    const found = validate(kind, d);
    setErrors(found);
    if (Object.keys(found).length > 0) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    setBusy(true);
    try {
      const body: LocationFields = {
        kind,
        name: d.name.trim(),
        parentId: location ? location.parentId : parentId,
        country: kind === "site" ? d.country : "",
        state: kind === "site" ? d.state : "",
        city: kind === "site" ? d.city : "",
        street: kind === "site" ? d.street : "",
        houseNumber: kind === "site" ? d.houseNumber : "",
        postalCode: kind === "site" ? d.postalCode : "",
        latitude: num(d.latitude),
        longitude: num(d.longitude),
        radiusM: num(d.radiusM),
      };
      onSaved(
        location
          ? await putLocation(location.id, body)
          : await createLocation(body),
        location === null,
      );
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), KEYS);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Drawer
      title={title}
      description={
        kind === "site"
          ? "A site carries the civic address and, if you know it, a position."
          : kind === "floor"
            ? "A floor is named as the civic floor (FLR), for example 3."
            : "A room is named as the civic room (ROOM), for example Server room."
      }
      onClose={busy ? undefined : onClose}
      width={520}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Can>
            <Button type="submit" form={FORM_ID} disabled={busy}>
              {location ? "Save location" : "Create location"}
            </Button>
          </Can>
        </>
      }
    >
      <form
        id={FORM_ID}
        className="cf-form"
        aria-label={title}
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <Input
          id="location-name"
          label="Name"
          autoFocus
          value={d.name}
          error={errors.name ?? errors.parentId}
          onChange={(e) => set("name", e.target.value)}
        />
        {kind === "site" && (
          <>
            <div className="cf-two">
              <Input
                id="location-country"
                label="Country"
                mono
                value={d.country}
                error={errors.country}
                hint="Two letters, for example NL."
                onChange={(e) => set("country", e.target.value)}
              />
              <Input
                id="location-state"
                label="State or province"
                value={d.state}
                error={errors.state}
                onChange={(e) => set("state", e.target.value)}
              />
            </div>
            <div className="cf-two">
              <Input
                id="location-city"
                label="City"
                value={d.city}
                error={errors.city}
                onChange={(e) => set("city", e.target.value)}
              />
              <Input
                id="location-postal"
                label="Postal code"
                value={d.postalCode}
                error={errors.postalCode}
                onChange={(e) => set("postalCode", e.target.value)}
              />
            </div>
            <div className="cf-two">
              <Input
                id="location-street"
                label="Street"
                value={d.street}
                error={errors.street}
                onChange={(e) => set("street", e.target.value)}
              />
              <Input
                id="location-house"
                label="House number"
                value={d.houseNumber}
                error={errors.houseNumber}
                onChange={(e) => set("houseNumber", e.target.value)}
              />
            </div>
          </>
        )}
        <div className="cf-two">
          <Input
            id="location-lat"
            label="Latitude"
            mono
            inputMode="decimal"
            value={d.latitude}
            error={errors.latitude}
            hint="Optional, degrees (WGS 84)."
            onChange={(e) => set("latitude", e.target.value)}
          />
          <Input
            id="location-lon"
            label="Longitude"
            mono
            inputMode="decimal"
            value={d.longitude}
            error={errors.longitude}
            onChange={(e) => set("longitude", e.target.value)}
          />
        </div>
        <Input
          id="location-radius"
          label="Radius (metres)"
          mono
          inputMode="decimal"
          value={d.radiusM}
          error={errors.radiusM}
          hint="Optional. How far off the position may be."
          onChange={(e) => set("radiusM", e.target.value)}
        />
        <FormAlert message={formError} unmatched={unmatched} />
      </form>
    </Drawer>
  );
}
