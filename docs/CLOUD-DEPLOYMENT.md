# Engram Cloud Server — Deployment

This document covers deploying the `engram-cloud` sync server securely. It assumes familiarity with nginx, systemd, and PostgreSQL.

## TLS is mandatory

The `engram-cloud` binary refuses to start over plain HTTP unless the operator explicitly opts in via `ENGRAM_CLOUD_ALLOW_INSECURE=1`. Tokens travel in the `Authorization: Bearer <apiKey>` header and MUST NOT cross any network without TLS in front. A single stolen token grants full access until rotation.

There are two supported topologies:

### 1. Reverse proxy terminates TLS (recommended for internal use)

```
client ──HTTPS──► nginx (TLS 1.3) ──HTTP (loopback only)──► engram-cloud
```

This is the expected topology at JPH and similar organisations that already run nginx. TLS certificates live on the proxy. The cloud server listens on `127.0.0.1:8080` only, so the raw HTTP port is never reachable from the network.

Run the cloud binary with:

```sh
export ENGRAM_CLOUD_DB="postgres://engram:...@db/engram_cloud?sslmode=require"
export ENGRAM_CLOUD_PORT=8080
export ENGRAM_CLOUD_ALLOW_INSECURE=1
engram-cloud serve
```

Bind the listener to loopback via systemd or `iptables` — the binary itself listens on all interfaces by default.

### 2. Direct TLS on the server

Useful for standalone deployments without a proxy:

```sh
export ENGRAM_CLOUD_DB="postgres://engram:...@db/engram_cloud?sslmode=require"
export ENGRAM_CLOUD_PORT=8443
export ENGRAM_CLOUD_TLS_CERT=/etc/engram/tls/cert.pem
export ENGRAM_CLOUD_TLS_KEY=/etc/engram/tls/key.pem
engram-cloud serve
```

If either variable is set, both MUST be set, otherwise the binary fails fast at startup.

## nginx reference configuration

Minimal reverse-proxy block for topology 1. Replace `engram.internal` with the real hostname and make sure the certificate is trusted by the clients.

```nginx
server {
    listen 443 ssl http2;
    server_name engram.internal;

    ssl_certificate     /etc/letsencrypt/live/engram.internal/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/engram.internal/privkey.pem;

    ssl_protocols       TLSv1.2 TLSv1.3;
    ssl_ciphers         HIGH:!aNULL:!MD5;
    ssl_prefer_server_ciphers on;

    # Discourage downgrade and ensure browsers stick to HTTPS.
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;

    # Forward the real client IP so the cloud server's RealIP middleware
    # gets the right value for rate limiting and audit logs.
    location / {
        proxy_pass         http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header   Host              $host;
        proxy_set_header   X-Real-IP         $remote_addr;
        proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto $scheme;

        # Push endpoint uploads can be a few megabytes; bump from the default.
        client_max_body_size 10m;

        # Keep the long-running sync connections alive.
        proxy_read_timeout  120s;
        proxy_send_timeout  120s;
    }
}

server {
    listen 80;
    server_name engram.internal;
    return 301 https://$host$request_uri;
}
```

Notes:
- The loopback `127.0.0.1:8080` is the only place `engram-cloud` should listen. Do not expose `:8080` to the network.
- Remote workers reach the server through the same nginx hostname as on-site users — there is no split-DNS requirement.

## Logging

`engram-cloud` runs `RequestLogMiddleware` globally. It logs one line per request with method, path, status, and duration. It does NOT log request or response headers, which means the `Authorization` header never touches the log.

When adding new log statements, pass any string that could contain a token through `MaskAPIKey` before logging. The helper redacts `engram_sk_*` values and unknown Bearer opaque values so future token formats cannot silently leak.

## Rotation and revocation

- Operators rotate a user's key with the existing `POST /api/v1/auth/rotate-key` endpoint. The old hash is overwritten; the next authenticated request with the stale key receives HTTP 401.
- There is no server-side expiry yet. For the current dev phase this is acceptable; once the cloud is exposed to remote developers on real networks, add short-lived access tokens with a refresh flow. Tracked under `architecture/cloud-security-hardening-postponed` in engram memory.

## What this document deliberately does NOT cover

- JWT / refresh tokens
- Device binding via `X-Device-ID`
- Audit log of auth events
- OIDC / SSO integration
- mTLS between clients and proxy

These are part of the future `cloud-security-hardening` change and will be documented once implemented. Keeping the deployment doc aligned with what the code actually does is a hard rule.
