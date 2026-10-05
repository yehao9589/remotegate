"""Integration checks for a fresh, disposable CI deployment only.

Requires SMOKE_BASE_URL. Never run against an existing deployment.
No user data or production credentials are used.
"""
import hashlib
import http.client
import http.cookiejar
import json
import os
from pathlib import Path
import socket
import ssl
import subprocess
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request


BASE = os.environ["SMOKE_BASE_URL"].rstrip("/")
ORIGIN = os.environ.get("SMOKE_PUBLIC_URL", BASE)
PASSWORD = "updated-ci-password-456" if sys.argv[1] == "restart" else "disposable-ci-password-123"
USER = "smoke-admin"
ENTRY = "/smoke-final-entry" if sys.argv[1] == "restart" else "/smoke-admin-entry"
cookies = http.cookiejar.CookieJar()
client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cookies))


def call(path, body=None, expected=200, origin=ORIGIN):
    data = None if body is None else json.dumps(body).encode()
    request = urllib.request.Request(BASE + path, data=data,
                                     headers={"Content-Type": "application/json", "X-Admin-Entry": ENTRY})
    if os.environ.get("SMOKE_PUBLIC_URL"):
        # Match Baota's default upstream Host without forwarding origin/protocol headers.
        request.add_header("Host", "127.0.0.1")
    if body is not None:
        request.add_header("Origin", origin)
    try:
        response = client.open(request, timeout=15)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        raw = response.read()
        assert response.status == expected, (path, response.status, raw[:300])
        return json.loads(raw) if raw and response.headers.get_content_type() == "application/json" else raw


def check_package():
    package = call("/api/admin/package")
    release = call("/api/admin/version")
    assert package["available"] and release["packageVersion"] in package["name"]
    assert package["server"] == release
    assert package["manifest"]["agentVersion"] == release["version"]
    assert package["manifest"]["packageVersion"] == release["packageVersion"]
    assert package["manifest"]["release"] == release
    if os.environ.get("GITHUB_SHA"):
        assert release["commit"] == os.environ["GITHUB_SHA"]
        assert release["builtAt"] != "unknown"
    assert package["manifest"]["architectures"] == ["x86_64", "ARM64", "ARMv7"]
    download = call(package["download"])
    assert hashlib.sha256(download).hexdigest() == package["sha256"]
    assert download.startswith(b"#!/bin/sh\n") and b"__PAYLOAD__\n" in download


def verify_tls(certificate):
    context = ssl.create_default_context(cadata=certificate)
    address = urllib.parse.urlparse(BASE).hostname
    port = int(os.environ["SMOKE_TLS_PORT"])
    with socket.create_connection((address, port), timeout=10) as raw:
        with context.wrap_socket(raw, server_hostname="example.test") as connection:
            connection.sendall(b"GET /healthz HTTP/1.1\r\nHost: example.test\r\nConnection: close\r\n\r\n")
            response = http.client.HTTPResponse(connection)
            response.begin()
            assert response.status == 204
            response.read()


phase = sys.argv[1]
assert phase in ("setup", "restart")
call("/healthz", expected=204)
status = call("/api/auth/status")
if phase == "setup":
    assert not status["initialized"], "This check requires an empty CI data directory"
    call("/api/admin/state", expected=409)
    assert b'id="setupForm"' in call("/install")
    call("/api/auth/setup", {}, expected=403, origin="https://evil.example")
    call("/api/auth/setup", {"username": USER, "password": PASSWORD, "confirmPassword": PASSWORD, "entryPath": ENTRY})
    assert b'id="loginform"' in call(ENTRY)
    call("/install", expected=404)
    call("/", expected=404)
    assert call("/api/auth/status")["authenticated"]
    call("/api/auth/setup", {"username": "different", "password": PASSWORD, "confirmPassword": PASSWORD}, expected=409)
    domain = call("/api/admin/domains", {"baseDomain": "example.test", "dnsProvider": "manual"})
    with tempfile.TemporaryDirectory() as directory:
        key, cert = Path(directory) / "key.pem", Path(directory) / "cert.pem"
        subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                        "-keyout", str(key), "-out", str(cert), "-days", "2",
                        "-subj", "/CN=example.test",
                        "-addext", "subjectAltName=DNS:example.test,DNS:*.example.test",
                        "-addext", "basicConstraints=critical,CA:FALSE",
                        "-addext", "extendedKeyUsage=serverAuth"], check=True, capture_output=True)
        result = call("/api/admin/certificates?domainId=" + domain["id"],
                      {"action": "upload", "certificate": cert.read_text(), "privateKey": key.read_text()})
        assert result["installed"] and result["certificate"]["valid"]
    # Pending devices stay out of the installed list. Generate, but don't execute, an installer.
    device = call("/api/admin/devices", {"name": "CI pending device"})
    ticket = call("/api/admin/install", {"deviceId": device["device"]["id"],
                                        "token": device["token"], "server": BASE})
    link = ticket["command"].split("'")[1]
    path = urllib.parse.urlparse(link).path
    script = call(path)
    assert b"sha256sum -c" in script and b"uci set remotegate.main.server=" in script
    call(path, expected=410)
    assert not call("/api/admin/state")["devices"]
else:
    assert status["initialized"] and not status["authenticated"]
    call("/api/admin/state", expected=401)
    call("/api/auth/login", {"username": USER, "password": PASSWORD})
    assert call("/api/admin/security")["entryPath"] == ENTRY
    domains = call("/api/admin/domains")
    assert len(domains) == 1 and domains[0]["baseDomain"] == "example.test"
    domain = domains[0]

check_package()
path = "/api/admin/certificates?domainId=" + domain["id"]
assert call(path)["installed"]
certificate = call(path, {"action": "export"})["certificate"]
verify_tls(certificate)
if phase == "setup":
    call("/api/admin/security", {"action": "password", "currentPassword": PASSWORD,
                               "newPassword": "updated-ci-password-456", "confirmPassword": "updated-ci-password-456"})
    call("/api/admin/state", expected=401)
    call("/api/auth/login", {"username": USER, "password": PASSWORD}, expected=401)
    PASSWORD = "updated-ci-password-456"
    call("/api/auth/login", {"username": USER, "password": PASSWORD})
    call("/api/admin/security", {"action": "entry", "entryPath": "/smoke-final-entry", "currentPassword": PASSWORD})
    call("/api/admin/state", expected=401)
    call(ENTRY, expected=404)
    ENTRY = "/smoke-final-entry"
    call("/api/auth/login", {"username": USER, "password": PASSWORD})
    assert b'id="loginform"' in call(ENTRY)
call("/api/auth/logout", {}, expected=204)
call("/api/admin/state", expected=401)
print("PASS:", phase, "administrator, persistence, package download, install ticket and native HTTPS")
