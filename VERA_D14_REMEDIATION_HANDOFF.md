# VERA_D14_REMEDIATION_HANDOFF

Status: design handoff only; implementation is not authorized.

## Required remediation seam

Restore one authoritative source-license resolution and use-check path:

```text
ResolveSourceLicense
  -> unique matching valid license
  -> RequireSourceUse
  -> bind LicenseID + EvidenceID
  -> archive / PIT snapshot
  -> model pre-send recheck
  -> send-time recheck
```

Missing, expired, revoked, out-of-scope, ambiguous, or unavailable authorization must fail closed. License denial must remain distinguishable from ordinary provider/data-source unavailability.

## Required negative coverage

Missing, expired, revoked, wrong dataset, wrong field, wrong instrument, wrong use, license service unavailable, revoked after snapshot, and replay after revocation. Restore the original revocation test; do not replace it with a 2xx assertion.

## Historical handling

Objects created without a valid source license remain retained for audit only. They must not be displayed, replayed, or sent to a model. No database license row may be added without real vendor authorization evidence.

## Ownership and gate

Implementation owner: Owner-authorized Vera maintainer, after G02 database identity and license records are proven.

Validation owner: independent reviewer after implementation.

Production owner: Owner; no production deployment is authorized by this handoff.

```text
D14_IMPLEMENTATION = NO
PRODUCTION_DEPLOYMENT = NO
NEXT_GATE = OWNER_REVIEW
```

## PRE2 six-document gap

The six original PRE2 filenames are not recoverable from the available handoff material. The current readiness report explicitly records that the six `D14_*.md` deliverables were not found. They are therefore not marked complete, and no filenames are invented here.

```text
PLANNED_PRE2_FILENAMES = NOT_AVAILABLE
PRE2_SIX_DOCUMENTS = NOT_FOUND / NOT_VERIFIED
RESPONSIBLE_PARTY = Owner to provide the original PRE2 task card or filename list
```
