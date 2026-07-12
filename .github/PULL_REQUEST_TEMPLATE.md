## Summary

-

## Scope

- [ ] Fits the CLI's user/API caller boundary.
- [ ] Does not add Agent Token handling, Runtime v2 WebSocket/Pull sessions, mTLS, or delegated child execution.
- [ ] Does not add Hosted billing, wallet, marketplace, or dashboard behavior.
- [ ] Does not include secrets, private URLs, customer data, or local `.env` values.

## Validation

- [ ] Root-module tests, formatting, vet, and build checks pass.
- [ ] Nested `example/agent-skill` tests and vet pass, or the example is unaffected.
- [ ] English and Chinese documentation were updated together, or no documentation change is needed.
- [ ] Security-sensitive behavior was reviewed, or the change is not security-sensitive.

## Notes

Link related issues. Call out breaking behavior and any Core API or
`openlinker-go` compatibility impact.
