# Operation Error Boundary

This document describes how the daemon attaches the external RPC failure
contract (`OperationErrorDetail`) to errors. It covers the boundary introduced
in this change; the per-handler migration described at the end is incremental.

## Problem

`errors.proto` defines a stable `reason` and, for write RPCs, an `effect`. The
wire representation already had one constructor (`errdetail.New`), but the
decision of which reason and effect apply was made by every handler:

```go
if err != nil {
    return nil, ConnectErrorForDomain(err)       // read
}
if err != nil {
    return nil, ConnectErrorForDomainWrite(err, api.ResourceVolume) // write
}
```

That placed the contract in the author's memory. A write mapped through the
read helper reported `OPERATION_EFFECT_UNSPECIFIED`, an unclassified path
reported no detail, and a nested boundary could downgrade an explicit
`NOT_APPLIED` claim. The mapper was also invisible to a newly added RPC.

## Design

The contract travels with the error from the layer that knows why the operation
failed to the transport boundary. Two pieces implement that:

1. `apierror.Failure` is a typed error carrying a `Reason` and an optional
   `Effect`. It supports `Unwrap`, so `fmt.Errorf("...: %w", err)` and
   `errors.As` preserve it across domain and storage layers.
2. `api.ErrorContractHandlerOptions` installs a Connect interceptor on every v2
   service. It is the only place that decides read versus write, derives the
   default effect, and attaches `OperationErrorDetail`.

Handlers return ordinary errors:

```go
volume, err := h.manager.Create(ctx, spec)
if err != nil {
    return nil, err
}
```

## Precedence

The interceptor applies these rules in order:

1. A typed `apierror.Failure` states the reason. The reason selects the
   Connect code (`codeForReason`). The effect comes from `effectFor`:
   - read RPC: always `UNSPECIFIED`;
   - write RPC: `NOT_APPLIED` only when the error carries the marker or an
     explicit typed claim, otherwise `UNKNOWN`.
   The effect is read from the outside in: the first non-unspecified `Failure`
   wins, a marker is a claim, and an unspecified `Failure` defers to its cause.
   An inner NOT_APPLIED is therefore not lost behind an outer unspecified
   wrapper.
2. An error that already carries a detail keeps its reason and code. Only the
   effect is normalized when the RPC direction changed. This is the migration
   path for handlers that still call the old helpers.
3. A raw `*connect.Error` keeps its code and gains a reason derived from that
   code.
4. Any other error is classified by the existing domain sentinel table.

A write never reports `NOT_APPLIED` without a marker or an explicit typed
claim. The fail-closed default is `UNKNOWN`; this is the same rule
`apierror.EffectFor` and the proto comment already state.

### Reason and code tables

`reasonCodes` is the single reason-to-code table. It is a map rather than a
switch so `TestReasonCodesCoverEveryReason` can iterate the wire enum and fail
when a new `OperationErrorReason` has no mapping.

The reverse mapping (`reasonForConnectCode`) is not a true inverse. Two losses
are deliberate and pinned by `TestReasonCodeRoundTrip`:

- `CodeAlreadyExists` and `CodeFailedPrecondition` both collapse into
  `ReasonPreconditionFailed`, so the reason cannot recover `AlreadyExists`;
- a generic `CodeUnavailable` has no dedicated reason and maps to
  `ReasonInternal`.

Existing handlers keep their original code because the interceptor preserves
it; the mapping only applies when a producer returns a typed `Failure`.

## Read/Write Classification

`error_contract_methods.go` contains an exhaustive map from procedure to
`rpcRead` or `rpcWrite`. Two tests guard it:

- `TestErrorContractCoversEveryRPC` walks the proto service descriptor and
  fails in both directions: a procedure without a classification, and a
  classification that no longer matches a procedure.
- `TestRPCKindMatchesVerb` checks the classification against the method's verb
  (`Get`/`List`/`Watch`/... are reads, `Create`/`Update`/`Remove`/... are
  writes). Methods whose verb is ambiguous must appear in `rpcKindOverrides`
  with a written rationale.

The verb test catches copy-paste and typo errors; it cannot prove that a
direction is semantically right. Semantic review still belongs to the service
owner. The durable fix is to move the classification into the proto as a
method option, so it is reviewed with the API definition instead of in a
separate Go table.

An unknown procedure is treated as a write at runtime (the fail-closed choice),
but the coverage test prevents that from reaching production.

## Migration

The interceptor is deliberately backward compatible, so handlers can migrate in
any order:

1. Keep the interceptor as the only place that calls `errdetail.New`.
2. Move reason ownership into the producer: storage returns
   `apierror.NewFailure(ReasonSandboxNotFound, err)` for a real not-found;
   controllers use `NewNotAppliedFailure` only before their first mutation.
3. Delete the per-handler `ConnectErrorForDomain*` calls.
4. Finish with a lint rule that rejects raw `connect.NewError` in
   `pkg/agentcompose/api` outside the boundary.

The old helpers remain during step 2. Their output is already detailed, so the
interceptor only upgrades an UNSPECIFIED effect on a write and otherwise leaves
it untouched.

## Compatibility Notes

- Adding a detail to an error that previously had none is backward compatible:
  clients are required to fail closed when the detail is absent.
- Upgrading a write's `UNSPECIFIED` effect to `UNKNOWN` is the documented
  behavior of `errors.proto`; clients must already treat `UNSPECIFIED` on a
  write as `UNKNOWN`.
- A `NOT_APPLIED` claim is accepted only through the `apierror.NotApplied`
  marker or an explicit `Failure` effect. A legacy detail that says
  `NOT_APPLIED` without either is downgraded to `UNKNOWN`. This matches the
  existing `preserveOrUpgradeDetail` rule: only code that can prove it wrote
  nothing may claim `NOT_APPLIED`.
- Streaming RPCs attach the detail to the trailing error. The HTTP status
  cannot change after the first frame.
