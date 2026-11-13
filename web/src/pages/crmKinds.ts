// The CRM connector kinds the console can create: what each one asks for and
// how its sign-in works. The server validates every value again; this only
// shapes the form.

export interface KindOption {
  value: string;
  label: string;
}

export interface KindField {
  /** The key in the connector's config. */
  key: string;
  label: string;
  hint?: string;
  required?: boolean;
  /** A fixed list; the field is a select. */
  options?: KindOption[];
  /** The value a new connector starts with. */
  initial?: string;
}

export interface KindSpec {
  value: string;
  label: string;
  /** oauth: the administrator approves the app at the CRM; direct: Hello signs in with the typed credentials. */
  auth: "oauth" | "direct";
  /** The label of config.clientId; absent when the kind has none. */
  idLabel?: string;
  idHint?: string;
  secretLabel: string;
  secretHint?: string;
  fields: KindField[];
  /** Config keys whose change asks for the secret again (besides clientId). */
  bound: string[];
  /** Ticket kinds can download a sidebar app. */
  sidebarApp?: boolean;
  guide: { summary: string; text: string; scopes?: string[]; steps?: string[] };
  /** Label of the fallback owner field. */
  ownerLabel: string;
}

const HUBSPOT_SCOPES = [
  "crm.objects.contacts.read",
  "crm.objects.contacts.write",
  "crm.objects.owners.read",
];

export const KINDS: KindSpec[] = [
  {
    value: "hubspot",
    label: "HubSpot",
    auth: "oauth",
    idLabel: "Client ID",
    secretLabel: "Client secret",
    secretHint: "Kept sealed; it is never shown again.",
    fields: [
      {
        key: "region",
        label: "HubSpot data centre",
        initial: "na",
        options: [
          { value: "na", label: "United States / default" },
          { value: "eu", label: "Europe (app-eu1)" },
        ],
      },
    ],
    bound: [],
    ownerLabel: "Fallback owner (CRM user id)",
    guide: {
      summary: "HubSpot app settings",
      text: "In your HubSpot developer account, create a private (unlisted) app and set these under Auth. HubSpot rejects a sign-in whose redirect URI differs by a character.",
      scopes: HUBSPOT_SCOPES,
    },
  },
  {
    value: "zendesk",
    label: "Zendesk",
    auth: "direct",
    idLabel: "Agent email",
    idHint: "The agent the API token belongs to.",
    secretLabel: "API token",
    secretHint: "Kept sealed; it is never shown again.",
    fields: [
      {
        key: "subdomain",
        label: "Zendesk subdomain",
        hint: "acme for acme.zendesk.com",
        required: true,
      },
      {
        key: "createContacts",
        label: "Unknown callers",
        initial: "true",
        options: [
          { value: "true", label: "Create a Zendesk user for them" },
          { value: "false", label: "Skip the call" },
        ],
      },
      {
        key: "answeredStatus",
        label: "Ticket status of an answered call",
        initial: "open",
        options: [
          { value: "open", label: "Open" },
          { value: "pending", label: "Pending" },
          { value: "hold", label: "On hold" },
          { value: "solved", label: "Solved" },
        ],
      },
    ],
    bound: ["subdomain"],
    sidebarApp: true,
    ownerLabel: "Fallback assignee (Zendesk agent id)",
    guide: {
      summary: "Zendesk setup",
      text: "Every call becomes an internal ticket; a missed call is a new ticket. Hello signs in with an API token, so no browser step is needed.",
      steps: [
        "Admin Center, Apps and integrations, APIs, Zendesk API: switch on Token access and add an API token.",
        "Use the email of an agent who may create tickets and users.",
        "Optional: download the sidebar app below and upload it under Admin Center, Apps and integrations, Zendesk Support apps, Upload private app.",
      ],
    },
  },
  {
    value: "freshdesk",
    label: "Freshdesk",
    auth: "direct",
    secretLabel: "API key",
    secretHint: "Kept sealed; it is never shown again.",
    fields: [
      {
        key: "subdomain",
        label: "Freshdesk subdomain",
        hint: "acme for acme.freshdesk.com",
        required: true,
      },
      {
        key: "answeredStatus",
        label: "Ticket status of an answered call",
        initial: "open",
        options: [
          { value: "open", label: "Open" },
          { value: "pending", label: "Pending" },
          { value: "resolved", label: "Resolved" },
        ],
      },
    ],
    bound: ["subdomain"],
    sidebarApp: true,
    ownerLabel: "Fallback responder (Freshdesk agent id)",
    guide: {
      summary: "Freshdesk setup",
      text: "Every call becomes a phone ticket; Freshdesk creates the contact of an unknown caller itself. Hello signs in with an API key.",
      steps: [
        "Profile settings in Freshdesk shows the API key of the agent Hello should act as.",
        "Optional: download the sidebar app below, unzip it and publish it with the Freshworks CLI (fdk pack), then upload it under Admin, Apps, Custom apps.",
      ],
    },
  },
  {
    value: "connectwise",
    label: "ConnectWise Manage",
    auth: "direct",
    idLabel: "Integrator client ID",
    idHint: "The clientId from developer.connectwise.com.",
    secretLabel: "Private key",
    secretHint: "Kept sealed; it is never shown again.",
    fields: [
      {
        key: "host",
        label: "API host",
        hint: "api-na.myconnectwise.net, api-eu..., api-au... or your own",
        required: true,
        initial: "api-na.myconnectwise.net",
      },
      { key: "companyId", label: "Company ID", required: true },
      { key: "publicKey", label: "Public key", required: true },
      {
        key: "codebase",
        label: "Release path",
        initial: "v4_6_release",
      },
      {
        key: "activityType",
        label: "Activity type",
        hint: "The name of the activity type a call gets.",
        initial: "Call",
      },
    ],
    bound: ["host", "companyId", "publicKey", "codebase"],
    ownerLabel: "Fallback member (ConnectWise member id)",
    guide: {
      summary: "ConnectWise Manage setup",
      text: "Every call becomes an activity of the contact's company, assigned to the member who took it. A call from a number that is no contact is skipped.",
      steps: [
        "System, Members, API Members: create an API member and an API key; note the public and private key.",
        "Give its role access to Sales activities, Company contacts and System members.",
        "Register an integrator at developer.connectwise.com for the client ID.",
      ],
    },
  },
  {
    value: "autotask",
    label: "Autotask PSA",
    auth: "direct",
    idLabel: "API user name",
    secretLabel: "API secret",
    secretHint: "Kept sealed; it is never shown again.",
    fields: [
      { key: "integrationCode", label: "Integration code", required: true },
      {
        key: "host",
        label: "Zone host",
        hint: "Optional, for example webservices5.autotask.net; found from the API user when empty.",
      },
      {
        key: "actionType",
        label: "Note action type id",
        hint: "Optional; found from the pick-list entry labelled Call when empty.",
      },
    ],
    bound: ["host", "integrationCode"],
    ownerLabel: "Fallback resource (Autotask resource id)",
    guide: {
      summary: "Autotask setup",
      text: "Every call becomes a company note on the contact's company, assigned to the resource who took it. A call from a number that is no contact is skipped.",
      steps: [
        "Admin, Resources (Users): create an API user and give it an integration vendor / tracking identifier.",
        "Its security level needs access to Contacts, Companies and Company notes.",
      ],
    },
  },
  {
    value: "halopsa",
    label: "HaloPSA",
    auth: "direct",
    idLabel: "Client ID",
    secretLabel: "Client secret",
    secretHint: "Kept sealed; it is never shown again.",
    fields: [
      {
        key: "host",
        label: "Halo address",
        hint: "acme.halopsa.com",
        required: true,
      },
      {
        key: "tenant",
        label: "Tenant",
        hint: "Only for a hosted instance that asks for it.",
      },
      { key: "ticketTypeId", label: "Ticket type id", hint: "Optional." },
      {
        key: "fallbackClientId",
        label: "Client id for unknown callers",
        hint: "Optional; without it a call from an unknown number is skipped.",
      },
    ],
    bound: ["host", "tenant"],
    sidebarApp: false,
    ownerLabel: "Fallback agent (Halo agent id)",
    guide: {
      summary: "HaloPSA setup",
      text: "Every call becomes a ticket for the caller's client. Hello uses an API application with the client-credentials grant.",
      steps: [
        "Configuration, Integrations, HaloPSA API: create an application with the Client ID and Secret (Services) login type.",
        "Give its agent permission to create tickets and read users and clients.",
      ],
    },
  },
  {
    value: "clio",
    label: "Clio Manage",
    auth: "oauth",
    idLabel: "Key",
    idHint: "The application key from Clio's developer portal.",
    secretLabel: "Secret",
    secretHint: "Kept sealed; it is never shown again.",
    fields: [
      {
        key: "region",
        label: "Clio region",
        initial: "us",
        options: [
          { value: "us", label: "United States (app.clio.com)" },
          { value: "eu", label: "Europe (eu.app.clio.com)" },
          { value: "ca", label: "Canada (ca.app.clio.com)" },
          { value: "au", label: "Australia (au.app.clio.com)" },
        ],
      },
    ],
    bound: ["region"],
    ownerLabel: "Fallback user (Clio user id)",
    guide: {
      summary: "Clio application settings",
      text: "Every call becomes a phone communication between the contact and the Clio user. A call from a number that is no contact is skipped.",
      steps: [
        "In Clio's developer portal create an application and set the redirect URI below.",
        "Grant it read access to contacts and users and write access to communications.",
      ],
    },
  },
  {
    value: "pipedrive",
    label: "Pipedrive",
    auth: "oauth",
    idLabel: "Client ID",
    secretLabel: "Client secret",
    secretHint: "Kept sealed; it is never shown again.",
    fields: [],
    bound: [],
    ownerLabel: "Fallback owner (Pipedrive user id)",
    guide: {
      summary: "Pipedrive app settings",
      text: "In the Pipedrive developer hub create a private app and set the callback URL below. Calls are logged for each user by their Pipedrive user id, so one connection by an administrator serves everyone. Pipedrive stores an audio file, not a link: with recordings on, the audio is uploaded to the call log (up to HELLO_CRM_UPLOAD_MAX_MB, 50 MB by default).",
      scopes: [
        "Contacts: read",
        "Calls (activities): full access",
        "Users: read",
      ],
    },
  },
  {
    value: "zoho",
    label: "Zoho CRM",
    auth: "oauth",
    idLabel: "Client ID",
    secretLabel: "Client secret",
    secretHint: "Kept sealed; it is never shown again.",
    fields: [
      {
        key: "region",
        label: "Zoho data centre",
        initial: "us",
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
    bound: ["region"],
    ownerLabel: "Fallback owner (Zoho user id)",
    guide: {
      summary: "Zoho API console client",
      text: "In the Zoho API console (the data centre of your account) add a Server-based Application and set the redirect URI below. Zoho's own PhoneBridge dialling is not used: call from Zoho with the browser extension.",
      scopes: [
        "ZohoCRM.modules.calls.CREATE",
        "ZohoCRM.modules.calls.UPDATE",
        "ZohoCRM.modules.contacts.READ",
        "ZohoCRM.modules.leads.READ",
        "ZohoCRM.users.READ",
      ],
    },
  },
  {
    value: "odoo",
    label: "Odoo",
    auth: "direct",
    idLabel: "Login of the API key's user",
    secretLabel: "API key",
    secretHint: "Kept sealed; it is never shown again.",
    fields: [
      {
        key: "url",
        label: "Odoo address",
        hint: "https://odoo.example.com",
        required: true,
      },
      { key: "db", label: "Database", required: true },
    ],
    bound: ["url", "db"],
    ownerLabel: "Fallback owner (Odoo user id)",
    guide: {
      summary: "Odoo API key",
      text: "Hello logs a call as a note in the contact's chatter, or in the call owner's own when the caller is unknown. Hello checks the key when you press Connect. Odoo 14 or later.",
      steps: [
        "In Odoo open the user's preferences, Account Security, New API Key.",
        "The user needs access to Contacts and the right to post notes.",
      ],
    },
  },
  {
    value: "bitrix24",
    label: "Bitrix24",
    auth: "direct",
    secretLabel: "Inbound webhook address",
    secretHint:
      "https://your-portal.bitrix24.com/rest/1/xxxxxxxx/ . The address contains the secret; it is kept sealed and never shown again.",
    fields: [],
    bound: [],
    ownerLabel: "Fallback owner (Bitrix24 user id)",
    guide: {
      summary: "Bitrix24 inbound webhook",
      text: "Calls appear in the CRM entity found by phone. The recording is fetched by Bitrix24 from a one-day link and kept as its own copy.",
      steps: [
        "Developer resources, Other, Inbound webhook: give it the permissions CRM, Telephony and Users.",
        "Paste its address here as the secret; Hello checks it when you press Connect.",
      ],
    },
  },
];

export function kindSpec(kind: string): KindSpec {
  return KINDS.find((k) => k.value === kind) ?? KINDS[0]!;
}

/** The config a form sends: the client id (when the kind has one) and the kind's own fields. */
export function buildConfig(
  spec: KindSpec,
  clientId: string,
  values: Record<string, string>,
): Record<string, string> {
  const cfg: Record<string, string> = {};
  if (spec.idLabel) cfg.clientId = clientId;
  for (const f of spec.fields) cfg[f.key] = values[f.key] ?? f.initial ?? "";
  return cfg;
}

export function initialValues(spec: KindSpec): Record<string, string> {
  const v: Record<string, string> = {};
  for (const f of spec.fields) v[f.key] = f.initial ?? "";
  return v;
}
