# Template Web Search Plugin

This is the minimal external Web Search provider scaffold.

It demonstrates:

- `extension_type: search` in the manifest;
- the `Handshake` / `Health` / `Search` gRPC contract;
- tenant-scoped parameters forwarded on each search request;
- registration through the existing `ProviderFactory` registry.

Build it from the repository root:

```powershell
Push-Location plugins/template-web-search
go build -buildvcs=false -mod=mod -o template-web-search.exe .
Pop-Location
```

The example deliberately returns a deterministic result and declares
`network: none`. Replace the `OnSearch` body with the provider's API call only
after choosing the correct manifest network permission and runtime sandbox.
