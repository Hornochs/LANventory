#!/usr/bin/env python3
"""End-to-end test for API tokens and the REST API (phase 5).

Against a running LANventory with an admin account WITHOUT a second factor
that may record inspections (and was logged in within the last 10 minutes, so
creating a token needs no re-authentication – the script logs in first):

  1. create a token with all scopes on /account/tokens (shown once)
  2. call the API with it: /me, categories, create an asset with a custom
     field, look it up by number, record an inspection, due list
  3. error format: no token, wrong token, missing field (problem+json)
  4. revoke the token in the UI; the API rejects it afterwards
  5. screenshots of the token page and the API docs

Usage:
  python3 -I tools/e2e/api_e2e.py <base-url> <admin-user> <password> <screenshot-dir>
"""

import json
import os
import sys
import time
import urllib.error
import urllib.request

from selenium import webdriver
from selenium.webdriver.chrome.service import Service
from selenium.webdriver.common.by import By
from selenium.webdriver.support.ui import WebDriverWait

TAG = "E2E-%d" % int(time.time())


def call(base, method, path, token=None, body=None):
    req = urllib.request.Request(base + path, method=method)
    if token:
        req.add_header("Authorization", "Bearer " + token)
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, data=data, timeout=15) as res:
            raw = res.read()
            return res.status, res.headers.get("Content-Type", ""), json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        raw = e.read()
        return e.code, e.headers.get("Content-Type", ""), json.loads(raw) if raw else None


class Run:
    def __init__(self, base, user, password, out):
        self.base, self.user, self.password, self.out = base.rstrip("/"), user, password, out
        opts = webdriver.ChromeOptions()
        opts.binary_location = os.environ.get("CHROMIUM", "/usr/bin/chromium")
        if os.environ.get("HEADLESS", "1") != "0":
            opts.add_argument("--headless=new")
        for a in ("--no-sandbox", "--window-size=1280,900"):
            opts.add_argument(a)
        opts.set_capability("goog:loggingPrefs", {"browser": "ALL"})
        self.d = webdriver.Chrome(service=Service(os.environ.get("CHROMEDRIVER", "/usr/bin/chromedriver")), options=opts)
        self.wait = WebDriverWait(self.d, 15)
        self.n = 0

    def ok(self, msg):
        self.n += 1
        print(f"  ok {self.n:2d} {msg}")

    def shot(self, name):
        self.d.save_screenshot(os.path.join(self.out, f"p5-{name}.png"))

    def run(self):
        d, b = self.d, self.base
        d.get(b + "/login")
        d.find_element(By.NAME, "username").send_keys(self.user)
        d.find_element(By.NAME, "password").send_keys(self.password)
        d.find_element(By.CSS_SELECTOR, "form[action='/login'] button[type=submit]").click()
        self.wait.until(lambda d: "/login" not in d.current_url)

        d.get(b + "/account/tokens")
        form = d.find_element(By.CSS_SELECTOR, "form[action='/account/tokens']")
        form.find_element(By.NAME, "name").send_keys(TAG + " token")
        for box in form.find_elements(By.CSS_SELECTOR, "input[name=scope]"):
            if not box.is_selected():
                box.click()
        form.find_element(By.CSS_SELECTOR, "button[type=submit]").click()
        token = self.wait.until(lambda d: d.find_element(By.CSS_SELECTOR, "code.secret")).text.strip()
        assert token.startswith("lvk_") and len(token) == 44, token
        self.shot("token-created")
        self.ok("token created and shown once")

        st, ct, me = call(b, "GET", "/api/v1/me", token)
        assert st == 200 and me["user"]["username"] == self.user if isinstance(me.get("user"), dict) else st == 200, me
        st, _, cats = call(b, "GET", "/api/v1/categories?per_page=200", token)
        assert st == 200
        cat = next(c for c in cats["items"] if c["prefix"] == "SL")
        st, _, asset = call(b, "POST", "/api/v1/assets", token,
                            {"category_id": cat["id"], "name": TAG + " via API", "fields": {"length_m": 2.5}})
        assert st == 201, (st, asset)
        assert asset["inventory_no"] and asset["fields"]["length_m"] == 2.5, asset
        st, _, again = call(b, "GET", "/api/v1/assets/by-number/" + asset["inventory_no"], token)
        assert st == 200 and again["id"] == asset["id"]
        self.ok("asset %s created via API, by-number lookup" % asset["inventory_no"])

        st, _, insp = call(b, "POST", "/api/v1/assets/%d/inspections" % asset["id"], token,
                           {"result": "pass", "protection_class": "I", "visual_ok": True, "function_ok": True,
                            "pe_resistance_ohm": 0.12, "insulation_mohm": 300, "leakage_ma": 0.1})
        assert st == 201 and insp["next_due"], (st, insp)
        st, _, full = call(b, "GET", "/api/v1/assets/%d" % asset["id"], token)
        assert full["dguv"]["state"] == "ok", full.get("dguv")
        st, _, due = call(b, "GET", "/api/v1/inspections/due", token)
        assert st == 200 and all(i.get("id") != asset["id"] for i in due["items"])
        self.ok("inspection recorded via API, asset ok, not due")

        st, ct, p = call(b, "GET", "/api/v1/assets")
        assert st == 401 and ct.startswith("application/problem+json") and p["code"], (st, ct, p)
        st, _, p = call(b, "GET", "/api/v1/assets", "lvk_" + "x" * 40)
        assert st == 401 and p["code"] == "invalid_token", p
        st, ct, p = call(b, "POST", "/api/v1/assets", token, {"category_id": cat["id"], "name": TAG, "fields": {"length_m": "lang"}})
        assert st == 422 and p["code"] == "validation_failed" and p.get("errors"), p
        self.ok("problem+json errors (401, invalid token, validation)")

        st, _, _ = call(b, "DELETE", "/api/v1/assets/%d" % asset["id"], token)
        assert st in (200, 204), st
        self.ok("asset deleted via API")

        d.get(b + "/account/tokens")
        d.execute_script("window.confirm = function () { return true; }")
        for row in d.find_elements(By.CSS_SELECTOR, "tbody tr"):
            if TAG in row.text:
                row.find_element(By.CSS_SELECTOR, "form[action$='/revoke'] button").click()
                break
        self.wait.until(lambda d: "ok=" in d.current_url or "revoked" in d.page_source.lower())
        self.shot("tokens")
        st, _, p = call(b, "GET", "/api/v1/me", token)
        assert st == 401, (st, p)
        self.ok("revoked token rejected")

        d.get(b + "/api/")
        assert "/api/v1/assets" in d.page_source
        self.shot("api-docs")
        errors = [e for e in d.get_log("browser") if e["level"] == "SEVERE"]
        assert not errors, errors
        self.ok("API docs page, no console errors")


def main():
    if len(sys.argv) != 5:
        print(__doc__)
        sys.exit(2)
    r = Run(*sys.argv[1:5])
    try:
        r.run()
        print("PASS")
    except Exception:
        r.shot("failure")
        print("FAIL at", r.d.current_url)
        raise
    finally:
        r.d.quit()


if __name__ == "__main__":
    main()
