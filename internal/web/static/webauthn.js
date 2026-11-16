// Passkeys (WebAuthn). No inline scripts: buttons carry data attributes.
//
//   data-passkey="create|get"  ceremony type
//   data-options / data-finish endpoint URLs (POST, JSON)
//   data-login-csrf            double-submit token on the login page; otherwise
//                              the session token from <body data-csrf> is sent
//   data-name-input            id of the input with the passkey name (create)
//   data-msg-cancel            message when the user cancels
//
// Elements with [data-passkey-support] are shown only when the browser can
// use passkeys, [data-passkey-nosupport] only when it cannot.
(function () {
  "use strict";

  var supported = !!(window.PublicKeyCredential && navigator.credentials);
  document.querySelectorAll(supported ? "[data-passkey-support]" : "[data-passkey-nosupport]").forEach(function (el) {
    el.hidden = false;
  });
  if (!supported) return;

  function b64urlToBuf(s) {
    s = s.replace(/-/g, "+").replace(/_/g, "/");
    while (s.length % 4) s += "=";
    var bin = atob(s), buf = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i);
    return buf.buffer;
  }

  function bufToB64url(buf) {
    var bytes = new Uint8Array(buf), bin = "";
    for (var i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
    return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  function decodeCreation(pk) {
    pk.challenge = b64urlToBuf(pk.challenge);
    pk.user.id = b64urlToBuf(pk.user.id);
    (pk.excludeCredentials || []).forEach(function (c) { c.id = b64urlToBuf(c.id); });
    return pk;
  }

  function decodeRequest(pk) {
    pk.challenge = b64urlToBuf(pk.challenge);
    (pk.allowCredentials || []).forEach(function (c) { c.id = b64urlToBuf(c.id); });
    return pk;
  }

  function encodeCredential(cred) {
    var r = cred.response, out = {
      id: cred.id,
      rawId: bufToB64url(cred.rawId),
      type: cred.type,
      clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {},
      response: { clientDataJSON: bufToB64url(r.clientDataJSON) }
    };
    if (cred.authenticatorAttachment) out.authenticatorAttachment = cred.authenticatorAttachment;
    if (r.attestationObject) {
      out.response.attestationObject = bufToB64url(r.attestationObject);
      if (r.getTransports) out.response.transports = r.getTransports();
    } else {
      out.response.authenticatorData = bufToB64url(r.authenticatorData);
      out.response.signature = bufToB64url(r.signature);
      if (r.userHandle) out.response.userHandle = bufToB64url(r.userHandle);
    }
    return out;
  }

  function headers(btn) {
    var h = { "Content-Type": "application/json", "Accept": "application/json" };
    if (btn.dataset.loginCsrf) h["X-Login-CSRF"] = btn.dataset.loginCsrf;
    else h["X-CSRF-Token"] = document.body.dataset.csrf || "";
    return h;
  }

  function post(btn, url, body) {
    return fetch(url, {
      method: "POST", credentials: "same-origin", headers: headers(btn),
      body: body === undefined ? "{}" : JSON.stringify(body)
    }).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (data) {
        if (!res.ok) {
          var err = new Error(data.error || res.statusText);
          err.redirect = data.redirect;
          throw err;
        }
        return data;
      });
    });
  }

  function showError(btn, msg) {
    var box = btn.parentElement.querySelector("[data-passkey-error]") ||
      (btn.closest("section") || document).querySelector("[data-passkey-error]");
    if (box) { box.textContent = msg; box.hidden = false; } else { window.alert(msg); }
  }

  function run(btn) {
    var create = btn.dataset.passkey === "create";
    var finish = btn.dataset.finish;
    if (create) {
      var input = document.getElementById(btn.dataset.nameInput);
      var name = input ? input.value.trim() : "";
      if (!name) { if (input) input.focus(); return; }
      finish += (finish.indexOf("?") < 0 ? "?" : "&") + "name=" + encodeURIComponent(name);
    }
    btn.disabled = true;
    post(btn, btn.dataset.options).then(function (opts) {
      return create
        ? navigator.credentials.create({ publicKey: decodeCreation(opts.publicKey) })
        : navigator.credentials.get({ publicKey: decodeRequest(opts.publicKey), mediation: opts.mediation || undefined });
    }).then(function (cred) {
      if (!cred) throw new Error(btn.dataset.msgCancel || "Cancelled");
      return post(btn, finish, encodeCredential(cred));
    }).then(function (data) {
      window.location.assign(data.redirect || "/");
    }).catch(function (err) {
      btn.disabled = false;
      if (err.redirect) { window.location.assign(err.redirect); return; }
      var cancelled = err && (err.name === "NotAllowedError" || err.name === "AbortError");
      showError(btn, cancelled ? (btn.dataset.msgCancel || "Cancelled") : err.message);
    });
  }

  document.addEventListener("click", function (e) {
    var btn = e.target.closest("button[data-passkey]");
    if (btn) { e.preventDefault(); run(btn); }
  });
})();
