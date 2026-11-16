#!/usr/bin/env python3
"""Browser end-to-end test for the inventory (phase 3).

Against a running LANventory with an admin account WITHOUT a second factor:

  1. log in, create a location
  2. create an asset in the seeded category "Steckerleiste" (power strip) with
     a length, check the generated inventory number
  3. edit it (status defective), check the history on the detail page
  4. search the list: htmx must update the results without a page load and
     push the filters into the URL
  5. screenshots (light and dark), then delete the asset and the location

Usage:
  python3 -I tools/e2e/inventory_e2e.py <base-url> <admin-user> <password> <screenshot-dir>
"""

import os
import re
import sys
import time

from selenium import webdriver
from selenium.webdriver.chrome.service import Service
from selenium.webdriver.common.by import By
from selenium.webdriver.support import expected_conditions as EC
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

    def ok(self, msg):
        self.n += 1
        print(f"  ok {self.n:2d} {msg}")

    def shot(self, name):
        self.d.save_screenshot(os.path.join(self.out, f"p3-{name}.png"))

    def path(self):
        return self.d.current_url[len(self.base):] or "/"

    def submit(self, form_css):
        self.d.find_element(By.CSS_SELECTOR, form_css + " button[type=submit]").click()

    def run(self):
        d = self.d
        d.get(self.base + "/login")
        d.find_element(By.NAME, "username").send_keys(self.user)
        d.find_element(By.NAME, "password").send_keys(self.password)
        self.submit("form[action='/login']")
        self.wait.until(lambda d: not self.path().startswith("/login"))
        self.shot("dashboard")
        self.ok("login")

        d.get(self.base + "/locations")
        form = d.find_element(By.CSS_SELECTOR, "form[action='/locations']")
        form.find_element(By.NAME, "name").send_keys(TAG + " Lager")
        form.find_element(By.CSS_SELECTOR, "button[type=submit]").click()
        self.wait.until(lambda d: TAG + " Lager" in d.page_source)
        self.shot("locations")
        self.ok("location created")

        d.get(self.base + "/categories")
        link = d.find_element(By.PARTIAL_LINK_TEXT, "Steckerleiste")
        cat_url = link.get_attribute("href")
        cat_id = re.search(r"/categories/(\d+)", cat_url).group(1)
        self.shot("categories")
        d.get(cat_url)
        self.shot("category-editor")
        d.get(f"{self.base}/assets/new?category={cat_id}")
        d.find_element(By.NAME, "name").send_keys(TAG + " Steckerleiste <b>bold</b>")
        d.find_element(By.NAME, "serial_no").send_keys(TAG + "-SN")
        Select(d.find_element(By.NAME, "location")).select_by_visible_text(TAG + " Lager")
        length = d.find_element(By.XPATH, "//label[contains(normalize-space(.), 'Länge')]//input | "
                                          "//label[contains(normalize-space(.), 'Länge')]/following::input[1]")
        length.send_keys("1,5")
        self.shot("asset-form")
        d.find_element(By.CSS_SELECTOR, "form[action='/assets'] button[type=submit]").click()
        self.wait.until(lambda d: re.match(r"^/assets/\d+", self.path()))
        asset_url = d.current_url.split("?")[0]
        body = d.find_element(By.TAG_NAME, "main").text
        # Any inventory.number_format: "SL-00001" by default, "0001" with "{seq:04}".
        number = re.search(r"\b(?:SL-)?\d{4,}\b", d.find_element(By.TAG_NAME, "h1").text)
        assert number, "no generated inventory number"
        assert "1.5" in body or "1,5" in body, "length not shown"
        assert "<b>bold</b>" in body, "name not escaped as text"
        self.ok("asset created with number %s" % number.group(0))

        d.get(asset_url + "/edit")
        Select(d.find_element(By.NAME, "status")).select_by_value("defective")
        d.find_element(By.CSS_SELECTOR, "form[method=post][action^='/assets/'] button[type=submit]").click()
        self.wait.until(lambda d: re.match(r"^/assets/\d+(\?.*)?$", self.path()))
        hist = d.find_element(By.TAG_NAME, "main").text
        assert hist.count(self.user) >= 2, "history lacks entries"
        self.shot("asset-detail")
        self.ok("edited, history shown")

        d.get(self.base + "/assets")
        d.execute_script("window.__noReload = 1")
        q = d.find_element(By.CSS_SELECTOR, "#filters input[name=q]")
        q.send_keys(TAG + "-SN")
        self.wait.until(lambda d: "q=" in d.current_url)
        self.wait.until(lambda d: TAG in d.find_element(By.ID, "results").text)
        assert d.execute_script("return window.__noReload") == 1, "page reloaded instead of htmx swap"
        rows = d.find_elements(By.CSS_SELECTOR, "#results tbody tr")
        assert len(rows) == 1, len(rows)
        self.shot("assets-search")
        self.ok("htmx search without reload, URL updated")

        d.execute_script("document.documentElement.dataset.theme='dark'")
        self.shot("assets-search-dark")

        d.get(asset_url)
        d.execute_script("window.confirm = function () { return true; }")
        d.find_element(By.CSS_SELECTOR, "form[action$='/delete'] button").click()
        self.wait.until(lambda d: not re.match(r"^/assets/\d+", self.path()))
        # Leftover assets of earlier aborted runs still use their locations.
        while True:
            d.get(self.base + "/assets?q=E2E-")
            links = d.find_elements(By.CSS_SELECTOR, "#results tbody tr td a")
            if not links:
                break
            d.get(links[0].get_attribute("href"))
            d.execute_script("window.confirm = function () { return true; }")
            d.find_element(By.CSS_SELECTOR, "form[action$='/delete'] button").click()
            self.wait.until(lambda d: not re.match(r"^/assets/\d+", self.path()))
        # Delete this run's location (and leftovers of earlier aborted runs).
        while True:
            d.get(self.base + "/locations")
            d.execute_script("window.confirm = function () { return true; }")
            target = None
            for row in d.find_elements(By.CSS_SELECTOR, "tbody tr"):
                names = row.find_elements(By.NAME, "name")
                if names and names[0].get_attribute("value").startswith("E2E-"):
                    target = row.find_element(By.CSS_SELECTOR, "form[action$='/delete'] button")
                    break
            if target is None:
                break
            target.click()
            self.wait.until(EC.staleness_of(target))
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
        r.d.quit()


if __name__ == "__main__":
    main()
