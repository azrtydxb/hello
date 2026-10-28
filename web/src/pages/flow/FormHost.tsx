// The object's own form, opened from the canvas panel (spec S-15: the forms
// are shared with the form pages). The same drawer the page uses renders
// here; instead of saving, it hands its body to the draft (`commit`).

import { useEffect, useState } from "react";
import {
  errorMessage,
  listAnnouncements,
  listExtensions,
  listIvrMenus,
  listMOHClasses,
  listPagingGroups,
  listRingGroups,
  listTrunks,
  request,
  type Announcement,
  type Conference,
  type Extension,
  type InboundRoute,
  type IvrMenu,
  type MOHClass,
  type OutboundRoute,
  type PagingGroup,
  type Queue,
  type RingGroup,
  type Trunk,
} from "../../api";
import {
  listSchedules,
  listTimeConditions,
  type TimeCondition,
} from "../../api/timecond";
import type { FlowNode } from "../../api/flow";
import { Alert, Drawer, Spinner } from "../../design/azrty/components";
import { RoomDrawer } from "../../forms/ConferenceForm";
import { IvrDrawer } from "../../forms/IvrForm";
import { QueueDrawer } from "../../forms/QueueForm";
import { ConditionDrawer } from "../../forms/TimeConditionForm";
import { InboundDrawer, OutboundDrawer } from "../../forms/RouteForm";
import { RingGroupDrawer } from "../../forms/RingGroupForm";
import { OBJECTS, idOf } from "./objects";
import type { Body } from "./draft";

interface Lists {
  extensions: Extension[];
  ringGroups: RingGroup[];
  announcements: Announcement[];
  pagingGroups: PagingGroup[];
  menus: IvrMenu[];
  mohClasses: MOHClass[];
  timeConditions: TimeCondition[];
  schedules: { id: number | string; name: string }[];
  trunks: Trunk[];
  ready: boolean;
}

const EMPTY: Lists = {
  extensions: [],
  ringGroups: [],
  announcements: [],
  pagingGroups: [],
  menus: [],
  mohClasses: [],
  timeConditions: [],
  schedules: [],
  trunks: [],
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
      soft(listMOHClasses(ctl.signal)),
      soft(listTimeConditions(ctl.signal)),
      soft(listSchedules(ctl.signal)),
      soft(listTrunks(ctl.signal)),
    ]).then(
      ([
        extensions,
        ringGroups,
        announcements,
        pagingGroups,
        menus,
        mohClasses,
        timeConditions,
        schedules,
        trunks,
      ]) => {
        if (ctl.signal.aborted) return;
        setLists({
          extensions,
          ringGroups,
          announcements,
          pagingGroups,
          menus,
          mohClasses,
          timeConditions,
          schedules,
          trunks,
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
  draftBody,
  onCommit,
  onClose,
}: {
  node: FlowNode;
  /** What the draft already changes on this object. */
  draftBody: Body | undefined;
  onCommit: (body: Body) => Promise<void>;
  onClose: () => void;
}) {
  const lists = useLists();
  const [obj, setObj] = useState<Body | null>(null);
  const [error, setError] = useState("");
  const read = OBJECTS[node.kind]?.read;

  useEffect(() => {
    const ctl = new AbortController();
    if (!read) return;
    request<Body>("GET", read(idOf(node.id)), { signal: ctl.signal })
      .then((o) => setObj(o))
      .catch((e: unknown) => {
        if (!ctl.signal.aborted) setError(errorMessage(e));
      });
    return () => ctl.abort();
  }, [node.id, read]);

  if (error || !obj || !lists.ready) {
    return (
      <Drawer
        title={node.label}
        description="Loading the object"
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
  // The object as the draft leaves it.
  const current = { ...obj, ...draftBody };
  const commit = (body: object) => onCommit(body as Body);
  const common = { onClose, commit, onSaved: () => undefined };
  const trunks = { items: lists.trunks, ready: lists.ready };
  const dest = {
    extensions: lists.extensions,
    ringGroups: lists.ringGroups,
    announcements: lists.announcements,
    pagingGroups: lists.pagingGroups,
    menus: lists.menus,
    timeConditions: lists.timeConditions,
  };
  switch (node.kind) {
    case "ringGroup":
      return (
        <RingGroupDrawer
          {...common}
          group={current as unknown as RingGroup}
          extensions={lists.extensions}
          extReady
        />
      );
    case "ivr":
      return (
        <IvrDrawer
          {...common}
          menu={current as unknown as IvrMenu}
          choices={dest}
        />
      );
    case "queue":
      return (
        <QueueDrawer
          {...common}
          queue={current as unknown as Queue}
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
          item={current as unknown as TimeCondition}
          choices={dest}
          schedules={lists.schedules}
        />
      );
    case "conference":
      return <RoomDrawer {...common} room={current as unknown as Conference} />;
    case "inboundRoute":
      return (
        <InboundDrawer
          {...common}
          route={current as unknown as InboundRoute}
          trunks={trunks}
        />
      );
    case "outboundRoute":
      return (
        <OutboundDrawer
          {...common}
          route={current as unknown as OutboundRoute}
          trunks={trunks}
        />
      );
  }
  return null;
}
