/* Hello's Dynamics 365 Channel Integration Framework (v2) widget (spec
 * integrations P19).
 *
 * Dynamics frames this page from the channel provider's channel URL, which
 * carries the environment address as ?org=. The page loads the CIF library
 * from that environment, so it needs no Dynamics token. It places calls
 * through Hello (the user's own desk phone rings first) when the user clicks
 * a phone number, and opens the caller's contact or lead on an incoming call.
 * The call itself is logged by the server as a Phone Call activity.
 */
(function (root) {
  "use strict";

  var DYN_HOSTS = ["dynamics.com"];
  var LIB = "/webresources/Widget/msdyn_ciLibrary.js";
  var PANEL_EXPANDED = 1;

  /** The environment origin the page may load the CIF library from, or "". */
  function orgOrigin(search, HelloEmbed) {
    var o = (new URLSearchParams(search).get("org") || "").replace(/\/+$/, "");
    return HelloEmbed.originAllowed(o, DYN_HOSTS) ? o : "";
  }

  /** The last nine digits Hello matches phone fields on (as the server does). */
  function tail(number) {
    var d = String(number || "").replace(/\D/g, "");
    return d.length > 9 ? d.slice(-9) : d;
  }

  /** The OData query that finds the records of a number in one table. */
  function phoneQuery(table, number) {
    var q = "'" + tail(number).replace(/'/g, "''") + "'";
    var f = "contains(telephone1," + q + ") or contains(mobilephone," + q + ")";
    var sel =
      table === "lead" ? "leadid,fullname,companyname" : "contactid,fullname";
    if (table === "contact") f += " or contains(telephone2," + q + ")";
    return "?$select=" + sel + "&$filter=" + encodeURIComponent(f) + "&$top=1";
  }

  /** The number of a click-to-act event (CIF hands it over as a JSON string). */
  function clickedNumber(data) {
    var v = data;
    if (typeof data === "string") {
      try {
        v = JSON.parse(data);
      } catch (e) {
        v = { value: data };
      }
    }
    return (v && (v.value || v.number)) || "";
  }

  function controller(deps) {
    var cif = deps.cif;
    var hello = deps.hello;
    var E = deps.HelloEmbed;
    var ui = deps.ui;
    var ctl = {};

    /** Opens the caller's record; contacts first, then leads. */
    ctl.screenPop = async function (number) {
      if (tail(number).length < 6) return false;
      var tables = ["contact", "lead"];
      for (var i = 0; i < tables.length; i++) {
        try {
          var raw = await cif.searchAndOpenRecords(
            tables[i],
            phoneQuery(tables[i], number),
            false,
          );
          var res = typeof raw === "string" ? JSON.parse(raw) : raw;
          if (res && Object.keys(res).length > 0) return true;
        } catch (e) {
          /* a table the user may not read is not a failure of the call */
        }
      }
      return false;
    };

    ctl.handleEvent = async function (ev) {
      var d = ev && ev.data;
      if (!d || !d.callId) return;
      if (E.isInboundRinging(ev)) {
        ui.setCall("Incoming call from " + E.otherNumber(ev), "good");
        try {
          await cif.setMode(PANEL_EXPANDED);
        } catch (e) {
          /* the panel stays as it is */
        }
        await ctl.screenPop(E.otherNumber(ev));
      } else if (ev.type === "call.ended" || ev.type === "call.missed") {
        ui.setCall("", "");
      } else if (
        ev.type === "call.answered" &&
        (d.direction === "inbound" || d.direction === "outbound")
      ) {
        ui.setCall(
          (d.direction === "outbound" ? "Call to " : "Call from ") +
            E.otherNumber(ev),
          "good",
        );
      }
    };

    ctl.dial = async function (number) {
      var n = E.dialable(number);
      if (!n) {
        ui.showError("That is not a number Hello can dial.");
        return;
      }
      ui.setCall("Dialling " + n + ". Your phone rings first; answer it.", "");
      try {
        await hello.originate(n);
      } catch (e) {
        ui.setCall("", "");
        ui.showError(e.message);
      }
    };

    /** Turns phone numbers in Dynamics into dial links. */
    ctl.enableClickToAct = async function () {
      await cif.setClickToAct(true);
      await cif.addHandler("onclicktoact", function (data) {
        var n = clickedNumber(data);
        return Promise.resolve(cif.setMode(PANEL_EXPANDED)).then(function () {
          return ctl.dial(n);
        });
      });
    };
    return ctl;
  }

  function boot() {
    var $ = function (id) {
      return document.getElementById(id);
    };
    var E = root.HelloEmbed;
    var origin = orgOrigin(root.location.search, E);
    var err = $("error");
    function showError(t) {
      err.textContent = t;
      err.hidden = !t;
    }
    if (!origin) {
      showError(
        "This page runs inside Dynamics 365. Add it as a channel provider and open it from there.",
      );
      return;
    }
    var hello = E.client({
      clientId: "hello-dynamics",
      callbackPath: "/integrations/dynamics/callback",
      scope: "calls events",
    });
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
    s.src = origin + LIB;
    s.onerror = function () {
      showError(
        "The Dynamics 365 framework could not be loaded. Check the environment address in the channel URL.",
      );
    };
    s.onload = function () {
      ctl = controller({
        cif: root.Microsoft.CIFramework,
        hello: hello,
        HelloEmbed: E,
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
      ctl.enableClickToAct().catch(function (e) {
        showError("Click-to-call is not available: " + e.message);
      });
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

  root.HelloDynamics = {
    controller: controller,
    orgOrigin: orgOrigin,
    phoneQuery: phoneQuery,
    clickedNumber: clickedNumber,
  };
  if (root.document && root.document.getElementById("dial-form")) boot();
})(typeof globalThis !== "undefined" ? globalThis : window);
