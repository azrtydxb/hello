// What the console needs to know about each CRM connector kind: how it signs
// in, which fields it takes and what the customer sets up on the CRM side.
// The server decides what is valid; this only drives the form and the help.

export interface KindField {
  key: string;
  label: string;
  /** A select when set, a text field otherwise. */
  options?: { value: string; label: string }[];
  defaultValue?: string;
  placeholder?: string;
  hint?: string;
}

export interface KindSetup {
  title: string;
  intro: string;
  /** Show the OAuth redirect URL to register in the CRM app. */
  redirect?: boolean;
  listTitle?: string;
  list?: string[];
}

export interface KindSpec {
  value: string;
  label: string;
  /** oauth: a browser sign-in; credential: the secret is verified on Connect. */
  auth: "oauth" | "credential";
  defaultName: string;
  /** The client id field; absent when the kind has none (Bitrix24). */
  clientIdLabel?: string;
  secretLabel: string;
  secretHint: string;
  fields: KindField[];
  setup: KindSetup;
  /** How recordings reach the CRM, for the switch's hint. */
  recordingHint: string;
  addHint: string;
}

const LINK_HINT =
  "Off by default. A link to the recording is written into the CRM record; anyone with the link can play it until it expires.";

export const KIND_SPECS: KindSpec[] = [
  {
    value: "hubspot",
    label: "HubSpot",
    auth: "oauth",
    defaultName: "HubSpot",
    clientIdLabel: "Client ID",
    secretLabel: "Client secret",
    secretHint: "Kept sealed; it is never shown again.",
    fields: [
      {
        key: "region",
        label: "HubSpot data centre",
        defaultValue: "na",
        options: [
          { value: "na", label: "United States / default" },
          { value: "eu", label: "Europe (app-eu1)" },
        ],
      },
    ],
    setup: {
      title: "HubSpot app settings",
      intro:
        "In your HubSpot developer account, create a private (unlisted) app and set these under Auth. HubSpot rejects a sign-in whose redirect URI differs by a character.",
      redirect: true,
      listTitle: "Scopes",
      list: [
        "crm.objects.contacts.read",
        "crm.objects.contacts.write",
        "crm.objects.owners.read",
      ],
    },
    recordingHint: LINK_HINT,
    addHint:
      "Add the HubSpot app you created in your own HubSpot developer account.",
  },
  {
    value: "pipedrive",
    label: "Pipedrive",
    auth: "oauth",
    defaultName: "Pipedrive",
    clientIdLabel: "Client ID",
    secretLabel: "Client secret",
    secretHint: "Kept sealed; it is never shown again.",
    fields: [],
    setup: {
      title: "Pipedrive app settings",
      intro:
        "In the Pipedrive developer hub create a private app, set the callback URL below and grant it these scopes. Calls are logged for each user by their Pipedrive user id, so one connection by an administrator serves everyone.",
      redirect: true,
      listTitle: "Scopes",
      list: [
        "Contacts: read",
        "Calls (activities): full access",
        "Users: read",
      ],
    },
    recordingHint:
      "Off by default. Pipedrive stores an audio file, not a link: the recording is uploaded to the call log (up to the upload limit, 50 MB by default).",
    addHint: "Add the private app you created in the Pipedrive developer hub.",
  },
  {
    value: "zoho",
    label: "Zoho CRM",
    auth: "oauth",
    defaultName: "Zoho CRM",
    clientIdLabel: "Client ID",
    secretLabel: "Client secret",
    secretHint: "Kept sealed; it is never shown again.",
    fields: [
      {
        key: "region",
        label: "Zoho data centre",
        defaultValue: "us",
        options: [
          { value: "us", label: "United States (zoho.com)" },
          { value: "eu", label: "Europe (zoho.eu)" },
          { value: "in", label: "India (zoho.in)" },
          { value: "au", label: "Australia (zoho.com.au)" },
          { value: "jp", label: "Japan (zoho.jp)" },
          { value: "ca", label: "Canada (zohocloud.ca)" },
          { value: "sa", label: "Saudi Arabia (zoho.sa)" },
          { value: "cn", label: "China (zoho.com.cn)" },
        ],
      },
    ],
    setup: {
      title: "Zoho API console client",
      intro:
        "In the Zoho API console (the data centre of your account) add a Server-based Application and set the redirect URI below. Zoho's own PhoneBridge dialling is not used: call from Zoho with the browser extension.",
      redirect: true,
      listTitle: "Scopes Hello asks for",
      list: [
        "ZohoCRM.modules.calls.CREATE",
        "ZohoCRM.modules.calls.UPDATE",
        "ZohoCRM.modules.contacts.READ",
        "ZohoCRM.modules.leads.READ",
        "ZohoCRM.users.READ",
      ],
    },
    recordingHint: LINK_HINT.replace("record;", "call's description;"),
    addHint: "Add the API console client you created in your Zoho account.",
  },
  {
    value: "odoo",
    label: "Odoo",
    auth: "credential",
    defaultName: "Odoo",
    clientIdLabel: "Login of the API key's user",
    secretLabel: "API key",
    secretHint: "Kept sealed; it is never shown again.",
    fields: [
      {
        key: "url",
        label: "Odoo address",
        placeholder: "https://odoo.example.com",
      },
      { key: "db", label: "Database", placeholder: "production" },
    ],
    setup: {
      title: "Odoo API key",
      intro:
        "In Odoo open the user's preferences, Account Security, New API Key, and paste the key here with the user's login and the database name. The user needs access to Contacts and to post notes. Hello logs a call as a note in the contact's chatter, or in the call owner's own, when the caller is unknown. Hello checks the key when you press Connect. Odoo 14 or later.",
    },
    recordingHint:
      "Off by default. A link to the recording is written into the call's note; anyone with the link can play it until it expires.",
    addHint: "Add the Odoo server and an API key.",
  },
  {
    value: "bitrix24",
    label: "Bitrix24",
    auth: "credential",
    defaultName: "Bitrix24",
    secretLabel: "Inbound webhook address",
    secretHint:
      "https://your-portal.bitrix24.com/rest/1/xxxxxxxx/ . The address contains the secret; it is kept sealed and never shown again.",
    fields: [],
    setup: {
      title: "Bitrix24 inbound webhook",
      intro:
        "In Bitrix24 open Developer resources, Other, Inbound webhook, give it the permissions below and paste its address here. Hello checks it when you press Connect. Calls appear in the CRM entity found by phone; the recording is fetched by Bitrix24 from a short-lived link and kept as its own copy.",
      listTitle: "Permissions",
      list: ["CRM (crm)", "Telephony (telephony)", "Users (user)"],
    },
    recordingHint:
      "Off by default. Bitrix24 fetches the recording once from a one-day link and keeps its own copy.",
    addHint: "Add a Bitrix24 inbound webhook.",
  },
];

export function kindSpec(kind: string): KindSpec {
  return KIND_SPECS.find((k) => k.value === kind) ?? KIND_SPECS[0]!;
}

/** The config to send: the client id when the kind has one, plus its fields. */
export function buildConfig(
  spec: KindSpec,
  clientId: string,
  values: Record<string, string>,
): Record<string, string> {
  const cfg: Record<string, string> = {};
  if (spec.clientIdLabel) cfg.clientId = clientId;
  for (const f of spec.fields)
    cfg[f.key] = values[f.key] ?? f.defaultValue ?? "";
  return cfg;
}
