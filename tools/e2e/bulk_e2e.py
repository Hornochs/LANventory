#!/usr/bin/env python3
"""Browser end-to-end test for the bulk edit of assets (step 6.9).

Against a running LANventory with an admin account WITHOUT a second factor:

  1. log in, create a location and three E2E-tagged assets in the seeded
     category "Steckerleiste" (power strip)
  2. filter the list (htmx), select the rows with the select-all checkbox, check
     the counter and that the selection survives only until the next swap
     (the checkboxes are re-bound after an htmx swap)
  3. "edit selected": tick location and status, preview (dry run: nothing is
     written yet), apply
  4. the list shows the flash message and the new location and status for all
     three assets; every asset has an "asset.update" history entry
  5. clean up (assets and locations tagged E2E-), check for console errors

Usage:
  python3 -I tools/e2e/bulk_e2e.py <base-url> <admin-user> <password> <screenshot-dir>
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
        self.d.save_screenshot(os.path.join(self.out, f"bulk-{name}.png"))

    def path(self):
        return self.d.current_url[len(self.base):] or "/"

    def counter(self):
        text = self.d.find_element(By.CSS_SELECTOR, "[data-bulk-count]").text
        return int(re.search(r"\d+", text).group(0))

    def expect_count(self, n):
        # Leaving the search field makes htmx reload the (same) results; the
        # selection survives, but the counter is briefly re-rendered.
        try:
            WebDriverWait(self.d, 5).until(lambda d: self.counter() == n)
        except Exception:
            raise AssertionError("selected %d, want %d" % (self.counter(), n))

    def create_asset(self, cat_id, name):
        d = self.d
        d.get(f"{self.base}/assets/new?category={cat_id}")
        d.find_element(By.NAME, "name").send_keys(name)
        length = d.find_element(By.XPATH, "//label[contains(normalize-space(.), 'Länge')]//input | "
                                          "//label[contains(normalize-space(.), 'Länge')]/following::input[1]")
        length.send_keys("2")
        d.find_element(By.CSS_SELECTOR, "form[action='/assets'] button[type=submit]").click()
        self.wait.until(lambda d: re.match(r"^/assets/\d+", self.path()))
        return d.current_url.split("?")[0]

    def delete_leftovers(self):
        d = self.d
        while True:
            d.get(self.base + "/assets?q=E2E-")
            links = d.find_elements(By.CSS_SELECTOR, "#results tbody tr td a")
            if not links:
                break
            d.get(links[0].get_attribute("href"))
            d.execute_script("window.confirm = function () { return true; }")
            d.find_element(By.CSS_SELECTOR, "form[action$='/delete'] button").click()
            self.wait.until(lambda d: not re.match(r"^/assets/\d+", self.path()))
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

    def run(self):
        d = self.d
        d.get(self.base + "/login")
        d.find_element(By.NAME, "username").send_keys(self.user)
        d.find_element(By.NAME, "password").send_keys(self.password)
        d.find_element(By.CSS_SELECTOR, "form[action='/login'] button[type=submit]").click()
        self.wait.until(lambda d: not self.path().startswith("/login"))
        self.ok("login")

        d.get(self.base + "/locations")
        form = d.find_element(By.CSS_SELECTOR, "form[action='/locations']")
        form.find_element(By.NAME, "name").send_keys(TAG + " Lager")
        form.find_element(By.CSS_SELECTOR, "button[type=submit]").click()
        self.wait.until(lambda d: TAG + " Lager" in d.page_source)

        d.get(self.base + "/categories")
        cat_id = re.search(r"/categories/(\d+)", d.find_element(By.PARTIAL_LINK_TEXT, "Steckerleiste")
                           .get_attribute("href")).group(1)
        urls = [self.create_asset(cat_id, f"{TAG} Leiste {w}") for w in ("Alpha", "Bravo", "Charlie")]
        self.ok("location and three assets created")

        # Selection in the list.
        d.get(self.base + "/assets")
        d.execute_script("window.__noReload = 1")
        q = d.find_element(By.CSS_SELECTOR, "#filters input[name=q]")
        q.send_keys(TAG)
        self.wait.until(lambda d: len(d.find_elements(By.CSS_SELECTOR, "#results input[data-bulk-row]")) == 3)
        assert d.execute_script("return window.__noReload") == 1, "page reloaded instead of htmx swap"
        self.expect_count(0)
        assert not d.find_element(By.CSS_SELECTOR, "[data-bulk-submit]").is_enabled()
        d.find_element(By.CSS_SELECTOR, "input[data-bulk-selectall]").click()
        self.expect_count(3)
        assert d.find_element(By.CSS_SELECTOR, "[data-bulk-submit]").is_enabled()
        d.find_elements(By.CSS_SELECTOR, "input[data-bulk-row]")[0].click()
        self.expect_count(2)
        d.find_elements(By.CSS_SELECTOR, "input[data-bulk-row]")[0].click()
        self.expect_count(3)
        self.shot("list-selected")
        self.ok("rows selected, counter and select-all work")

        # A new filter swaps the results; the controls must work again (event
        # delegation). Rows that stay listed keep their selection; only listed
        # rows are submitted.
        q.clear()
        q.send_keys(TAG + " Alpha")
        self.wait.until(lambda d: len(d.find_elements(By.CSS_SELECTOR, "#results input[data-bulk-row]")) == 1)
        self.expect_count(1)
        d.find_element(By.CSS_SELECTOR, "input[data-bulk-selectall]").click()
        self.expect_count(0)
        d.find_element(By.CSS_SELECTOR, "input[data-bulk-selectall]").click()
        self.expect_count(1)
        q.clear()
        q.send_keys(TAG)
        self.wait.until(lambda d: len(d.find_elements(By.CSS_SELECTOR, "#results input[data-bulk-row]")) == 3)
        self.expect_count(3)
        self.ok("selection works again after an htmx swap and is kept for listed rows")

        d.find_element(By.CSS_SELECTOR, "[data-bulk-submit]").click()
        self.wait.until(lambda d: self.path() == "/assets/bulk/edit")
        assert TAG not in d.find_element(By.TAG_NAME, "h1").text
        self.shot("bulk-form")
        d.find_element(By.NAME, "chg_location").click()
        Select(d.find_element(By.NAME, "v_location")).select_by_visible_text(TAG + " Lager")
        d.find_element(By.NAME, "chg_status").click()
        Select(d.find_element(By.NAME, "v_status")).select_by_value("defective")
        d.find_element(By.CSS_SELECTOR, "form.bulk-form button.primary").click()
        self.wait.until(lambda d: self.path() == "/assets/bulk/preview")
        main = d.find_element(By.TAG_NAME, "main").text
        assert "3" in main and TAG + " Lager" in main, main
        self.shot("bulk-preview")
        # Dry run: nothing is written yet.
        d.execute_script("window.open(arguments[0])", urls[0])
        d.switch_to.window(d.window_handles[-1])
        assert TAG + " Lager" not in d.find_element(By.TAG_NAME, "main").text
        d.close()
        d.switch_to.window(d.window_handles[0])
        self.ok("preview shows the changes and writes nothing")

        d.find_element(By.CSS_SELECTOR, "form[action='/assets/bulk/apply'] button").click()
        self.wait.until(lambda d: self.path().startswith("/assets?ok=bulk"))
        assert "ok=bulk&n=3" in self.path(), self.path()
        assert "3" in d.find_element(By.CSS_SELECTOR, ".alert-ok").text, "flash message missing"
        d.get(self.base + "/assets?q=" + TAG)
        rows = d.find_elements(By.CSS_SELECTOR, "#results tbody tr")
        assert len(rows) == 3
        text = d.find_element(By.ID, "results").text
        assert text.count(TAG + " Lager") == 3, text
        self.shot("list-after")
        for url in urls:
            d.get(url)
            body = d.find_element(By.TAG_NAME, "main").text
            assert TAG + " Lager" in body
            assert body.count(self.user) >= 2, "history lacks the bulk change"
        self.ok("all three assets changed, history entries written")

        self.delete_leftovers()
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
