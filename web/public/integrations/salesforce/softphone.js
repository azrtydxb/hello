/* Hello's Salesforce Open CTI softphone (spec integrations I-51).
 *
 * Salesforce frames this page from the Call Center's adapter URL and appends
 * sfdcIFrameOrigin; the page loads Open CTI from that origin, so it needs no
 * Salesforce token. The phone itself is a desk phone: this page only places
 * the call through Hello (the user's own phone rings first), pops the caller's
 * record on an incoming call, and logs the call as a Task with saveLog.
 */
(function (root) {
  "use strict";

  var SF_HOSTS = [
    "salesforce.com",
    "force.com",
    "cloudforce.com",
    "salesforce-setup.com",
  ];
  var OPENCTI_VERSION = "62.0";
  var CALL_ID_MAX = 128;

  /** The Salesforce origin the page may load Open CTI from, or "". */
  function sfOrigin(search, HelloEmbed) {
    var o = new URLSearchParams(search).get("sfdcIFrameOrigin") || "";
    return HelloEmbed.originAllowed(o, SF_HOSTS) ? o : "";
  }

  function whoField(id) {
    // Contacts and leads are who a call was with; anything else is what it is about.
    return /^(003|00Q)/.test(id || "")
      ? "WhoId"
      : /^[a-zA-Z0-9]{15,18}$/.test(id || "")
        ? "WhatId"
        : "";
  }

  function whoFromSearch(res) {
    var rv = res && res.returnValue;
    if (!rv) return "";
    var ids = Object.keys(rv).filter(function (k) {
      return /^(003|00Q)[a-zA-Z0-9]{12,15}$/.test(k);
    });
    return ids.length === 1 ? ids[0] : "";
  }

  function disposition(ev) {
    var d = ev.data || {};
    var r = String(d.disposition || d.reason || "").toLowerCase();
    if (r === "answered") return "Connected";
    if (r === "busy") return "Busy";
    if (r === "cancelled") return "Cancelled";
    if (r === "" || r === "no_answer" || r === "dnd" || r === "unreachable")
      return d.answeredAt ? "Connected" : "No answer";
    return "Failed";
  }

  function seconds(d) {
    if (d.answeredAt && d.endedAt) {
      var s = (Date.parse(d.endedAt) - Date.parse(d.answeredAt)) / 1000;
      if (s > 0) return Math.round(s);
    }
    return Math.round((d.billableMs || 0) / 1000);
  }

  /**
   * controller wires Open CTI, Hello's client and the page together.
   * deps: {sforce, hello, HelloEmbed, logMissed, ui}. ui has setCall(text,
   * tone), setLive(text) and showError(text).
   */
  function controller(deps) {
    var sforce = deps.sforce;
    var hello = deps.hello;
    var E = deps.HelloEmbed;
    var ui = deps.ui;
    var calls = {}; // callId -> {taskId, who, number, started}
    var pending = null; // the record of the last click-to-dial
    var ctl = {};

    function cti(fn, args) {
      return new Promise(function (resolve) {
        var a = Object.assign({}, args, {
          callback: function (r) {
            resolve(r || {});
          },
        });
        fn(a);
      });
    }

    function task(ev, st, final) {
      var d = ev.data || {};
      var outbound = d.direction === "outbound";
      var num = E.otherNumber(ev);
      var v = {
        entityApiName: "Task",
        Subject:
          (final && !d.answeredAt
            ? "Missed call " + (outbound ? "to " : "from ")
            : outbound
              ? "Call to "
              : "Call from ") + (st.name || num),
        Status: "Completed",
        TaskSubtype: "Call",
        CallType: outbound ? "Outbound" : "Inbound",
        CallObject: String(d.callId || "").slice(0, CALL_ID_MAX),
        ActivityDate: new Date().toISOString().slice(0, 10),
        Description:
          "Logged by Hello.\nFrom: " +
          ((d.from && d.from.number) || "") +
          "\nTo: " +
          ((d.to && d.to.number) || ""),
      };
      if (final) {
        v.CallDisposition = disposition(ev);
        v.CallDurationInSeconds = seconds(d);
      }
      if (st.taskId) v.Id = st.taskId;
      var f = whoField(st.who);
      if (f) v[f] = st.who;
      return v;
    }

    async function save(ev, st, final) {
      var res = await cti(sforce.opencti.saveLog, {
        value: task(ev, st, final),
      });
      var id = res.returnValue && res.returnValue.recordId;
      if (res.success === false || !id) {
        ui.showError(
          "Salesforce did not save the call log" +
            (res.errors && res.errors[0] ? ": " + res.errors[0].message : "."),
        );
        return;
      }
      st.taskId = id;
    }

    async function begin(ev) {
      var d = ev.data || {};
      // The ring may have created the entry (it remembers who was popped).
      var st =
        calls[d.callId] ||
        (calls[d.callId] = { number: E.otherNumber(ev), who: "", taskId: "" });
      if (st.began) return;
      st.began = true;
      if (
        d.direction === "outbound" &&
        pending &&
        E.dialable(pending.number) === E.dialable(st.number)
      ) {
        st.who = pending.recordId || "";
        st.name = pending.recordName || "";
        pending = null;
      }
      ui.setCall(
        (d.direction === "outbound" ? "Call to " : "Call from ") +
          (st.name || st.number),
        "good",
      );
      await save(ev, st, false);
      if (st.taskId) {
        // The server patches this Task (duration, recording link) rather than
        // creating its own.
        hello
          .api("POST", "/api/v1/integrations/salesforce/engagements", {
            callId: d.callId,
            recordId: st.taskId,
          })
          .catch(function () {});
      }
    }

    async function end(ev, missed) {
      var d = ev.data || {};
      var st = calls[d.callId];
      if (!st) {
        if (!missed || !deps.logMissed) return;
        st = calls[d.callId] = {
          number: E.otherNumber(ev),
          who: "",
          taskId: "",
        };
      }
      if (st.ended) return;
      st.ended = true;
      ui.setCall("", "");
      await save(ev, st, true);
      if (missed && st.taskId) {
        hello
          .api("POST", "/api/v1/integrations/salesforce/engagements", {
            callId: d.callId,
            recordId: st.taskId,
          })
          .catch(function () {});
      }
    }

    /** One call event of the signed-in user. */
    ctl.handleEvent = async function (ev) {
      var d = ev && ev.data;
      if (
        !d ||
        !d.callId ||
        (d.direction !== "inbound" && d.direction !== "outbound")
      )
        return;
      switch (ev.type) {
        case "call.ringing":
          if (E.isInboundRinging(ev)) {
            ui.setCall("Incoming call from " + E.otherNumber(ev), "good");
            cti(sforce.opencti.setSoftphonePanelVisibility, { visible: true });
            var res = await cti(sforce.opencti.searchAndScreenPop, {
              searchParams: E.otherNumber(ev),
              queryParams: "",
              callType: sforce.opencti.CALL_TYPE.INBOUND,
              deferred: false,
            });
            var who = whoFromSearch(res);
            if (who) {
              calls[d.callId] = calls[d.callId] || {
                number: E.otherNumber(ev),
                who: who,
                taskId: "",
              };
              calls[d.callId].who = who;
            }
          }
          break;
        case "call.answered":
          await begin(ev);
          break;
        case "call.ended":
          await end(ev, false);
          break;
        case "call.missed":
          await end(ev, true);
          break;
      }
    };

    /** A click on a phone number in Salesforce, or a number typed in the page. */
    ctl.dial = async function (number, record) {
      var n = E.dialable(number);
      if (!n) {
        ui.showError("That is not a number Hello can dial.");
        return;
      }
      pending = {
        number: n,
        recordId: (record && record.recordId) || "",
        recordName: (record && record.recordName) || "",
      };
      ui.setCall("Dialling " + n + ". Your phone rings first; answer it.", "");
      try {
        await hello.originate(n);
      } catch (e) {
        pending = null;
        ui.setCall("", "");
        ui.showError(e.message);
      }
    };

    /** Registers click-to-dial with Salesforce. */
    ctl.enableClickToDial = function () {
      sforce.opencti.enableClickToDial({ callback: function () {} });
      sforce.opencti.onClickToDial({
        listener: function (p) {
          cti(sforce.opencti.setSoftphonePanelVisibility, { visible: true });
          ctl.dial(p && p.number, p);
        },
      });
    };

    return ctl;
  }

  function boot() {
    var $ = function (id) {
      return document.getElementById(id);
    };
    var E = root.HelloEmbed;
    var origin = sfOrigin(root.location.search, E);
    var err = $("error");
    function showError(t) {
      err.textContent = t;
      err.hidden = !t;
    }
    if (!origin) {
      showError(
        "This page runs inside a Salesforce call center. Open it from the Hello softphone in Lightning.",
      );
      return;
    }
    var hello = E.client({
      clientId: "hello-salesforce",
      callbackPath: "/integrations/salesforce/callback",
      scope: "calls events",
    });
    var logMissed =
      new URLSearchParams(root.location.search).get("missed") === "1";
    var ctl = null;
    var stop = null;

    function render() {
      var on = hello.signedIn();
      $("signin").hidden = on;
      $("phone").hidden = !on;
    }

    function start() {
      render();
      if (!hello.signedIn() || !ctl || stop) return;
      stop = hello.stream(ctl.handleEvent, function (s) {
        $("live").textContent =
          s === "live" ? "Connected to Hello." : "Reconnecting to Hello…";
      });
    }

    var s = document.createElement("script");
    s.src =
      origin + "/support/api/" + OPENCTI_VERSION + "/lightning/opencti_min.js";
    s.onerror = function () {
      showError(
        "Salesforce Open CTI could not be loaded. Check that this Hello address is a CSP Trusted Site.",
      );
    };
    s.onload = function () {
      ctl = controller({
        sforce: root.sforce,
        hello: hello,
        HelloEmbed: E,
        logMissed: logMissed,
        ui: {
          setCall: function (t, tone) {
            var c = $("call");
            c.textContent = t;
            c.hidden = !t;
            c.className = "status" + (tone ? " " + tone : "");
          },
          showError: showError,
        },
      });
      ctl.enableClickToDial();
      start();
    };
    document.head.appendChild(s);

    $("signin-btn").addEventListener("click", function () {
      showError("");
      hello.signIn().then(start, function (e) {
        showError(e.message);
      });
    });
    $("signout-btn").addEventListener("click", function () {
      if (stop) stop();
      stop = null;
      hello.signOut();
      render();
    });
    $("dial-form").addEventListener("submit", function (e) {
      e.preventDefault();
      showError("");
      if (ctl) ctl.dial($("number").value);
    });
    render();
  }

  root.HelloSalesforce = {
    controller: controller,
    sfOrigin: sfOrigin,
    whoFromSearch: whoFromSearch,
    whoField: whoField,
  };
  if (root.document && root.document.getElementById("dial-form")) boot();
})(typeof globalThis !== "undefined" ? globalThis : window);
