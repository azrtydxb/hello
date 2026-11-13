// Hello sidebar app for Freshdesk. It holds no Hello credential: the Call
// button opens Hello's dialer in a new tab, where the agent is signed in.
// Everything shown is set with textContent, never as HTML.
var HELLO_URL = __HELLO_URL_JSON__;

app.initialized().then(function (client) {
  "use strict";
  var root = document.getElementById("app");

  function el(tag, text, cls) {
    var e = document.createElement(tag);
    if (text !== undefined) e.textContent = text;
    if (cls) e.className = cls;
    return e;
  }

  function callLink(number) {
    var a = el("a", "Call", "call");
    a.href = HELLO_URL.replace(/\/+$/, "") + "/dial?n=" + encodeURIComponent(number);
    a.target = "_blank";
    a.rel = "noopener noreferrer";
    a.setAttribute("aria-label", "Call " + number + " with Hello");
    return a;
  }

  function render(contact, tickets) {
    root.textContent = "";
    root.appendChild(el("h2", contact.name || "Requester"));
    var numbers = [contact.phone, contact.mobile].filter(Boolean);
    var ul = el("ul");
    numbers.forEach(function (n) {
      var li = el("li", n);
      li.appendChild(callLink(n));
      ul.appendChild(li);
    });
    if (!numbers.length) ul.appendChild(el("li", "No phone number on file.", "muted"));
    root.appendChild(ul);
    root.appendChild(el("h2", "Recent tickets"));
    var tl = el("ul");
    tickets.forEach(function (t) {
      tl.appendChild(el("li", "#" + t.id + " " + (t.subject || "(no subject)")));
    });
    if (!tickets.length) tl.appendChild(el("li", "No other tickets.", "muted"));
    root.appendChild(tl);
  }

  client.data
    .get("contact")
    .then(function (d) {
      var contact = d.contact;
      return client.request
        .invokeTemplate("listTickets", { context: { requesterId: String(contact.id) } })
        .then(function (r) {
          var tickets = [];
          try { tickets = JSON.parse(r.response); } catch (e) { /* none */ }
          render(contact, tickets);
          client.instance.resize({ height: "260px" });
        });
    })
    .catch(function () {
      root.textContent = "Could not load the requester.";
    });
});
