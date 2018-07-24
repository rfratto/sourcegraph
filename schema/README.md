# Sourcegraph JSON Schemas

[JSON Schema](http://json-schema.org/) is a way to define the structure of a JSON document. It enables typechecking and code intelligence on JSON documents.

The following schemas are the sources of truth for Sourcegraph-related configuration:

- [`settings.schema.json`](./settings.schema.json)
- [`site.schema.json`](./site.schema.json)
- [`datacenter.schema.json`](./datacenter.schema.json)

# Modifying a schema

1.  Edit the `*.schema.json` file in this directory.
1.  Run `go generate` to update the `*_stringdata.json` file.
1.  Commit the changes to both files.
1.  When the change is ready for release, [update the documentation](https://github.com/sourcegraph/website/blob/master/README.md#documentation-pages).

## Known issues

- The JSON Schema IDs (URIs) are of the form `https://sourcegraph.com/v1/*.schema.json#`, but these are not actually valid URLs. This means you generally need to supply them to JSON Schema validation libraries manually instead of having the validator fetch the schema from the web.