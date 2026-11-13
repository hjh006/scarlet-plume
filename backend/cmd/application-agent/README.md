# Customer-side application API collector

This command reads one customer-approved, read-only JSON configuration endpoint and submits only allowlisted configuration observations. It does not inspect arbitrary application responses, read prompts or customer records, prove actual provider behavior, or make legal/compliance decisions.

Configure these environment variables in the customer runtime:

- `SCARLET_URL`: the platform HTTPS origin.
- `SCARLET_APPLICATION_TOKEN`: the platform's source-specific observation token.
- `SCARLET_APPLICATION_API_URL`: the approved HTTPS snapshot endpoint, without user info, query or fragment.
- `SCARLET_APPLICATION_API_TOKEN`: optional source-side Bearer token. It is not sent to the platform.

Run `go run ./cmd/application-agent -interval 1m` from `backend`. `-once` performs one collection/delivery. The minimum interval is 30 seconds. A failed or invalid source response sends an empty error observation so prior observations can become unavailable; an unavailable platform is logged as an unconfirmed delivery. Logs contain no endpoint, response body or credential.

The source API must respond with `Content-Type: application/json` and this schema:

```json
{"items":[{"assetId":41,"facts":{"application_name":"Support assistant","application_vendor":"configured provider","application_model":"configured model","application_region":"CN","application_training_use":"disabled"}}]}
```

`assetId` must come from an explicit customer-approved mapping to a platform asset. There is no matching by display name. The platform must independently enforce the token's registered workspace/source/asset scope. There may be zero through fifty unique positive asset IDs. Each item must contain at least one observed fact. Optional facts are string fields from exactly this list: `application_name`, `application_vendor`, `application_model`, `application_region`, `application_retention`, `application_training_use`, `application_service_audience`, `application_knowledge_bases`, `application_tools`, `application_data_categories`. Omit unknown values; blank values are not observations. Values are at most 1,000 Unicode characters and cannot contain control characters or credential-shaped data. Duplicate or unknown JSON fields, structured/raw data fields, oversized responses and suspicious values reject the entire snapshot; no partial fact batch is submitted.

Both source and platform requests verify HTTPS certificates and hostnames, disable redirects and proxies, use a 10-second request timeout and bound the source and observation body to 512 KiB. Source acquisition uses GET only. Delivery retries keep the identical batch identifier and payload; permanent rejections and redirects are not retried. `observedAt` records when the API response was collected, not the provider's configuration-change time. The platform decides freshness and keeps historical evidence separately.

For an isolated local synthetic API, set `SCARLET_SAMPLE_ASSET_ID` to an asset in the isolated fixture workspace and optionally `SCARLET_SAMPLE_API_TOKEN`, then run:

```sh
python3 cmd/application-agent/testdata/application-api.py
```

The fixture prints its loopback-only URL, never its token. Set `SCARLET_APPLICATION_API_URL` to that URL and use `-allow-insecure-loopback -once`. The HTTP exception accepts literal loopback IPs only; it does not enable arbitrary private addresses, DNS aliases or Docker service names. Production remains verified HTTPS. The fixture is not a vendor-specific integration or evidence about a real supplier.
