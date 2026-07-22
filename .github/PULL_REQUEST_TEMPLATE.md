## Summary

-

## Scope

- [ ] Fits the caller, plugin bridge, Runtime Worker, provider adapter, or image boundary.
- [ ] Keeps User, Agent, and provider credentials isolated and does not add delegated child execution outside SDK `RuntimeContext`.
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
