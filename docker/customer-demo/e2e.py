#!/usr/bin/env python3
"""Real isolated customer-stack acceptance. Requires operator-provisioned depth fixtures.
No paid/external LLM. Reads private files; writes only content-free private evidence.
"""
import argparse, base64, datetime as dt, hashlib, http.client, http.cookiejar, json, os, re, ssl, subprocess, time, urllib.error, urllib.request, urllib.parse, uuid, zlib, struct, threading
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
        choices=["all", "stream", "cache", "fallback", "media", "mcp"],
        default="all",
    )
    p.add_argument("--output", type=Path, required=True)
    a = p.parse_args()
    os.umask(0o077)
    a.output.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
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

    def save():
        a.output.write_text(
            json.dumps(
                {
                    "runId": run,
                    "realStack": True,
                    "externalLLM": False,
                    "transport": "two independent OpenAI-compatible synthetic endpoints",
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
    reviewer = session(accounts["primary"]["reviewer"])

    def api(path, value=None, method=None, folder="primary", review=False):
        return rawcall(
            a.controller,
            "/api/v1/workspaces/" + meta[folder]["workspace"] + path,
            value,
            method=method,
            opener=reviewer if review else owners[folder],
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
        if st != 200 and (
            "person@example.com" in out
            or "demo@example.com" in out
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

    def account():
        return dbjson(
            "SELECT json_build_object('spentMicros',spent_micros,'reservedMicros',reserved_micros) FROM plume.ai_gateway_accounts a JOIN plume.workspaces w ON w.id=a.workspace_id WHERE w.slug='customer-depth' AND application_id='depth-app'"
        )

    def timestamp(seconds=0):
        return (
            (dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=seconds))
            .isoformat()
            .replace("+00:00", "Z")
        )

    def newtask(parent=None, tokenmeta=None):
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
            "expiresAt": timestamp(1800),
        }
        if parent:
            v.update(parentTaskId=parent["id"], expiresAt=parent["expiresAt"])
        st, out, _ = api("/ai-gateway/tasks", v)
        assert st == 201, ("new task", st, out[:200])
        return json.loads(out)

    def publish_current():
        st, out, _ = api("/ai-security")
        assert st == 200
        bundle = json.loads(out)
        st, packs, _ = api("/ai-security/regression-packs?limit=100")
        assert st == 200
        packs = json.loads(packs)["items"]
        assert packs
        st, out, _ = api("/ai-security/evaluations", {"packId": packs[0]["id"]})
        assert st == 201
        evaluation = json.loads(out)
        assert evaluation["gateEligible"]
        st, _, _ = api(
            "/ai-security/publish",
            {
                "expectedRevision": bundle["revision"],
                "evaluationId": evaluation["id"],
                "note": "Synthetic independent republication after approval invalidation test",
            },
            review=True,
        )
        assert st == 200
        st, out, _ = api("/ai-security/runtime-config")
        assert st == 200
        dest = a.private_dir / "primary/config/catalog.json"
        tmp = dest.with_suffix(".json.tmp")
        tmp.write_text(out)
        tmp.chmod(0o600)
        tmp.replace(dest)

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
            (422, 502),
        )
        check(
            "provider-timeout-no-replay", text("fixture:timeout", True), (422, 502, 504)
        )
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
        st, out, _ = api("/ai-security")
        assert st == 200
        rev = json.loads(out)["revision"]
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
        st, out, _ = api("/ai-security/runtime-config")
        assert st == 200
        dest = a.private_dir / "primary/config/catalog.json"
        temporary = dest.with_suffix(".json.tmp")
        temporary.write_text(out)
        temporary.chmod(0o600)
        temporary.replace(dest)
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
        st, _, _ = api(
            "/ai-security/approvals/" + staleid + "/review",
            {"decision": "approved", "note": "Synthetic stale approval acceptance"},
            review=True,
        )
        assert st == 200
        st, out, _ = api("/ai-security/runtime-config")
        assert st == 200
        temporary.write_text(out)
        temporary.chmod(0o600)
        temporary.replace(dest)
        stale["approvalId"] = staleid
        st, bundle, _ = api("/ai-security")
        assert st == 200
        bundle = json.loads(bundle)
        before_save = account()
        st, saved, _ = api(
            "/ai-security/configuration",
            {
                "expectedRevision": bundle["revision"],
                "configuration": bundle["configuration"],
            },
            method="PUT",
        )
        assert st == 200
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
        publish_current()
        live = {
            "requestId": "mcp-live-" + run,
            "issuedAt": timestamp(-1),
            "expiresAt": timestamp(240),
        }
        st, out, _ = api("/ai-security")
        assert st == 200
        current = json.loads(out)
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
        st, _, _ = api(
            "/ai-security/approvals/" + liveid + "/review",
            {
                "decision": "approved",
                "note": "Synthetic in-flight revocation acceptance",
            },
            review=True,
        )
        assert st == 200
        st, out, _ = api("/ai-security/runtime-config")
        assert st == 200
        temporary.write_text(out)
        temporary.chmod(0o600)
        temporary.replace(dest)
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
        st, bundle, _ = api("/ai-security")
        assert st == 200
        bundle = json.loads(bundle)
        st, _, _ = api(
            "/ai-security/configuration",
            {
                "expectedRevision": bundle["revision"],
                "configuration": bundle["configuration"],
            },
            method="PUT",
        )
        assert st == 200
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
        publish_current()

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
