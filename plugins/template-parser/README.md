# Template Parser Plugin

This is the minimal external parser scaffold for WeKnora.

It demonstrates the parser-side plugin flow:

- `plugin.yaml` discovery and permission declaration
- `pluginapi.ServeParser(...)`
- `Handshake` / `Health` / `Parse`
- external registration through `metadata.engine_name`

Build from the repository root:

```powershell
go build -o template-markdown-parser.exe ./plugins/template-parser
```

The host can load the plugin from an independent repository by pointing a
manifest `entrypoint` to the built binary and setting `metadata.file_types` to
the formats it handles.

For a quick smoke test, the parser should accept a `ParserRequest` with
`FileContent` and return the same content as `MarkdownContent`.
