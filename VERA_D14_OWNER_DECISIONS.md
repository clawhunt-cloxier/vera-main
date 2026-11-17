# VERA_D14_OWNER_DECISIONS

Status: Owner-reviewed decision record; docs-only handoff.

## Fixed decisions

```text
G07_OWNER_AUTHORIZATION = NOT_PROVEN
G02_DATABASE_IDENTITY = NOT_PROVEN
G02_LICENSE_RECORDS = NOT_QUERIED
LICENSE_SCOPE_VERIFIED = NOT_PROVEN
D14_REPAIR_DESIGN = CONDITIONALLY_ACCEPTED
D14_IMPLEMENTATION = NO
PRODUCTION_DEPLOYMENT = NO
```

## Binding handling rules

1. Do not infer historical authorization or executor intent.
2. Multiple valid licenses matching one request scope are a conflict: reject and alert; never select the latest expiry automatically.
3. Historical records without a license remain retained as raw records but are blocked from display, replay, and model use. No retroactive license is created.
4. Prove Vera production database identity before any authorized read-only license verification.
5. Do not insert license rows to create an authorization path without authentic vendor authorization evidence.

## Design constraints

The repair must preserve arbitrary A-share code input, the existing Go DataService, observation protocol, model pre-send and send-time rechecks, PIT, archive, and license data model. It must introduce or restore `ResolveSourceLicense`, `RequireSourceUse`, LicenseID/EvidenceID binding, fail-closed handling, conflict rejection, historical isolation, error classification, and the ten negative cases plus the revocation test. It must not revert `d521f56` wholesale.

## Scope boundary

Finance Suite MCP is independent. MCP availability or a Finance Suite Trust Gate does not bypass Vera's own License Gate.
