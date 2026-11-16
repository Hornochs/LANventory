# Reverse proxy

Run LANventory on `127.0.0.1:9060` and let a reverse proxy terminate TLS. Set `public_url` to
the address browsers use; every state-changing request is checked against it.

```yaml
listen: 127.0.0.1:9060
public_url: https://inventory.example.org
trusted_proxies: [127.0.0.1, "::1"]
```

## nginx

```nginx
server {
    listen 443 ssl;
    server_name inventory.example.org;
    # ssl_certificate / ssl_certificate_key …

    location / {
        proxy_pass http://127.0.0.1:9060;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto https;
    }
}

server {
    listen 80;
    server_name inventory.example.org;
    return 301 https://$host$request_uri;
}
```

- Send `X-Forwarded-Proto https` as a fixed value when TLS ends at this proxy.
- `X-Real-IP` and `X-Forwarded-For` are only trusted from `trusted_proxies`; they feed the login
  lockout and the audit log. Set `X-Forwarded-For` to `$remote_addr` (not
  `$proxy_add_x_forwarded_for`) so clients cannot forge it.
- Do not serve LANventory under a path prefix; `public_url` must not contain a path.

## Upload size (CSV import)

The CSV import (`/assets/import`) accepts files up to 5 MB; the request may be slightly larger
because of the form fields. Make sure the proxy lets such a body through. nginx limits request
bodies to 1 MB by default and answers 413 before LANventory sees the upload:

```nginx
    client_max_body_size 6m;   # in the server or location block
```

Other proxies have similar settings (Caddy: `request_body { max_size 6MB }`, Apache:
`LimitRequestBody 6291456`, Traefik: `maxRequestBodyBytes`).

## Passkeys

Passkeys (WebAuthn) work only in a secure context: use HTTPS and keep
`public_url` stable. A passkey is bound to the host name; changing the host name invalidates
all registered passkeys.
