// Hello sidebar app for Zendesk Support. It holds no Hello credential: the
// Call button opens Hello's dialer in a new tab, where the agent is signed in.
// Everything shown is set with textContent, never as HTML.
(function () {
  "use strict";
  var HELLO_URL = __HELLO_URL_JSON__;
  var client = ZAFClient.init();
  var root = document.getElementById("app");

  function el(tag, text, cls) {
    var e = document.createElement(tag);
    if (text !== undefined) e.textContent = text;
    if (cls) e.className = cls;
    return e;
  }

  function callLink(number) {
    var a = el("a", "Call", "call");
    a.href =
      HELLO_URL.replace(/\/+$/, "") + "/dial?n=" + encodeURIComponent(number);
    a.target = "_blank";
    a.rel = "noopener noreferrer";
    a.setAttribute("aria-label", "Call " + number + " with Hello");
    return a;
  }

  function render(user, tickets) {
    root.textContent = "";
    root.appendChild(el("h2", user.name || "Requester"));
    var numbers = [user.phone].filter(Boolean);
    var ul = el("ul");
    numbers.forEach(function (n) {
      var li = el("li", n);
      li.appendChild(callLink(n));
      ul.appendChild(li);
    });
    if (!numbers.length)
      ul.appendChild(el("li", "No phone number on file.", "muted"));
    root.appendChild(ul);
    root.appendChild(el("h2", "Recent tickets"));
    var tl = el("ul");
    tickets.forEach(function (t) {
      tl.appendChild(
        el(
          "li",
          "#" +
            t.id +
            " " +
            (t.subject || "(no subject)") +
            " (" +
            t.status +
            ")",
        ),
      );
    });
    if (!tickets.length) tl.appendChild(el("li", "No other tickets.", "muted"));
    root.appendChild(tl);
  }

  client
    .get("ticket.requester.id")
    .then(function (d) {
      var id = d["ticket.requester.id"];
      if (!id) throw new Error("This ticket has no requester.");
      return Promise.all([
        client.request("/api/v2/users/" + encodeURIComponent(id) + ".json"),
        client.request(
          "/api/v2/users/" +
            encodeURIComponent(id) +
            "/tickets/requested.json?per_page=5&sort_order=desc",
        ),
      ]);
    })
    .then(function (r) {
      render(r[0].user, r[1].tickets || []);
      client.invoke("resize", { width: "100%", height: "260px" });
    })
    .catch(function (e) {
      root.textContent =
        e && e.message ? e.message : "Could not load the requester.";
    });
})();
