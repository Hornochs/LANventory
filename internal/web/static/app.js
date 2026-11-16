// LANventory UI. No inline scripts: the CSP only allows files from /static.
(function () {
  "use strict";

  // Confirm dialogs: <form data-confirm="..."> or <button data-confirm="...">.
  document.addEventListener("submit", function (e) {
    var form = e.target;
    var submitter = e.submitter;
    var msg = (submitter && submitter.dataset.confirm) || form.dataset.confirm;
    if (msg && !window.confirm(msg)) {
      e.preventDefault();
    }
  });

  // Timestamps: <time class="ts" data-ts="<unix seconds>"> shows local time.
  function pad(n) { return n < 10 ? "0" + n : String(n); }
  Array.prototype.forEach.call(document.querySelectorAll("time.ts[data-ts]"), function (el) {
    var secs = parseInt(el.dataset.ts, 10);
    if (!isFinite(secs)) return;
    var d = new Date(secs * 1000);
    el.textContent = d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()) + " " +
      pad(d.getHours()) + ":" + pad(d.getMinutes());
  });

  // Bulk edit in the asset list: row checkboxes (data-bulk-row), the select-all
  // box in the header (data-bulk-selectall) and "all matches" (data-bulk-all).
  // The results are swapped by htmx, so everything is bound by delegation.
  // The selection survives swaps (leaving the search field triggers a second
  // request with the same results): rows that are still listed stay checked.
  var bulkSelected = {};
  function bulkUpdate() {
    var rows = document.querySelectorAll("input[data-bulk-row]");
    var checked = 0;
    Array.prototype.forEach.call(rows, function (b) { if (b.checked) checked++; });
    var all = document.querySelector("input[data-bulk-all]");
    var n = all && all.checked ? parseInt(all.dataset.total, 10) || 0 : checked;
    var count = document.querySelector("[data-bulk-count]");
    if (count) count.textContent = (count.dataset.template || "%d").replace("%d", String(n));
    var submit = document.querySelector("[data-bulk-submit]");
    if (submit) submit.disabled = n === 0;
    var head = document.querySelector("input[data-bulk-selectall]");
    if (head) {
      head.checked = rows.length > 0 && checked === rows.length;
      head.indeterminate = checked > 0 && checked < rows.length;
    }
  }
  document.addEventListener("change", function (e) {
    var t = e.target;
    if (!t || !t.matches) return;
    if (t.matches("input[data-bulk-selectall]")) {
      Array.prototype.forEach.call(document.querySelectorAll("input[data-bulk-row]"), function (b) {
        b.checked = t.checked;
        bulkSelected[b.value] = t.checked;
      });
    }
    if (t.matches("input[data-bulk-row]")) bulkSelected[t.value] = t.checked;
    if (t.matches("input[data-bulk-selectall], input[data-bulk-row], input[data-bulk-all]")) bulkUpdate();
  });
  document.addEventListener("htmx:afterSwap", function () {
    Array.prototype.forEach.call(document.querySelectorAll("input[data-bulk-row]"), function (b) {
      if (bulkSelected[b.value]) b.checked = true;
    });
    bulkUpdate();
  });
  window.addEventListener("pageshow", bulkUpdate);
  bulkUpdate();

  // Inspection form: hint when the test device's calibration ends before the
  // inspection date (data-calib-date, data-calib-until, data-calib-hint). Not blocking.
  function calibCheck() {
    var on = document.querySelector("input[data-calib-date]");
    var until = document.querySelector("input[data-calib-until]");
    var hint = document.querySelector("[data-calib-hint]");
    if (!on || !until || !hint) return;
    hint.hidden = !(on.value && until.value && until.value < on.value);
  }
  document.addEventListener("input", function (e) {
    if (e.target.matches("input[data-calib-date], input[data-calib-until]")) calibCheck();
  });
})();
