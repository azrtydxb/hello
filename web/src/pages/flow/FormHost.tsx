// The object's own form, opened from the canvas (spec S-15: the forms are
// shared with the form pages). The same drawer the page uses renders here,
// for an existing object (every field of it) or a new one. Instead of saving,
// it hands its body to the draft (`onCommit`).
//
// What a batch cannot carry is done by the form at once, exactly as its page
// does it, and the canvas reloads afterwards (`onDone`): a new device or
// phone (the answer is a SIP secret or a provisioning URL, shown once here)
// and an announcement's audio, text prompt and deletion.

import { useEffect, useState } from "react";
import {
  deleteAnnouncement,
  errorMessage,
  listAnnouncements,
  listConferences,
  listDevices,
  listExtensions,
  listIvrMenus,
  listMOHClasses,
  listPagingGroups,
  listRingGroups,
  listTrunks,
  request,
  type Announcement,
  type Conference,
  type Device,
  type Extension,
  type FeatureCode,
  type InboundRoute,
  type IvrMenu,
  type MOHClass,
  type OutboundRoute,
  type PagingGroup,
  type ParkLot,
  type PickupGroup,
  type Queue,
  type RingGroup,
  type Trunk,
} from "../../api";
import type { IssuedDevice } from "../../api/directory";
import type { FaxDestination } from "../../api/fax";
import type { FlowNode } from "../../api/flow";
import {
  listGroups,
  listMulticastChannels,
  type CatalogChannel,
  type Group,
} from "../../api/phonebook";
import {
  listTemplates,
  type IssuedPhone,
  type Phone,
  type Template,
} from "../../api/prov";
import {
  listSchedules,
  listTimeConditions,
  type Calendar,
  type HoursSchedule,
  type TimeCondition,
} from "../../api/timecond";
import { listVoicemailGroups, type VoicemailGroup } from "../../api/vmgroups";
import {
  Alert,
  Button,
  ConfirmDialog,
  Drawer,
  Spinner,
  useToast,
} from "../../design/azrty/components";
import { RoomDrawer } from "../../forms/ConferenceForm";
import { DeviceDrawer } from "../../forms/DeviceForm";
import { ExtensionDrawer, NewExtension } from "../../forms/ExtensionForm";
import { IvrDrawer } from "../../forms/IvrForm";
import { PromptEditor } from "../../forms/PromptForm";
import { QueueDrawer } from "../../forms/QueueForm";
import { RingGroupDrawer } from "../../forms/RingGroupForm";
import { InboundDrawer, OutboundDrawer } from "../../forms/RouteForm";
import { ConditionDrawer } from "../../forms/TimeConditionForm";
import { ReplaceAudio, UploadCard } from "../Announcements";
import { issuedDevice, NewDevice, SecretModal, type Issued } from "../Devices";
import { DestinationForm } from "../Faxes";
import { ClassDrawer } from "../MusicOnHold";
import { PagingDrawer } from "../Paging";
import { ChannelModal } from "../paging/MulticastChannels";
import { ParkLotDrawer, PickupDrawer } from "../ParkPickup";
import { issuedView, IssuedUrlModal, PhoneForm } from "../Phones";
import { FeatureCodes } from "../System";
import { CalendarDrawer } from "../timecond/Calendars";
import { ScheduleDrawer } from "../timecond/Schedules";
import { TrunkDrawer } from "../Trunks";
import { GroupDrawer } from "../VoicemailGroups";
import type { Body } from "./draft";
import { OBJECTS, idOf } from "./objects";

interface Lists {
  extensions: Extension[];
  ringGroups: RingGroup[];
  announcements: Announcement[];
  pagingGroups: PagingGroup[];
  menus: IvrMenu[];
  voicemailGroups: VoicemailGroup[];
  mohClasses: MOHClass[];
  timeConditions: TimeCondition[];
  schedules: { id: number | string; name: string }[];
  trunks: Trunk[];
  devices: Device[];
  channels: CatalogChannel[];
  rooms: Conference[];
  templates: Template[];
  groups: Group[];
  ready: boolean;
}

const EMPTY: Lists = {
  extensions: [],
  ringGroups: [],
  announcements: [],
  pagingGroups: [],
  menus: [],
  voicemailGroups: [],
  mohClasses: [],
  timeConditions: [],
  schedules: [],
  trunks: [],
  devices: [],
  channels: [],
  rooms: [],
  templates: [],
  groups: [],
  ready: false,
};

/** Every list the forms' pickers choose from; a list that fails stays empty. */
function useLists(): Lists {
  const [lists, setLists] = useState<Lists>(EMPTY);
  useEffect(() => {
    const ctl = new AbortController();
    const soft = <T,>(p: Promise<T[]>): Promise<T[]> =>
      p.catch(() => [] as T[]);
    void Promise.all([
      soft(listExtensions(ctl.signal)),
      soft(listRingGroups(ctl.signal)),
      soft(listAnnouncements(ctl.signal)),
      soft(listPagingGroups(ctl.signal)),
      soft(listIvrMenus(ctl.signal)),
      soft(listVoicemailGroups(ctl.signal)),
      soft(listMOHClasses(ctl.signal)),
      soft(listTimeConditions(ctl.signal)),
      soft(listSchedules(ctl.signal)),
      soft(listTrunks(ctl.signal)),
      soft(listDevices(ctl.signal)),
      soft(listMulticastChannels(ctl.signal)),
      soft(listConferences(ctl.signal)),
      soft(listTemplates(ctl.signal)),
      soft(listGroups(ctl.signal)),
    ]).then(
      ([
        extensions,
        ringGroups,
        announcements,
        pagingGroups,
        menus,
        voicemailGroups,
        mohClasses,
        timeConditions,
        schedules,
        trunks,
        devices,
        channels,
        rooms,
        templates,
        groups,
      ]) => {
        if (ctl.signal.aborted) return;
        setLists({
          extensions,
          ringGroups,
          announcements,
          pagingGroups,
          menus,
          voicemailGroups,
          mohClasses,
          timeConditions,
          schedules,
          trunks,
          devices,
          channels,
          rooms,
          templates,
          groups,
          ready: true,
        });
      },
    );
    return () => ctl.abort();
  }, []);
  return lists;
}

export function FormHost({
  node,
  kind = node?.kind ?? "",
  draftBody,
  onCommit,
  onDone,
  onClose,
}: {
  /** The object to edit; null opens the form for a new one of `kind`. */
  node: FlowNode | null;
  kind?: string;
  /** What the draft already changes on this object. */
  draftBody: Body | undefined;
  /** Stages the form's body: an edit of `node`, or a new object. */
  onCommit: (body: Body) => Promise<void>;
  /**
   * After a write the form made at once (not in the draft): the node it
   * made or changed (null when it deleted it), and what to tell the user.
   */
  onDone: (node: string | null, message: string) => void | Promise<void>;
  onClose: () => void;
}) {
  const lists = useLists();
  const [obj, setObj] = useState<Body | null>(null);
  const [error, setError] = useState("");
  const def = OBJECTS[kind];
  const read = node ? def?.read : undefined;

  useEffect(() => {
    const ctl = new AbortController();
    if (!node || !read) return;
    request<Body>("GET", read(idOf(node.id)), { signal: ctl.signal })
      .then((o) => setObj(o))
      .catch((e: unknown) => {
        if (!ctl.signal.aborted) setError(errorMessage(e));
      });
    return () => ctl.abort();
  }, [node, read]);

  if (error || (node && !obj) || !lists.ready) {
    return (
      <Drawer
        title={node?.label ?? "New object"}
        description="Loading the form"
        onClose={onClose}
        width={560}
      >
        {error ? (
          <Alert tone="bad" title="Could not read the object">
            {error}
          </Alert>
        ) : (
          <Spinner label="Loading the form" />
        )}
      </Drawer>
    );
  }
  // The object as the draft leaves it, in the shape its GET answers; null
  // for a new one.
  const view = def?.toView ?? ((b: Body) => b);
  const current: Body | null = obj
    ? { ...obj, ...(draftBody ? view(draftBody) : {}) }
    : null;
  const as = <T,>(): T | null => current as unknown as T | null;
  const commit = (body: object) => onCommit(body as Body);
  const common = { onClose, commit, onSaved: () => undefined };
  const trunks = { items: lists.trunks, ready: lists.ready };
  const dest = {
    extensions: lists.extensions,
    ringGroups: lists.ringGroups,
    announcements: lists.announcements,
    pagingGroups: lists.pagingGroups,
    menus: lists.menus,
    voicemailGroups: lists.voicemailGroups,
    timeConditions: lists.timeConditions,
  };
  switch (kind) {
    case "extension": {
      const ext = as<Extension>();
      return ext ? (
        <ExtensionDrawer
          ext={ext}
          devices={lists.devices.filter(
            (d) => String(d.extensionId) === String(ext.id),
          )}
          extensions={lists.extensions}
          live={{}}
          onClose={onClose}
          onSaved={() => undefined}
          onDeleted={() => undefined}
          commit={commit}
        />
      ) : (
        <NewExtension
          onClose={onClose}
          onCreated={() => undefined}
          commit={commit}
        />
      );
    }
    case "device": {
      const device = as<Device>();
      return device ? (
        <DeviceDrawer
          device={device}
          extensions={lists.extensions}
          onClose={onClose}
          commit={commit}
        />
      ) : (
        <NewDeviceHost
          extensions={lists.extensions}
          onClose={onClose}
          onDone={onDone}
        />
      );
    }
    case "phone": {
      const phone = as<Phone>();
      const shared = {
        extensions: lists.extensions,
        devices: lists.devices,
        templates: lists.templates,
        groups: lists.groups,
        rooms: lists.rooms,
      };
      return phone ? (
        <PhoneForm
          {...shared}
          phone={phone}
          onClose={onClose}
          onCreated={() => undefined}
          onUpdated={() => undefined}
          commit={commit}
        />
      ) : (
        <NewPhoneHost {...shared} onClose={onClose} onDone={onDone} />
      );
    }
    case "trunk":
      return <TrunkDrawer {...common} trunk={as<Trunk>()} />;
    case "ringGroup":
      return (
        <RingGroupDrawer
          {...common}
          group={as<RingGroup>()}
          extensions={lists.extensions}
          extReady
        />
      );
    case "ivr":
      return <IvrDrawer {...common} menu={as<IvrMenu>()} choices={dest} />;
    case "queue":
      return (
        <QueueDrawer
          {...common}
          queue={as<Queue>()}
          choices={{
            ...dest,
            all: lists.extensions,
            mohClasses: lists.mohClasses,
          }}
        />
      );
    case "timeCondition":
      return (
        <ConditionDrawer
          {...common}
          item={as<TimeCondition>()}
          choices={dest}
          schedules={lists.schedules}
        />
      );
    case "schedule":
      return <ScheduleDrawer {...common} item={as<HoursSchedule>()} />;
    case "holidayCalendar":
      return (
        <CalendarDrawer
          {...common}
          item={as<Calendar>()}
          onImported={() => undefined}
        />
      );
    case "mohClass":
      return (
        <ClassDrawer
          {...common}
          cls={as<MOHClass>()}
          announcements={lists.announcements}
          annReady
        />
      );
    case "conference":
      return <RoomDrawer {...common} room={as<Conference>()} />;
    case "inboundRoute":
      return (
        <InboundDrawer {...common} route={as<InboundRoute>()} trunks={trunks} />
      );
    case "outboundRoute":
      return (
        <OutboundDrawer
          {...common}
          route={as<OutboundRoute>()}
          trunks={trunks}
        />
      );
    case "parkLot":
      return <ParkLotDrawer {...common} lot={as<ParkLot>()} />;
    case "pickupGroup":
      return (
        <PickupDrawer
          {...common}
          group={as<PickupGroup>()}
          extensions={lists.extensions}
          extReady
        />
      );
    case "multicastChannel":
      return <ChannelModal {...common} channel={as<CatalogChannel>()} />;
    case "pagingGroup":
      return (
        <PagingDrawer
          {...common}
          group={as<PagingGroup>()}
          extensions={lists.extensions}
          extReady
          channels={lists.channels}
        />
      );
    case "voicemailGroup":
      return (
        <GroupDrawer
          {...common}
          group={as<VoicemailGroup>()}
          extensions={lists.extensions}
          extReady
        />
      );
    case "fax": {
      const destination = as<FaxDestination>();
      return (
        <Drawer
          title={
            destination ? `Fax ${destination.name}` : "New fax destination"
          }
          description="A named inbox: a route or an IVR key sends a fax call to it."
          onClose={onClose}
          width={560}
        >
          <DestinationForm
            key={destination ? String(destination.id) : "new"}
            destination={destination}
            commit={commit}
            onCreated={onClose}
          />
        </Drawer>
      );
    }
    case "featureCode": {
      const items = (current?.items ?? draftBody?.items) as
        FeatureCode[] | undefined;
      return (
        <Drawer
          title="Feature codes"
          description="The codes are one list: the draft replaces it as a whole when you apply."
          onClose={onClose}
          width={720}
        >
          <FeatureCodes
            initial={items}
            adding={node === null}
            commit={commit}
            onSaved={onClose}
          />
        </Drawer>
      );
    }
    case "announcement":
      return (
        <AnnouncementHost
          announcement={as<Announcement>()}
          onClose={onClose}
          onDone={onDone}
        />
      );
  }
  return (
    <Drawer title={node?.label ?? kind} onClose={onClose} width={480}>
      <Alert tone="info" title="No form">
        This is not a configurable object: it has no form.
      </Alert>
    </Drawer>
  );
}

/** A new device: created at once, its secret shown once, then the canvas reloads. */
function NewDeviceHost({
  extensions,
  onClose,
  onDone,
}: {
  extensions: Extension[];
  onClose: () => void;
  onDone: (node: string | null, message: string) => void | Promise<void>;
}) {
  // Component state only: gone when the dialog closes.
  const [issued, setIssued] = useState<{
    id: string;
    view: Issued;
  } | null>(null);
  const toast = useToast();
  if (issued) {
    return (
      <>
        <SecretModal
          issued={issued.view}
          onCopied={toast.show}
          onClose={() => {
            const id = issued.id;
            setIssued(null);
            void onDone(id, "Device created.");
          }}
        />
        {toast.node}
      </>
    );
  }
  return (
    <NewDevice
      extensions={extensions}
      onClose={onClose}
      onCreated={(created: IssuedDevice) =>
        setIssued({
          id: `device:${String(created.id)}`,
          view: issuedDevice(created),
        })
      }
    />
  );
}

/** A new phone: added at once, its provisioning URL shown once, then the canvas reloads. */
function NewPhoneHost({
  onClose,
  onDone,
  ...lists
}: {
  extensions: Extension[];
  devices: Device[];
  templates: Template[];
  groups: Group[];
  rooms: Conference[];
  onClose: () => void;
  onDone: (node: string | null, message: string) => void | Promise<void>;
}) {
  const [issued, setIssued] = useState<{
    id: string;
    created: IssuedPhone;
  } | null>(null);
  const toast = useToast();
  if (issued) {
    return (
      <>
        <IssuedUrlModal
          issued={issuedView("create", issued.created)}
          onCopied={toast.show}
          onClose={() => {
            const id = issued.id;
            setIssued(null);
            void onDone(id, "Phone added.");
          }}
        />
        {toast.node}
      </>
    );
  }
  return (
    <PhoneForm
      {...lists}
      phone={null}
      onClose={onClose}
      onCreated={(created) =>
        setIssued({ id: `phone:${String(created.id)}`, created })
      }
      onUpdated={() => undefined}
    />
  );
}

/**
 * An announcement: its audio (an upload, or speech made from text) and its
 * deletion are saved at once, as on the Announcements page; a batch cannot
 * roll back object storage or a speech provider.
 */
function AnnouncementHost({
  announcement,
  onClose,
  onDone,
}: {
  announcement: Announcement | null;
  onClose: () => void;
  onDone: (node: string | null, message: string) => void | Promise<void>;
}) {
  const [mode, setMode] = useState<"menu" | "prompt" | "replace" | "delete">(
    "menu",
  );
  const nodeOf = (a: Announcement) => `announcement:${String(a.id)}`;
  if (mode === "prompt") {
    return (
      <PromptEditor
        announcement={announcement}
        onClose={() => setMode("menu")}
        onDone={(a, message) => void onDone(nodeOf(a), message)}
      />
    );
  }
  if (announcement && mode === "replace") {
    return (
      <ReplaceAudio
        announcement={announcement}
        onClose={() => setMode("menu")}
        onReplaced={(a) =>
          void onDone(nodeOf(a), `The audio of ${a.name} was replaced.`)
        }
      />
    );
  }
  if (announcement && mode === "delete") {
    return (
      <ConfirmDialog
        title={`Delete announcement ${announcement.name}?`}
        description="It is deleted at once, not in the draft. An announcement that a route, menu, queue or class still uses cannot be deleted."
        confirmLabel="Delete announcement"
        errorTitle="Could not delete"
        onConfirm={async () => {
          await deleteAnnouncement(announcement.id);
          await onDone(null, `Announcement ${announcement.name} deleted.`);
        }}
        onClose={() => setMode("menu")}
      />
    );
  }
  return (
    <Drawer
      title={
        announcement ? `Announcement ${announcement.name}` : "New announcement"
      }
      description="Audio is saved at once, not in the draft: object storage and speech cannot be rolled back."
      onClose={onClose}
      width={520}
    >
      {announcement ? (
        <div className="flow-panel__section">
          <Button
            size="sm"
            variant="secondary"
            icon="replace"
            onClick={() => setMode("replace")}
          >
            Replace the audio
          </Button>
          <Button
            size="sm"
            variant="secondary"
            icon="wand-sparkles"
            onClick={() => setMode("prompt")}
          >
            {announcement.generated
              ? "Edit the text prompt"
              : "Make it from text"}
          </Button>
          <Button
            size="sm"
            variant="danger"
            icon="trash-2"
            onClick={() => setMode("delete")}
          >
            Delete announcement
          </Button>
        </div>
      ) : (
        <>
          <UploadCard
            onUploaded={(a) =>
              void onDone(nodeOf(a), `Announcement ${a.name} uploaded.`)
            }
          />
          <div className="flow-panel__section">
            <Button
              size="sm"
              variant="secondary"
              icon="wand-sparkles"
              onClick={() => setMode("prompt")}
            >
              Make it from text instead
            </Button>
          </div>
        </>
      )}
    </Drawer>
  );
}
