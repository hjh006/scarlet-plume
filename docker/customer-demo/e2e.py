#!/usr/bin/env python3
"""Real isolated customer-stack acceptance. Requires operator-provisioned depth fixtures.
No paid/external LLM. Reads private files; writes only content-free private evidence.
"""
import argparse, base64, datetime as dt, fcntl, hashlib, http.client, http.cookiejar, json, os, re, ssl, subprocess, time, urllib.error, urllib.request, urllib.parse, uuid, zlib, struct, threading
from pathlib import Path


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--private-dir", type=Path, default=Path("var/customer-demo/depth"))
    p.add_argument("--controller", default="http://localhost:18912")
    p.add_argument("--gateway", default="https://127.0.0.1:8907")
    p.add_argument("--other-gateway", default="https://127.0.0.1:8909")
    p.add_argument("--ca-file", type=Path, default=Path("var/customer-demo/tls/ca.crt"))
    p.add_argument("--fixtures", type=Path, default=Path("var/customer-demo/fixtures"))
    p.add_argument(
        "--only",
        choices=["all", "stream", "cache", "fallback", "media", "mcp", "lifecycle"],
        default="all",
    )
    p.add_argument("--output", type=Path, required=True)
    p.add_argument("--preflight-only", action="store_true")
    p.add_argument(
        "--allow-fixture-mutations",
        action="store_true",
        help="acknowledge exclusive use of these synthetic workspaces and approval invalidation",
    )
    a = p.parse_args()
    os.umask(0o077)
    if a.output.exists():
        raise SystemExit(
            "Use a new evidence output path; existing evidence is preserved"
        )
    if (
        not a.preflight_only
        and a.only in ["all", "mcp", "lifecycle"]
        and not a.allow_fixture_mutations
    ):
        raise SystemExit(
            "all/mcp/lifecycle requires --allow-fixture-mutations in an exclusive synthetic fixture window"
        )
    a.output.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    # This prevents two local harnesses sharing files; it cannot lock out operators.
    fixture_lock = (a.private_dir / ".acceptance.lock").open("a")
    try:
        fcntl.flock(fixture_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        raise SystemExit("Another acceptance run owns this private fixture directory")
    for target, scheme in [
        (a.controller, "http"),
        (a.gateway, "https"),
        (a.other_gateway, "https"),
    ]:
        u = urllib.parse.urlsplit(target)
        if (
            u.scheme != scheme
            or u.hostname not in ["localhost", "127.0.0.1", "::1"]
            or u.username
            or u.password
            or u.query
            or u.fragment
            or u.path not in ["", "/"]
            or u.port == 8890
        ):
            raise SystemExit("Only dedicated loopback simulation targets are accepted")
    ctx = ssl.create_default_context(cafile=str(a.ca_file))
    meta = {
        f: json.loads((a.private_dir / f / "metadata.json").read_text())
        for f in ["primary", "other"]
    }
    accounts = json.loads((a.private_dir / "accounts.json").read_text())
    run = uuid.uuid4().hex[:16]
    results = []
    failures = []
    preflight = {}
    mutations = []
    trusted_configs = {}
    current_revision = None
    owned_approvals = set()

    def functional_fingerprint(config):
        # A local comparison fingerprint, not the platform's typed ConfigDigest.
        functional = {k: v for k, v in config.items() if k != "approvals"}
        return hashlib.sha256(
            json.dumps(functional, sort_keys=True, separators=(",", ":")).encode()
        ).hexdigest()

    def save():
        a.output.write_text(
            json.dumps(
                {
                    "runId": run,
                    "realStack": True,
                    "externalLLM": False,
                    "transport": "two independent OpenAI-compatible synthetic endpoints",
                    "preflight": preflight,
                    "configurationMutations": mutations,
                    "results": results,
                    "failures": failures,
                },
                indent=2,
            )
        )
        a.output.chmod(0o600)

    def rawcall(
        base, path, value=None, token=None, method=None, opener=None, headers=None
    ):
        h = {"Content-Type": "application/json", **(headers or {})}
        if token:
            h["Authorization"] = "Bearer " + token
        req = urllib.request.Request(
            base + path,
            None if value is None else json.dumps(value).encode(),
            h,
            method=method,
        )
        try:
            with (
                opener.open(req, timeout=60)
                if opener
                else urllib.request.urlopen(req, context=ctx, timeout=60)
            ) as r:
                return r.status, r.read().decode(), dict(r.headers)
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode(), dict(e.headers)

    def session(account):
        o = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())
        )
        st, _, _ = rawcall(
            a.controller,
            "/api/v1/session",
            account,
            opener=o,
            headers={"Origin": a.controller},
        )
        assert st == 204, ("session", st)
        return o

    assert (
        meta["primary"]["catalogDigest"] == meta["other"]["catalogDigest"]
    ), "cross-tenant case requires identical catalog digests"
    owners = {f: session(accounts[f]["owner"]) for f in meta}
    reviewers = {f: session(accounts[f]["reviewer"]) for f in meta}

    def api(path, value=None, method=None, folder="primary", review=False):
        return rawcall(
            a.controller,
            "/api/v1/workspaces/" + meta[folder]["workspace"] + path,
            value,
            method=method,
            opener=reviewers[folder] if review else owners[folder],
            headers={"Origin": a.controller},
        )

    def credential(folder="primary", app="depth-app"):
        return meta[folder]["credentials"][app]["token"]

    containers = {
        r: "scarlet-customer-gateway-depth-" + r + "-provider-1"
        for r in ["primary", "secondary"]
    }
    containers["mcp"] = "scarlet-customer-gateway-depth-mcp-1"
    offsets_file = a.private_dir / "counter-offsets.json"
    offsets = json.loads(offsets_file.read_text()) if offsets_file.exists() else {}
    assert all(type(v) is int and v >= 0 for v in offsets.values())

    def counters():
        return {
            r: offsets.get(r, 0)
            + subprocess.check_output(
                ["docker", "logs", c], stderr=subprocess.STDOUT
            ).count(
                b"synthetic MCP tool handled"
                if r == "mcp"
                else b"synthetic provider request handled"
            )
            for r, c in containers.items()
        }

    def check(
        name,
        body,
        status=200,
        delta=(1, 0, 0),
        folder="primary",
        token=None,
        path="/v1/chat/completions",
        headers=None,
        cache=None,
    ):
        before = counters()
        st, out, h = rawcall(
            a.gateway if folder == "primary" else a.other_gateway,
            path,
            body,
            token=token or credential(folder),
            headers=headers,
        )
        after = counters()
        got = tuple(after[r] - before[r] for r in ["primary", "secondary", "mcp"])
        item = {
            "case": name,
            "status": st,
            "calls": dict(zip(["primary", "secondary", "mcp"], got)),
            "cache": h.get("X-Scarlet-Cache"),
        }
        errors = []
        if st not in (status if isinstance(status, tuple) else (status,)):
            errors.append("unexpected HTTP status")
        if got != delta:
            errors.append("unexpected actual provider/tool count")
        if cache and h.get("X-Scarlet-Cache") != cache:
            errors.append("unexpected cache result")
        if st == 200 and body.get("stream") is True and (
            not h.get("Content-Type", "").startswith("text/event-stream")
            or not out.rstrip().endswith("data: [DONE]")
        ):
            errors.append("successful stream did not complete")
        if st != 200 and (
            "person@example.com" in out
            or "demo@example.com" in out
            or "Synthetic customer fixture response" in out
            or "chat.completion.chunk" in out
            or "data: [DONE]" in out
        ):
            errors.append("rejected output was released")

        if st != 200:
            try:
                item["error"] = json.loads(out).get("error")
            except (ValueError, AttributeError):
                pass
        item["passed"] = not errors
        item["errors"] = errors
        results.append(item)
        if errors:
            failures.append(name)
        save()
        print(name, st, got, "PASS" if not errors else "FAIL", flush=True)
        return st, out, h

    def text(label, stream=False, model="depth-primary"):
        return {
            "model": model,
            "messages": [{"role": "user", "content": label + " " + run}],
            "stream": stream,
            "max_tokens": 64,
            **({"stream_options": {"include_usage": True}} if stream else {}),
        }

    def dbjson(sql):
        return json.loads(
            subprocess.check_output(
                [
                    "docker",
                    "exec",
                    "scarlet-customer-gateway-db-1",
                    "psql",
                    "-U",
                    "customer_demo",
                    "-d",
                    "customer_demo",
                    "-At",
                    "-c",
                    sql,
                ]
            )
        )

    def reject_unowned_pending():
        owned = ",".join(str(i) for i in sorted(owned_approvals)) or "0"
        pending = dbjson(
            "SELECT json_build_object('approvals',(SELECT count(*) FROM plume.ai_security_approvals a JOIN plume.workspaces w ON w.id=a.workspace_id WHERE w.slug='customer-depth' AND a.status IN ('proposed','approved') AND a.expires_at>now() AND a.id NOT IN ("
            + owned
            + ")), 'leaseProposals',(SELECT count(*) FROM plume.ai_gateway_task_lease_proposals p JOIN plume.workspaces w ON w.id=p.workspace_id WHERE w.slug='customer-depth' AND p.status='proposed' AND p.expires_at>now()))"
        )
        assert pending == {"approvals": 0, "leaseProposals": 0}, (
            "Refusing to invalidate approvals or lease proposals owned by another run",
            pending,
        )

    def checked_bundle(folder="primary", expected=None):
        st, out, _ = api("/ai-security", folder=folder)
        assert st == 200, ("configuration read", st)
        bundle = json.loads(out)
        assert (
            bundle["publishedDigest"] == meta[folder]["catalogDigest"]
        ), "Published catalog drift; stop and review the fixture without overwriting it"
        assert (
            functional_fingerprint(bundle["configuration"]) == trusted_configs[folder]
        ), "Configuration content drift; refusing to save or publish another draft"
        if expected is not None:
            assert bundle["revision"] == expected, (
                "Configuration revision drift; no automatic retry or republication",
                expected,
                bundle["revision"],
            )
        return bundle

    def refresh_catalog(expected_revision):
        st, out, h = api("/ai-security/runtime-config")
        assert st == 200
        assert h.get("X-Configuration-Revision") == str(
            expected_revision
        ), "Published runtime revision changed; local catalog was not replaced"
        assert (
            functional_fingerprint(json.loads(out)) == trusted_configs["primary"]
        ), "Published runtime content changed; local catalog was not replaced"
        dest = a.private_dir / "primary/config/catalog.json"
        temporary = dest.with_suffix(".json.tmp")
        temporary.write_text(out)
        temporary.chmod(0o600)
        temporary.replace(dest)

    def save_same_configuration():
        nonlocal current_revision
        bundle = checked_bundle(expected=current_revision)
        assert (
            bundle["revision"] == bundle["publishedRevision"]
        ), "A draft is already pending; refusing to overwrite it"
        reject_unowned_pending()
        st, out, _ = api(
            "/ai-security/configuration",
            {
                "expectedRevision": current_revision,
                "configuration": bundle["configuration"],
            },
            method="PUT",
        )
        assert st == 200, ("same configuration CAS", st)
        saved = json.loads(out)
        assert saved["revision"] == current_revision + 1
        current_revision = saved["revision"]
        # Persist the owned revision before any subsequent evaluation or assertion.
        mutations.append(
            {
                "savedRevision": current_revision,
                "previousPublishedRevision": bundle["publishedRevision"],
                "catalogDigest": meta["primary"]["catalogDigest"],
                "functionalFingerprint": trusted_configs["primary"],
                "republished": False,
            }
        )
        save()
        return current_revision

    for folder, slug in [
        ("primary", "customer-depth"),
        ("other", "customer-depth-other"),
    ]:
        assert (
            meta[folder]["workspace"] == slug
        ), "Only dedicated depth fixture workspaces are accepted"
        local = json.loads((a.private_dir / folder / "config/catalog.json").read_text())
        assert (
            local["version"] == "customer-depth-v2"
        ), "Unexpected fixture catalog version"
        trusted_configs[folder] = functional_fingerprint(local)
        for role, opener in [
            ("owner", owners[folder]),
            ("reviewer", reviewers[folder]),
        ]:
            st, out, _ = rawcall(a.controller, "/api/v1/session", opener=opener)
            assert st == 200
            identity = json.loads(out)
            assert identity["username"] == accounts[folder][role]["username"]
            assert any(
                w["slug"] == slug
                and w["id"] == meta[folder]["workspaceId"]
                and w["role"] == role
                for w in identity["workspaces"]
            ), ("Fixture role or workspace mismatch", folder, role)
        assert (
            accounts[folder]["owner"]["username"]
            != accounts[folder]["reviewer"]["username"]
        )
        bundle = checked_bundle(folder)
        assert (
            bundle["revision"] == bundle["publishedRevision"] > 0
        ), "Unpublished draft detected; only its operator may review and recover it"
        st, runtime, h = api("/ai-security/runtime-config", folder=folder)
        assert st == 200 and h.get("X-Configuration-Revision") == str(
            bundle["revision"]
        )
        assert functional_fingerprint(json.loads(runtime)) == trusted_configs[folder]
        settings = dbjson(
            "SELECT configuration FROM plume.ai_gateway_settings s JOIN plume.workspaces w ON w.id=s.workspace_id WHERE w.slug='"
            + slug
            + "'"
        )
        assert (
            settings["enabled"]
            and settings["catalogDigest"] == bundle["publishedDigest"]
        )
        for app in ["depth-app", "task-app"]:
            binding = next(v for v in settings["applications"] if v["id"] == app)
            assert binding["enabled"] and binding.get("requireTask", False) == (
                app == "task-app"
            )
            assert set(binding["modelIds"]) == {
                "depth-primary",
                "depth-backup",
                "depth-text",
                "depth-text-backup",
            }
            assert set(binding["toolIds"]) == (
                {"ticket-read", "ticket-close"} if app == "task-app" else set()
            )
            cm = meta[folder]["credentials"][app]
            cid = int(cm["credential"]["id"])
            live = dbjson(
                "SELECT json_build_object('applicationId',application_id,'enabled',enabled AND (expires_at IS NULL OR expires_at>now()),'tokenHash',token_hash) FROM plume.ai_gateway_credentials c JOIN plume.workspaces w ON w.id=c.workspace_id WHERE w.slug='"
                + slug
                + "' AND c.id="
                + str(cid)
            )
            assert live and live["enabled"] and live["applicationId"] == app, (
                "Fixture credential unavailable",
                folder,
                app,
            )
            assert (
                live["tokenHash"] == hashlib.sha256(cm["token"].encode()).hexdigest()
            ), ("Fixture credential does not match its platform record", folder, app)
        preflight[folder] = {
            "workspace": slug,
            "provisionedRevision": meta[folder]["publishedRevision"],
            "liveRevision": bundle["revision"],
            "publishedRevision": bundle["publishedRevision"],
            "catalogDigest": bundle["publishedDigest"],
            "localFunctionalFingerprint": trusted_configs[folder],
            "rolesAndCredentialsVerified": True,
        }
        if folder == "primary":
            current_revision = bundle["revision"]
    assert (
        trusted_configs["primary"] == trusted_configs["other"]
    ), "Cross-tenant case requires identical functional catalogs"
    if a.only in ["all", "mcp"]:
        reject_unowned_pending()
    preflight["passed"] = True
    save()
    if a.preflight_only:
        print(
            json.dumps(
                {
                    "preflightPassed": True,
                    "modelOrToolCalls": 0,
                    "configurationWrites": 0,
                }
            )
        )
        return 0

    def account(task=False):
        application = "task-app" if task else "depth-app"
        return dbjson(
            "SELECT json_build_object('spentMicros',spent_micros,'reservedMicros',reserved_micros) FROM plume.ai_gateway_accounts a JOIN plume.workspaces w ON w.id=a.workspace_id WHERE w.slug='customer-depth' AND application_id='" + application + "'"
        )

    def timestamp(seconds=0):
        return (
            (dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=seconds))
            .isoformat()
            .replace("+00:00", "Z")
        )

    def newtask(parent=None, tokenmeta=None, expires_seconds=1800, expected_status=201):
        cm = tokenmeta or meta["primary"]["credentials"]["task-app"]
        v = {
            "applicationId": "task-app",
            "principalId": "depth-agent",
            "credentialId": cm["credential"]["id"],
            "catalogDigest": meta["primary"]["catalogDigest"],
            "purpose": "support",
            "modelIds": [
                "depth-primary",
                "depth-backup",
                "depth-text",
                "depth-text-backup",
            ],
            "toolIds": ["ticket-read", "ticket-close"],
            "toolScopes": [
                {
                    "toolId": "ticket-read",
                    "actions": ["read"],
                    "targets": ["ticket-17"],
                },
                {
                    "toolId": "ticket-close",
                    "actions": ["close"],
                    "targets": ["ticket-17"],
                },
            ],
            "documentIds": [],
            "allowedCategories": ["image", "audio", "unclassified_media"],
            "maxSteps": 20 if not parent else 5,
            "budgetMicros": 10000000 if not parent else 1000000,
            "maxDelegationDepth": 1 if not parent else 0,
            "expiresAt": timestamp(expires_seconds),
        }
        if parent:
            v.update(parentTaskId=parent["id"], expiresAt=parent["expiresAt"])
        st, out, _ = api("/ai-gateway/tasks", v)
        assert st == expected_status, ("unexpected task creation status", st)
        return json.loads(out) if st == 201 else None

    def publish_owned_revision(expected_revision):
        bundle = checked_bundle(expected=expected_revision)
        assert mutations and mutations[-1]["savedRevision"] == expected_revision
        st, packs, _ = api("/ai-security/regression-packs?limit=100")
        assert st == 200
        packs = json.loads(packs)["items"]
        assert packs
        st, out, _ = api("/ai-security/evaluations", {"packId": packs[0]["id"]})
        assert st == 201
        evaluation = json.loads(out)
        assert (
            evaluation["gateEligible"]
            and evaluation["configurationRevision"] == expected_revision
            and evaluation["catalogDigest"] == meta["primary"]["catalogDigest"]
        )
        checked_bundle(expected=expected_revision)
        st, _, _ = api(
            "/ai-security/publish",
            {
                "expectedRevision": expected_revision,
                "evaluationId": evaluation["id"],
                "note": "Synthetic independent republication after approval invalidation test",
            },
            review=True,
        )
        assert st == 200
        refresh_catalog(expected_revision)
        mutations[-1]["republished"] = True
        save()

    def rpc(
        name,
        task=None,
        action="read-ticket",
        target="ticket-17",
        arguments=None,
        control=None,
        token=None,
        status=200,
        delta=(0, 0, 1),
        rpcid=1,
        extra=None,
    ):
        value = {
            "jsonrpc": "2.0",
            "id": rpcid,
            "method": "tools/call",
            "params": {
                "name": action,
                "arguments": {"ticketId": target, **(arguments or {})},
                "_meta": {
                    "io.modelcontextprotocol/protocolVersion": "2026-07-28",
                    "io.modelcontextprotocol/clientCapabilities": {},
                    "org.scarletplume/control": control
                    or {
                        "requestId": "mcp-" + uuid.uuid4().hex,
                        "issuedAt": timestamp(-1),
                        "expiresAt": timestamp(120),
                    },
                },
            },
        }
        h = {
            "Accept": "application/json, text/event-stream",
            "MCP-Protocol-Version": "2026-07-28",
            "Mcp-Method": "tools/call",
            "Mcp-Name": action,
            **(extra or {}),
        }
        if task:
            h["X-Scarlet-Task-Id"] = task["id"]
        return check(
            name,
            value,
            status,
            delta,
            token=token or credential(app="task-app"),
            path="/mcp",
            headers=h,
        )

    if a.only in ["all", "stream"]:
        check("SSE-normal", text("SSE normal", True))
        check(
            "SSE-cross-chunk-sensitive-suppressed",
            text("fixture:chunk-email", True),
            403,
        )
        check(
            "SSE-incomplete-provider-no-replay",
            text("fixture:partial-stream", True),
            502,
        )
        check("provider-timeout-no-replay", text("fixture:timeout", True), 502)
        before = counters()
        previous = account()
        nonce = "cancel-" + run
        url = urllib.parse.urlsplit(a.gateway)
        conn = http.client.HTTPSConnection(
            url.hostname, url.port, context=ctx, timeout=30
        )
        conn.request(
            "POST",
            "/v1/chat/completions",
            json.dumps(text("fixture:timeout client disconnect", True)),
            {
                "Content-Type": "application/json",
                "Authorization": "Bearer " + credential(),
                "Idempotency-Key": nonce,
            },
        )
        deadline = time.monotonic() + 5
        while (
            time.monotonic() < deadline and counters()["primary"] == before["primary"]
        ):
            time.sleep(0.05)
        conn.close()
        terminal = None
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            rows = [
                json.loads(line)
                for line in (a.private_dir / "primary/state/audit.jsonl")
                .read_text()
                .splitlines()
            ]
            terminal = next(
                (
                    r
                    for r in reversed(rows)
                    if r["requestId"] == nonce and r["stage"] == "completed"
                ),
                None,
            )
            if terminal:
                break
            time.sleep(0.1)
        after = counters()
        current = account()
        item = {
            "case": "client-disconnect-preserves-unknown",
            "calls": {k: after[k] - before[k] for k in before},
            "accountBefore": previous,
            "accountAfter": current,
            "outcome": terminal["execution"]["outcome"] if terminal else None,
        }
        item["passed"] = (
            item["calls"] == {"primary": 1, "secondary": 0, "mcp": 0}
            and item["outcome"] == "unknown"
            and current["reservedMicros"] > previous["reservedMicros"]
            and current["spentMicros"] == previous["spentMicros"]
        )
        results.append(item)
        if not item["passed"]:
            failures.append(item["case"])
        save()
        print(item["case"], "PASS" if item["passed"] else "FAIL", flush=True)
        check(
            "unknown-call-nonce-cannot-replay",
            text("fixture:timeout client disconnect", True),
            409,
            (0, 0, 0),
            headers={"Idempotency-Key": nonce},
        )

    if a.only in ["all", "media"]:

        def chunk(typ, data):
            return (
                struct.pack("!I", len(data))
                + typ
                + data
                + struct.pack("!I", zlib.crc32(typ + data) & 0xFFFFFFFF)
            )

        png = (
            b"\x89PNG\r\n\x1a\n"
            + chunk(b"IHDR", struct.pack("!2I5B", 64, 64, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress((b"\x00" + b"\xff" * (64 * 3)) * 64))
            + chunk(b"IEND", b"")
        )
        v = text("real benign OCR")
        v["messages"][0]["content"] = [
            {
                "type": "image_url",
                "image_url": {
                    "url": "data:image/png;base64," + base64.b64encode(png).decode()
                },
            }
        ]
        check("real-benign-PNG-OCR", v)
        plain = text("media-policy-cache-bypass")
        check("media-policy-cache-bypass-first", plain)
        check("media-policy-cache-bypass-second", plain)
        for mime, file, status, delta in [
            ("image/png", "synthetic-email.png", 403, (0, 0, 0)),
            ("audio/wav", "synthetic-speech.wav", 200, (1, 0, 0)),
        ]:
            b = base64.b64encode((a.fixtures / file).read_bytes()).decode()
            part = (
                {
                    "type": "image_url",
                    "image_url": {"url": "data:" + mime + ";base64," + b},
                }
                if mime.startswith("image/")
                else {
                    "type": "input_audio",
                    "input_audio": {"format": "wav", "data": b},
                }
            )
            v = text("approved synthetic media")
            v["messages"][0]["content"] = [
                {"type": "text", "text": "Approved synthetic media " + run},
                part,
            ]
            check("real-" + mime, v, status, delta)
        check("real-output-image-DLP", text("fixture:media-email-output"), 403)
        v = text("fixture:audio-output")
        v.update(
            modalities=["text", "audio"], audio={"voice": "alloy", "format": "wav"}
        )
        check("real-output-WAV-ASR", v)
    if a.only in ["all", "cache"]:
        v = text("exact cache scope", True, model="depth-text")
        check("cache-warm-real-provider", v)
        check("cache-hit-no-provider", v, delta=(0, 0, 0), cache="hit")
        check("same-digest-cross-tenant-must-miss", v, folder="other")
        check("other-tenant-cache-hit", v, folder="other", delta=(0, 0, 0), cache="hit")
        nonce = {"Idempotency-Key": "cache-" + run}
        check("cache-hit-claims-nonce", v, headers=nonce, delta=(0, 0, 0), cache="hit")
        check("cache-cannot-replay-nonce", v, 409, (0, 0, 0), headers=nonce)
        changed = json.loads(json.dumps(v))
        changed["messages"].insert(
            0, {"role": "system", "content": "Changed approved synthetic context"}
        )
        check("changed-context-must-miss", changed)
        st, out, _ = api(
            "/ai-gateway/credentials",
            {"applicationId": "depth-app", "name": "Revocation acceptance " + run},
        )
        assert st == 201
        new = json.loads(out)
        check(
            "fresh-key-authorized-cache-hit",
            v,
            token=new["token"],
            delta=(0, 0, 0),
            cache="hit",
        )
        st, _, _ = api(
            "/ai-gateway/credentials/" + str(new["credential"]["id"]), method="DELETE"
        )
        assert st == 204
        check("revoked-key-cannot-read-cache", v, 401, (0, 0, 0), token=new["token"])
        parent = newtask()
        child = newtask(parent)
        tk = credential(app="task-app")
        check(
            "task-cache-context-first-provider",
            v,
            headers={"X-Scarlet-Task-Id": parent["id"]},
            token=tk,
        )
        check(
            "task-context-cache-hit",
            v,
            headers={"X-Scarlet-Task-Id": parent["id"]},
            token=tk,
            delta=(0, 0, 0),
            cache="hit",
        )
        check(
            "child-context-cache-isolated",
            v,
            headers={"X-Scarlet-Task-Id": child["id"]},
            token=tk,
        )
        st, out, _ = api("/ai-gateway/tasks/" + parent["id"])
        assert st == 200
        cur = json.loads(out)["task"]
        st, _, _ = api(
            "/ai-gateway/tasks/" + parent["id"] + "/status",
            {
                "expectedRevision": cur["revision"],
                "status": "cancelled",
                "reason": "Synthetic ancestor revocation",
            },
        )
        assert st == 200
        check(
            "cancelled-ancestor-cannot-read-child-cache",
            v,
            403,
            (0, 0, 0),
            headers={"X-Scarlet-Task-Id": child["id"]},
            token=tk,
        )
        encrypted = dbjson(
            "SELECT json_build_object('entries',count(*),'noPlaintext',COALESCE(bool_and(position(convert_to('Synthetic customer fixture response','UTF8') in ciphertext)=0),false)) FROM plume.ai_gateway_cache"
        )
        assert encrypted["entries"] > 0 and encrypted["noPlaintext"]
        results.append(
            {
                "case": "actual-cache-ciphertext-representation",
                "passed": True,
                **encrypted,
            }
        )
        save()
    if a.only in ["all", "lifecycle"]:

        def task_cache_rows():
            return dbjson(
                "SELECT COALESCE(json_object_agg(scope_hash,expires_at), '{}'::json) FROM plume.ai_gateway_cache c JOIN plume.workspaces w ON w.id=c.workspace_id WHERE w.slug='customer-depth' AND application_id='task-app'"
            )

        def warm_task_cache(case, task, body):
            existing = task_cache_rows()
            headers = {"X-Scarlet-Task-Id": task["id"]}
            check(case, body, headers=headers, token=credential(app="task-app"))
            rows = task_cache_rows()
            added = rows.keys() - existing.keys()
            assert len(added) == 1, "Expected exactly one new encrypted cache entry"
            reference = added.pop()
            return headers, reference, dt.datetime.fromisoformat(rows[reference])

        short = newtask(expires_seconds=24)
        child = newtask(short)
        deadline = dt.datetime.fromisoformat(short["expiresAt"])
        value = text("expiring task cached response", True, model="depth-text")
        headers, expiring, stored_expiry = warm_task_cache(
            "task-deadline-warms-cache", child, value
        )
        assert stored_expiry == deadline, "Cache retention should end at the task deadline"
        check(
            "current-task-reads-cached-stream", value, delta=(0, 0, 0),
            headers=headers, token=credential(app="task-app"), cache="hit",
        )
        rpc("current-task-executes-read-only-MCP", child)
        control = newtask(expires_seconds=240)
        control_value = text("independent fresh task cached response", True, model="depth-text")
        control_headers, retained, control_expiry = warm_task_cache(
            "independent-task-warms-cache", control, control_value
        )
        assert control_expiry > deadline + dt.timedelta(seconds=70), "Fixture needs an independent cache entry with a longer TTL"
        assert dt.datetime.fromisoformat(dbjson("SELECT to_json(clock_timestamp())")) < deadline
        wait = time.monotonic() + 30
        while dt.datetime.fromisoformat(dbjson("SELECT to_json(clock_timestamp())")) < deadline:
            assert time.monotonic() < wait, "Database clock did not reach task deadline"
            time.sleep(0.35)
        previous = account(task=True)
        check(
            "expired-task-cannot-read-cached-stream", value, 403, (0, 0, 0),
            headers=headers, token=credential(app="task-app"),
        )
        rpc("expired-ancestor-denies-read-only-MCP", child, status=403, delta=(0, 0, 0))
        assert account(task=True) == previous, "Denied calls changed task application accounting"
        wait = time.monotonic() + 80
        rows = task_cache_rows()
        while expiring in rows and time.monotonic() < wait:
            time.sleep(1)
            rows = task_cache_rows()
        passed = expiring not in rows and retained in rows and account(task=True) == previous
        name = "idle-expired-cache-reclaimed-fresh-cache-retained"
        results.append({
            "case": name,
            "passed": passed,
            "taskDeadline": deadline.isoformat(),
            "storedDeadline": stored_expiry.isoformat(),
            "expiredRowRemoved": expiring not in rows,
            "freshRowPreserved": retained in rows,
        })
        if not passed:
            failures.append(name)
        save()
        print(name, "PASS" if passed else "FAIL", flush=True)
        check(
            "fresh-cache-hit-after-background-reclamation", control_value,
            delta=(0, 0, 0), headers=control_headers,
            token=credential(app="task-app"), cache="hit",
        )
    if a.only in ["all", "fallback"]:
        st, state, _ = api("/ai-gateway")
        assert st == 200
        for route in json.loads(state)["routeHealth"]:
            if route["providerId"] == "primary" and route.get("openUntil"):
                remaining = (
                    dt.datetime.fromisoformat(route["openUntil"].replace("Z", "+00:00"))
                    - dt.datetime.now(dt.timezone.utc)
                ).total_seconds()
                if remaining > 0:
                    time.sleep(min(remaining + 0.2, 32))
        check(
            "healthy-primary-resets-circuit-by-real-success",
            text("healthy before controlled availability failures"),
        )
        for i in range(3):
            check(
                "explicit-503-fallback-" + str(i),
                text("fixture:failure-503 " + str(i)),
                delta=(1, 1, 0),
            )
        check(
            "open-circuit-pre-dispatch-switch",
            text("circuit-open-next-request"),
            delta=(0, 1, 0),
        )
        check(
            "fallback-route-output-DLP-still-enforced",
            text("fixture:chunk-email fallback after circuit", True),
            403,
            (0, 1, 0),
        )
    if a.only in ["all", "mcp"]:
        parent = newtask()
        child = newtask(parent)
        rpc("MCP-missing-task-denied", status=403, delta=(0, 0, 0))
        rpc(
            "MCP-target-scope-denied",
            child,
            target="ticket-99",
            status=403,
            delta=(0, 0, 0),
        )
        rpc("MCP-low-impact-authorized", child)
        rpc(
            "MCP-unapproved-outbound-argument",
            child,
            arguments={"destination": "https://untrusted.invalid"},
            status=(400, 403),
            delta=(0, 0, 0),
        )
        rpc(
            "MCP-model-only-key-has-no-tool-authority",
            token=credential(),
            status=(403, 404),
            delta=(0, 0, 0),
        )
        rpc(
            "MCP-header-protocol-downgrade-refused",
            child,
            status=400,
            delta=(0, 0, 0),
            extra={"MCP-Protocol-Version": "2025-03-26"},
        )
        control = {
            "requestId": "mcp-" + run,
            "issuedAt": timestamp(-1),
            "expiresAt": timestamp(180),
        }
        rpc(
            "MCP-high-impact-no-approval",
            child,
            action="close-ticket",
            arguments={"resolution": "fixed"},
            control=control,
            status=403,
            delta=(0, 0, 0),
        )
        rev = checked_bundle(expected=current_revision)["revision"]
        r = {
            "taskId": child["id"],
            "requestId": control["requestId"],
            "kind": "tool",
            "applicationId": "task-app",
            "principalId": "depth-agent",
            "toolId": "ticket-close",
            "action": "close",
            "target": "ticket-17",
            "arguments": {"resolution": "fixed"},
            "issuedAt": control["issuedAt"],
            "expiresAt": control["expiresAt"],
        }
        st, out, _ = api(
            "/ai-security/approvals", {"expectedRevision": rev, "request": r}
        )
        assert st == 201, ("approval request", st, out[:200])
        approval = json.loads(out)
        aid = str(approval["id"])
        owned_approvals.add(approval["id"])
        st, _, _ = api(
            "/ai-security/approvals/" + aid + "/review",
            {"decision": "approved", "note": "Synthetic independent approval"},
        )
        assert st == 403
        st, out, _ = api(
            "/ai-security/approvals/" + aid + "/review",
            {"decision": "approved", "note": "Synthetic independent approval"},
            review=True,
        )
        assert st == 200, ("review", st, out[:200])
        refresh_catalog(current_revision)
        control["approvalId"] = aid
        rpc(
            "MCP-approval-arguments-exact",
            child,
            action="close-ticket",
            arguments={"resolution": "pending"},
            control=control,
            status=403,
            delta=(0, 0, 0),
        )
        rpc(
            "MCP-independent-approval-executes",
            child,
            action="close-ticket",
            arguments={"resolution": "fixed"},
            control=control,
        )
        rpc(
            "MCP-JSONRPC-ID-cannot-replay-nonce",
            child,
            action="close-ticket",
            arguments={"resolution": "fixed"},
            control=control,
            status=409,
            delta=(0, 0, 0),
            rpcid=999,
        )
        stale = {
            "requestId": "mcp-stale-" + run,
            "issuedAt": timestamp(-1),
            "expiresAt": timestamp(240),
        }
        request = dict(
            r,
            requestId=stale["requestId"],
            issuedAt=stale["issuedAt"],
            expiresAt=stale["expiresAt"],
        )
        st, out, _ = api(
            "/ai-security/approvals", {"expectedRevision": rev, "request": request}
        )
        assert st == 201
        staleid = str(json.loads(out)["id"])
        owned_approvals.add(int(staleid))
        st, _, _ = api(
            "/ai-security/approvals/" + staleid + "/review",
            {"decision": "approved", "note": "Synthetic stale approval acceptance"},
            review=True,
        )
        assert st == 200
        refresh_catalog(current_revision)
        stale["approvalId"] = staleid
        before_save = account()
        saved_revision = save_same_configuration()
        after_save = account()
        results.append(
            {
                "case": "same-config-save-keeps-unknown-accounting",
                "passed": before_save == after_save,
                "before": before_save,
                "after": after_save,
            }
        )
        assert before_save == after_save
        save()
        rpc(
            "MCP-invalidated-approval-old-runtime-snapshot",
            child,
            action="close-ticket",
            arguments={"resolution": "fixed"},
            control=stale,
            status=403,
            delta=(0, 0, 0),
        )
        # Restore a usable published revision through real regression and independent review, without reactivating approvals.
        publish_owned_revision(saved_revision)
        live = {
            "requestId": "mcp-live-" + run,
            "issuedAt": timestamp(-1),
            "expiresAt": timestamp(240),
        }
        current = checked_bundle(expected=current_revision)
        request = dict(
            r,
            requestId=live["requestId"],
            issuedAt=live["issuedAt"],
            expiresAt=live["expiresAt"],
        )
        st, out, _ = api(
            "/ai-security/approvals",
            {"expectedRevision": current["revision"], "request": request},
        )
        assert st == 201
        liveid = str(json.loads(out)["id"])
        owned_approvals.add(int(liveid))
        st, _, _ = api(
            "/ai-security/approvals/" + liveid + "/review",
            {
                "decision": "approved",
                "note": "Synthetic in-flight revocation acceptance",
            },
            review=True,
        )
        assert st == 200
        refresh_catalog(current_revision)
        live["approvalId"] = liveid
        before = counters()
        worker = threading.Thread(
            target=lambda: rpc(
                "MCP-inflight-approval-revocation-suppresses-output",
                child,
                action="close-ticket",
                arguments={"resolution": "fixed"},
                control=live,
                status=502,
                delta=(0, 0, 1),
            )
        )
        worker.start()
        deadline = time.monotonic() + 5
        while counters()["mcp"] == before["mcp"] and time.monotonic() < deadline:
            time.sleep(0.02)
        assert (
            counters()["mcp"] == before["mcp"] + 1
        ), "tool did not enter controlled response delay"
        saved_revision = save_same_configuration()
        worker.join(20)
        assert not worker.is_alive()
        audit = [
            json.loads(line)
            for line in (a.private_dir / "primary/state/audit.jsonl")
            .read_text()
            .splitlines()
        ]
        attempt = [row for row in audit if row["requestId"] == live["requestId"]]
        terminal = [row for row in attempt if row["stage"] == "completed"]
        assert len(terminal) == 1 and len({row["attemptId"] for row in attempt}) == 1
        terminal = terminal[0]
        assert (
            terminal["decision"]["code"] == "upstream_result_unknown"
            and terminal["decision"]["outcome"] == "deny"
            and terminal["execution"]["outcome"] == "unknown"
            and terminal["execution"]["attempted"] is True
            and not terminal["execution"].get("outputReleased", False)
            and not terminal["execution"].get("cacheHit", False)
        )
        step = dbjson(
            "SELECT json_build_object('status',s.status,'completionHash',s.completion_hash,'completedAt',s.completed_at,'reservationState',r.state,'reservedMicros',r.reserved_micros) FROM plume.ai_gateway_task_steps s JOIN plume.workspaces w ON w.id=s.workspace_id LEFT JOIN plume.ai_gateway_reservations r ON r.workspace_id=s.workspace_id AND r.request_id=s.step_id WHERE w.slug='customer-depth' AND s.step_id='"
            + live["requestId"]
            + "'"
        )
        assert (
            step["status"] == "admitted"
            and step["completionHash"] == ""
            and step["completedAt"] is None
            and step["reservationState"] == "unknown"
            and step["reservedMicros"] == 0
        )
        item = next(
            row
            for row in results
            if row["case"] == "MCP-inflight-approval-revocation-suppresses-output"
        )
        item.update(
            terminalCode="upstream_result_unknown",
            outcome="unknown",
            attempted=True,
            outputReleased=False,
            independentAttempts=1,
            step=step,
        )
        save()
        publish_owned_revision(saved_revision)

        keypair = []
        for label in ["parent", "child"]:
            st, out, _ = api(
                "/ai-gateway/credentials",
                {
                    "applicationId": "task-app",
                    "name": "Synthetic ancestor " + label + " " + run,
                },
            )
            assert st == 201
            keypair.append(json.loads(out))
        keyparent = newtask(tokenmeta=keypair[0])
        keychild = newtask(keyparent, tokenmeta=keypair[1])
        rpc(
            "MCP-separate-child-credential-authorized",
            keychild,
            token=keypair[1]["token"],
        )
        st, _, _ = api(
            "/ai-gateway/credentials/" + str(keypair[0]["credential"]["id"]),
            method="DELETE",
        )
        assert st == 204
        rpc(
            "MCP-revoked-ancestor-key-blocks-active-child-key",
            keychild,
            token=keypair[1]["token"],
            status=403,
            delta=(0, 0, 0),
        )
        st, _, _ = api(
            "/ai-gateway/credentials/" + str(keypair[1]["credential"]["id"]),
            method="DELETE",
        )
        assert st == 204
        rpc(
            "MCP-browser-Origin-refused",
            child,
            status=403,
            delta=(0, 0, 0),
            extra={"Origin": "https://untrusted.invalid"},
        )
        st, out, _ = api("/ai-gateway/tasks/" + parent["id"])
        assert st == 200
        cur = json.loads(out)["task"]
        st, _, _ = api(
            "/ai-gateway/tasks/" + parent["id"] + "/status",
            {
                "expectedRevision": cur["revision"],
                "status": "cancelled",
                "reason": "Synthetic MCP ancestor cancellation",
            },
        )
        assert st == 200
        rpc("MCP-ancestor-cancel-stops-tools", child, status=403, delta=(0, 0, 0))
        newtask(parent=parent, expected_status=403)
        results.append({"case": "cancelled-ancestor-rejects-delegation", "status": 403, "passed": True})
        save()
    save()
    print(
        json.dumps(
            {
                "cases": len(results),
                "passed": sum(r["passed"] for r in results),
                "failed": len(failures),
                "realCounters": counters(),
            }
        )
    )
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
