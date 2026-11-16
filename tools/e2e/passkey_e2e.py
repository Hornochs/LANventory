#!/usr/bin/env python3
"""Browser end-to-end test for second factors (roadmap step 2.8).

Drives a real Chromium with a virtual WebAuthn authenticator (Chrome DevTools)
against a running LANventory over HTTPS:

  1. password login
  2. register a passkey on "Sign-in & security"
  3. log out, passwordless login with the passkey
  4. log out, password login + passkey as second factor
  5. re-authentication with the passkey
  6. enable TOTP (code computed here), recovery codes page
  7. log out, password login + TOTP code
  8. screenshots in light and dark theme

Use a throwaway user without second factors; afterwards remove its factors with
`lanventory user reset-mfa <name>` or delete it.

Usage:
  python3 -I tools/e2e/passkey_e2e.py <base-url> <user> <password> <screenshot-dir>

Environment: CHROMIUM (default /usr/bin/chromium), CHROMEDRIVER (default
/usr/bin/chromedriver), HEADLESS=0 to watch.
"""

import base64
import hashlib
import hmac
import os
import struct
import sys
import time

from selenium import webdriver
from selenium.webdriver.chrome.service import Service
from selenium.webdriver.common.by import By
from selenium.webdriver.common.virtual_authenticator import (
    Protocol,
    Transport,
    VirtualAuthenticatorOptions,
)
from selenium.webdriver.support import expected_conditions as EC
from selenium.webdriver.support.ui import WebDriverWait


def totp(secret_b32: str, step_offset: int = 0) -> str:
    s = secret_b32.replace(" ", "").upper()
    key = base64.b32decode(s + "=" * (-len(s) % 8))
    counter = int(time.time()) // 30 + step_offset
    digest = hmac.new(key, struct.pack(">Q", counter), hashlib.sha1).digest()
    off = digest[-1] & 0x0F
    code = (struct.unpack(">I", digest[off:off + 4])[0] & 0x7FFFFFFF) % 1_000_000
    return f"{code:06d}"


class Run:
    def __init__(self, base, user, password, out):
        self.base, self.user, self.password, self.out = base.rstrip("/"), user, password, out
        opts = webdriver.ChromeOptions()
        opts.binary_location = os.environ.get("CHROMIUM", "/usr/bin/chromium")
        if os.environ.get("HEADLESS", "1") != "0":
            opts.add_argument("--headless=new")
        for a in ("--no-sandbox", "--window-size=1280,900", "--lang=en-US"):
            opts.add_argument(a)
        opts.set_capability("goog:loggingPrefs", {"browser": "ALL"})
        self.d = webdriver.Chrome(service=Service(os.environ.get("CHROMEDRIVER", "/usr/bin/chromedriver")), options=opts)
        self.wait = WebDriverWait(self.d, 15)
        va = VirtualAuthenticatorOptions()
        va.protocol = Protocol.CTAP2
        va.transport = Transport.INTERNAL
        va.has_resident_key = True
        va.has_user_verification = True
        va.is_user_verified = True
        va.is_user_consenting = True
        self.d.add_virtual_authenticator(va)
        self.step = 0

    def ok(self, msg):
        self.step += 1
        print(f"  ok {self.step:2d} {msg}")

    def shot(self, name):
        self.d.save_screenshot(os.path.join(self.out, f"p2-{name}.png"))

    def path(self):
        return self.d.current_url[len(self.base):] or "/"

    def wait_path(self, prefix):
        self.wait.until(lambda d: d.current_url[len(self.base):].startswith(prefix))

    def click(self, css):
        el = self.wait.until(EC.element_to_be_clickable((By.CSS_SELECTOR, css)))
        el.click()

    def password_login(self):
        self.d.get(self.base + "/login")
        self.d.find_element(By.NAME, "username").send_keys(self.user)
        self.d.find_element(By.NAME, "password").send_keys(self.password)
        self.d.find_element(By.CSS_SELECTOR, "form[action='/login'] button[type=submit]").click()

    def logout(self):
        self.d.get(self.base + "/account")
        self.d.find_element(By.CSS_SELECTOR, "form[action='/logout'] button").click()
        self.wait_path("/login")

    def run(self):
        d = self.d
        self.password_login()
        self.wait_path("/")
        assert not self.path().startswith("/login"), "password login failed"
        self.ok("password login")

        d.get(self.base + "/account/security")
        d.find_element(By.ID, "passkey-name").send_keys("E2E virtual authenticator")
        self.click("button[data-passkey='create']")
        self.wait_path("/account/security?ok=passkey_added")  # _recovery on the first factor
        assert "E2E virtual authenticator" in d.page_source
        self.ok("passkey registered")
        self.shot("security-passkey")

        self.logout()
        self.click("button[data-passkey='get']")
        self.wait.until(lambda d: not self.path().startswith("/login"))
        assert self.path() == "/", self.path()
        self.ok("passwordless login with passkey")

        self.logout()
        self.password_login()
        self.wait_path("/login/mfa")
        self.shot("mfa-passkey")
        self.click("button[data-passkey='get']")
        self.wait.until(lambda d: not self.path().startswith("/login"))
        self.ok("password + passkey as second factor")

        d.get(self.base + "/reauth?next=/account/security")
        self.shot("reauth")
        self.click("button[data-passkey='get']")
        self.wait_path("/account/security")
        self.ok("re-authentication with passkey")

        self.click("form[action='/account/totp/setup'] button")
        secret = self.wait.until(EC.presence_of_element_located((By.CSS_SELECTOR, "code.secret"))).text
        self.shot("totp-enroll")
        d.find_element(By.CSS_SELECTOR, "form[action='/account/totp/confirm'] input[name=code]").send_keys(totp(secret))
        d.find_element(By.CSS_SELECTOR, "form[action='/account/totp/confirm'] button").click()
        self.wait.until(EC.presence_of_element_located((By.CSS_SELECTOR, "ol.recovery-codes")))
        codes = d.find_elements(By.CSS_SELECTOR, "ol.recovery-codes code")
        assert len(codes) == 10, len(codes)
        self.ok("TOTP enabled, 10 recovery codes shown")
        self.shot("recovery-codes")

        self.logout()
        self.password_login()
        self.wait_path("/login/mfa")
        # The enrollment code's step is used up; take the next one.
        d.find_element(By.CSS_SELECTOR, "input[name=code][autocomplete=one-time-code]").send_keys(totp(secret, 1))
        d.find_element(By.CSS_SELECTOR, "form[action='/login/mfa'] button[type=submit]").click()
        self.wait.until(lambda d: not self.path().startswith("/login"))
        self.ok("password + TOTP")

        d.get(self.base + "/account/security")
        self.shot("security-light")
        d.execute_script("document.documentElement.dataset.theme='dark'")
        self.shot("security-dark")
        self.logout()
        d.execute_script("document.documentElement.dataset.theme='dark'")
        self.shot("login-dark")

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
