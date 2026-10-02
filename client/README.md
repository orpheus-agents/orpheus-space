# Go client

Import `github.com/orpheus-agents/orpheus-space/client` at a release tag of the root
module. The client and nullable wire types are generated from `api/openapi.yaml`.

```go
api, err := client.NewClientWithResponses(host,
    client.WithRequestEditorFn(func(ctx context.Context, r *http.Request) error {
        r.Header.Set("Authorization", "Bearer " + key)
        return nil
    }))
// Check err, then inspect both the request error and response HTTP status.
page, err := api.ListSchedulesWithResponse(ctx, &client.ListSchedulesParams{})
```

Optional nullable fields distinguish omitted, null and concrete values through
`nullable.Nullable[T]`. To clear an owner/model on PATCH, call `SetNull()` on that
field. To leave it unchanged, leave the field unset. Persist one Idempotency-Key
per create attempt and reuse it on transport retries.

Use `GetProfilesWithResponse` and `GetTemplatesWithResponse` for the available
choices, descriptions and `is_default`. On creation, omitted `Profile` / `Template`
use the configured defaults; on PATCH omission keeps the stored choice. Set a
nonempty name to choose explicitly. Unlike model/owner, these fields cannot be null.
Catalog reads and changed selections require Orpheus availability.

`make generate-client-check`, `make test-client` and `make build-client` validate
the package without starting a database. Generated files must not be edited by hand.
