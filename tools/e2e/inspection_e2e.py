#!/usr/bin/env python3
"""Browser end-to-end test for DGUV V3 inspections (phase 4).

Against a running LANventory with an admin account WITHOUT a second factor
that has the "may record DGUV inspections" flag:

  1. log in, switch the UI to German
  2. create a power strip ("Steckerleiste"), it shows up as never inspected
     on the due page
  3. record a failed inspection with a high protective conductor resistance:
     guidance flag shown, asset becomes defective
  4. record a passed inspection, set the asset active again; next due date
  5. printable protocol without scripts, inspections list, CSV export
  6. screenshots, delete the asset, restore the UI language

Usage:
  python3 -I tools/e2e/inspection_e2e.py <base-url> <admin-user> <password> <screenshot-dir>
"""

import os
import re
import sys
import time

from selenium import webdriver
from selenium.webdriver.chrome.service import Service
from selenium.webdriver.common.by import By
from selenium.webdriver.support.ui import Select, WebDriverWait

TAG = "E2E-%d" % int(time.time())


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
        self.lang = None

    def ok(self, msg):
        self.n += 1
        print(f"  ok {self.n:2d} {msg}")

    def shot(self, name):
        self.d.save_screenshot(os.path.join(self.out, f"p4-{name}.png"))

    def path(self):
        return self.d.current_url[len(self.base):] or "/"

    def set_lang(self, lang):
        d = self.d
        d.get(self.base + "/account")
        sel = Select(d.find_element(By.CSS_SELECTOR, "form[action='/account/prefs'] select[name=language]"))
        if self.lang is None:
            self.lang = sel.first_selected_option.get_attribute("value")
        sel.select_by_value(lang)
        d.find_element(By.CSS_SELECTOR, "form[action='/account/prefs'] button[type=submit]").click()
        self.wait.until(lambda d: "ok=" in d.current_url)

    def inspect(self, asset_url, result, pe, set_flag):
        d = self.d
        d.get(asset_url + "/inspections/new")
        Select(d.find_element(By.NAME, "protection_class")).select_by_value("I")
        for name in ("visual_ok", "function_ok"):
            box = d.find_element(By.CSS_SELECTOR, f"input[type=checkbox][name={name}]")
            if not box.is_selected():
                box.click()
        d.find_element(By.NAME, "pe_resistance_ohm").send_keys(pe)
        d.find_element(By.NAME, "insulation_mohm").send_keys(">299")
        d.find_element(By.NAME, "leakage_ma").send_keys("0,2")
        d.find_element(By.NAME, "touch_ma").send_keys("0,1")
        device = d.find_element(By.NAME, "test_device")
        device.clear()  # prefilled from the user's last inspection
        device.send_keys("E2E tester")
        radio = d.find_elements(By.CSS_SELECTOR, f"input[name=result][value={result}]")
        if radio:
            radio[0].click()
        else:
            Select(d.find_element(By.NAME, "result")).select_by_value(result)
        if set_flag:
            box = d.find_element(By.CSS_SELECTOR, f"input[type=checkbox][name={set_flag}]")
            if not box.is_selected():
                box.click()
        self.shot(f"form-{result}")
        d.find_element(By.CSS_SELECTOR, "form[action$='/inspections'] button[type=submit]").click()
        self.wait.until(lambda d: "/inspections/new" not in d.current_url)

    def run(self):
        d = self.d
        d.get(self.base + "/login")
        d.find_element(By.NAME, "username").send_keys(self.user)
        d.find_element(By.NAME, "password").send_keys(self.password)
        d.find_element(By.CSS_SELECTOR, "form[action='/login'] button[type=submit]").click()
        self.wait.until(lambda d: not self.path().startswith("/login"))
        self.set_lang("de")
        self.ok("login, UI in German")

        d.get(self.base + "/categories")
        cat_id = re.search(r"/categories/(\d+)", d.find_element(By.PARTIAL_LINK_TEXT, "Steckerleiste").get_attribute("href")).group(1)
        d.get(f"{self.base}/assets/new?category={cat_id}")
        d.find_element(By.NAME, "name").send_keys(TAG + " Steckerleiste")
        d.find_element(By.XPATH, "//label[contains(normalize-space(.), 'Länge')]//input | "
                                 "//label[contains(normalize-space(.), 'Länge')]/following::input[1]").send_keys("3")
        d.find_element(By.CSS_SELECTOR, "form[action='/assets'] button[type=submit]").click()
        self.wait.until(lambda d: re.match(r"^/assets/\d+", self.path()))
        asset_url = d.current_url.split("?")[0]
        d.get(self.base + "/inspections/due?state=never")
        assert TAG in d.find_element(By.TAG_NAME, "main").text, "new asset not on the due page"
        self.shot("due")
        self.ok("asset listed as never inspected")

        self.inspect(asset_url, "fail", "0,5", "set_defective")
        page = d.find_element(By.TAG_NAME, "main").text
        assert "0,5" in page, "German number formatting missing"
        assert "> 299" in page, "qualifier of the insulation reading missing"
        d.get(asset_url)
        assert re.search(r"defekt", d.find_element(By.TAG_NAME, "main").text, re.I), "asset not defective"
        self.ok("failed inspection, guidance value shown, asset defective")

        self.inspect(asset_url, "pass", "0,1", "set_active")
        insp_url = d.current_url.split("?")[0]
        assert re.match(r".*/inspections/\d+$", insp_url), insp_url
        d.get(asset_url)
        main = d.find_element(By.TAG_NAME, "main").text
        assert not re.search(r"\bdefekt\b", main.split("Prüf")[0], re.I) or "aktiv" in main, "asset not active again"
        assert re.search(r"\d{2}\.\d{2}\.\d{4}", main), "no German date on the asset page"
        self.shot("asset")
        self.ok("passed inspection, asset active, next due date shown")

        d.get(insp_url + "/print")
        assert not d.find_elements(By.TAG_NAME, "script"), "protocol page has scripts"
        assert TAG in d.page_source
        self.shot("protocol")
        self.ok("printable protocol without scripts")

        d.get(self.base + "/inspections")
        assert TAG in d.find_element(By.TAG_NAME, "main").text
        self.shot("inspections")
        csv = d.execute_async_script(
            "var done = arguments[0];"
            "fetch('/inspections.csv', {credentials: 'same-origin'}).then(function (r) {"
            "  return r.text().then(function (t) { done([r.status, r.headers.get('content-disposition'), t]); });"
            "});")
        assert csv[0] == 200 and "attachment" in (csv[1] or ""), csv[:2]
        header = csv[2].splitlines()[0]
        assert "inventory_no" in header and TAG in csv[2], header
        self.ok("inspections list and CSV export")

        d.get(self.base + "/")
        self.shot("dashboard")

        d.get(asset_url)
        d.execute_script("window.confirm = function () { return true; }")
        d.find_element(By.CSS_SELECTOR, "form[action='%s/delete'] button" % asset_url[len(self.base):]).click()
        self.wait.until(lambda d: not re.match(r"^/assets/\d+", self.path()))
        self.ok("cleaned up")

        errors = [e for e in d.get_log("browser") if e["level"] == "SEVERE"]
        assert not errors, errors
        self.ok("no console errors")


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
        try:
            if r.lang and r.lang != "de":
                r.set_lang(r.lang)
        except Exception:
            pass
        r.d.quit()


if __name__ == "__main__":
    main()
