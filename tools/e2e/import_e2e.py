#!/usr/bin/env python3
"""Browser end-to-end test for CSV import and export (phase 6).

Uploads a file as Excel on German Windows writes it (Windows-1252, semicolons,
German headers) with an admin account WITHOUT a second factor:

  1. upload, automatic column mapping, preview with one invalid row
  2. import with "skip invalid rows", result page
  3. the imported assets appear in the list with their custom field
  4. CSV export of the filtered list contains them
  5. screenshots, then the imported assets are deleted

Usage:
  python3 -I tools/e2e/import_e2e.py <base-url> <admin-user> <password> <screenshot-dir>
"""

import os
import re
import sys
import tempfile
import time

from selenium import webdriver
from selenium.webdriver.chrome.service import Service
from selenium.webdriver.common.by import By
from selenium.webdriver.support.ui import WebDriverWait

TAG = "E2E%d" % int(time.time())

ROWS = [
    "Inventarnummer;Kategorie;Bezeichnung;Seriennummer;Status;Länge",
    f";Steckerleiste;{TAG} Leiste Bühne;{TAG}-1;aktiv;5",
    f";Steckerleiste;{TAG} Leiste Kasse;{TAG}-2;defekt;1,5",
    f";Kabeltrommel;{TAG} Trommel Außen;{TAG}-3;aktiv;25",
    f";Gibt es nicht;{TAG} Kaputte Zeile;{TAG}-4;aktiv;3",
]


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
        self.wait = WebDriverWait(self.d, 20)
        self.n = 0
        fd, self.csv = tempfile.mkstemp(suffix=".csv")
        with os.fdopen(fd, "wb") as f:
            f.write(("\r\n".join(ROWS) + "\r\n").encode("cp1252"))

    def ok(self, msg):
        self.n += 1
        print(f"  ok {self.n:2d} {msg}")

    def shot(self, name):
        self.d.save_screenshot(os.path.join(self.out, f"p6-{name}.png"))

    def run(self):
        d, b = self.d, self.base
        d.get(b + "/login")
        d.find_element(By.NAME, "username").send_keys(self.user)
        d.find_element(By.NAME, "password").send_keys(self.password)
        d.find_element(By.CSS_SELECTOR, "form[action='/login'] button[type=submit]").click()
        self.wait.until(lambda d: "/login" not in d.current_url)

        d.get(b + "/assets/import")
        d.find_element(By.CSS_SELECTOR, "input[type=file][name=file]").send_keys(self.csv)
        self.shot("upload")
        d.find_element(By.CSS_SELECTOR, "form[action='/assets/import'] button[type=submit]").click()
        self.wait.until(lambda d: d.find_elements(By.CSS_SELECTOR, "form[action='/assets/import/preview']"))
        page = d.page_source
        assert "Bühne" in page and "Außen" in page, "Windows-1252 not decoded"
        self.shot("mapping")
        self.ok("upload decoded (Windows-1252, semicolons), mapping page")

        for el in d.find_elements(By.CSS_SELECTOR, "input[name=on_error][value=skip], select[name=on_error] option[value=skip]"):
            el.click()
        d.find_element(By.CSS_SELECTOR, "form[action='/assets/import/preview'] button[type=submit]").click()
        self.wait.until(lambda d: d.find_elements(By.CSS_SELECTOR, "form[action='/assets/import/confirm']"))
        main = d.find_element(By.TAG_NAME, "main").text
        assert "Gibt es nicht" in main, "invalid row not shown"
        self.shot("preview")
        self.ok("preview with one invalid row")

        d.find_element(By.CSS_SELECTOR, "form[action='/assets/import/confirm'] button[type=submit]").click()
        self.wait.until(lambda d: "/assets/import/confirm" in d.current_url or "3" in d.find_element(By.TAG_NAME, "main").text)
        self.shot("result")
        self.ok("imported")

        d.get(b + "/assets?q=" + TAG)
        rows = d.find_elements(By.CSS_SELECTOR, "#results tbody tr")
        assert len(rows) == 3, len(rows)
        assert "defe" in d.find_element(By.ID, "results").text.lower()
        self.ok("3 assets in the list")

        csv = d.execute_async_script(
            "var done = arguments[0];"
            "fetch('/assets.csv?q=%s', {credentials: 'same-origin'}).then(function (r) {"
            "  return r.text().then(function (t) { done([r.status, t]); });"
            "});" % TAG)
        assert csv[0] == 200, csv[0]
        lines = [l for l in csv[1].splitlines() if l]
        assert len(lines) == 4 and "field:length_m" in lines[0], lines[:2]
        assert any(",1.5," in l or ",1.5" in l for l in lines[1:]), lines
        self.ok("CSV export contains the imported assets with lengths")

        # Clean up: delete the imported assets.
        while True:
            d.get(b + "/assets?q=" + TAG)
            links = d.find_elements(By.CSS_SELECTOR, "#results tbody tr td a")
            if not links:
                break
            d.get(links[0].get_attribute("href"))
            d.execute_script("window.confirm = function () { return true; }")
            d.find_element(By.CSS_SELECTOR, "form[action$='/delete'] button").click()
            self.wait.until(lambda d: not re.match(r".*/assets/\d+$", d.current_url.split("?")[0]))
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
        os.unlink(r.csv)


if __name__ == "__main__":
    main()
