import { fileURLToPath } from "node:url";
import SwaggerParser from "@apidevtools/swagger-parser";

const contractPath = fileURLToPath(
  new URL("../../contracts/openapi.yaml", import.meta.url),
);
const api = await SwaggerParser.validate(contractPath);

function assert(condition, message) {
  if (!condition) throw new Error(`CP12 OpenAPI assertion failed: ${message}`);
}

function resolveSchema(contract, schema) {
  if (!schema?.$ref) return schema;
  const prefix = "#/components/schemas/";
  assert(schema.$ref.startsWith(prefix), `unsupported schema reference ${schema.$ref}`);
  return contract.components.schemas[schema.$ref.slice(prefix.length)];
}

function assertAccepts(contract, schema, value, label) {
  const resolved = resolveSchema(contract, schema);
  if (resolved.type === "string") assert(typeof value === "string", `${label} is not a string`);
  if (resolved.type === "integer") assert(Number.isInteger(value), `${label} is not an integer`);
  if (resolved.minLength !== undefined) assert(value.length >= resolved.minLength, `${label} is too short`);
  if (resolved.minimum !== undefined) assert(value >= resolved.minimum, `${label} is below minimum`);
  if (resolved.enum) assert(resolved.enum.includes(value), `${label} is outside enum`);
  if (resolved.pattern) assert(new RegExp(resolved.pattern).test(value), `${label} violates pattern`);
}

function assertRejects(contract, schema, value, label) {
  let rejected = false;
  try {
    assertAccepts(contract, schema, value, label);
  } catch {
    rejected = true;
  }
  assert(rejected, `${label} unexpectedly satisfies schema`);
}

function assertCP12Matrix(contract) {
  for (const [path, item] of Object.entries(contract.paths)) {
    if (!path.startsWith("/api/v1/") || !path.includes("{id}")) continue;
    for (const method of ["get", "post", "put", "delete", "head"]) {
      if (item[method]) assert(item[method].responses["400"], `${method.toUpperCase()} ${path} omits 400`);
    }
  }

  const media = contract.paths["/api/v1/media/{id}"];
  const requiredStatuses = ["200", "206", "304", "400", "401", "404", "405", "416", "500"];
  const headerMatrix = {
    "200": ["ETag", "Content-Type", "Accept-Ranges", "Content-Length", "X-Accel-Redirect"],
    "206": ["ETag", "Content-Type", "Accept-Ranges", "Content-Range", "Content-Length", "X-Accel-Redirect"],
    "304": ["ETag", "Content-Type", "Accept-Ranges"],
    "416": ["ETag", "Content-Type", "Accept-Ranges", "Content-Range"],
  };
  for (const method of ["get", "head"]) {
    const operation = media[method];
    for (const status of requiredStatuses) {
      assert(operation.responses[status], `${method.toUpperCase()} media omits ${status}`);
    }
    for (const parameterName of ["Range", "If-None-Match"]) {
      const parameter = operation.parameters.find(({ name }) => name === parameterName);
      assert(parameter, `${method.toUpperCase()} media omits ${parameterName}`);
      assert(parameter.schema.$ref !== "#/components/schemas/ULID", `${parameterName} references ULID`);
    }
    for (const [status, names] of Object.entries(headerMatrix)) {
      for (const name of names) {
        const header = operation.responses[status].headers?.[name];
        assert(header, `${method.toUpperCase()} media ${status} omits ${name}`);
        assert(header.schema.$ref !== "#/components/schemas/ULID", `${name} references ULID`);
      }
    }
  }

  const schemas = contract.components.schemas;
  for (const name of [
    "LibraryCreate", "LibraryUpdate", "WorkCreate", "WorkUpdate", "EditionCreate",
    "EditionUpdate", "ChapterCreate", "ChapterUpdate", "SourceAssetCreate", "SourceAssetUpdate",
  ]) {
    assert(schemas[name].additionalProperties === false, `${name} is not closed`);
  }
  assertAccepts(contract, schemas.SingleByteRange, "bytes=0-1023", "valid Range");
  assertAccepts(contract, schemas.ConditionalETag, `W/"${"a".repeat(64)}"`, "valid If-None-Match");
  assertAccepts(contract, schemas.StrongSHA256ETag, `"${"a".repeat(64)}"`, "valid ETag");
  assertAccepts(contract, schemas.MediaType, "audio/mpeg", "valid Content-Type");
  assertAccepts(contract, schemas.MediaType, "opaque-media", "opaque Content-Type");
  assertAccepts(contract, schemas.MediaType, "application/octet-stream+x-custom", "non-standard Content-Type");
  assertAccepts(contract, schemas.ByteRangeUnit, "bytes", "valid Accept-Ranges");
  assertAccepts(contract, schemas.ByteLength, 0, "valid Content-Length");
  assertAccepts(contract, schemas.ContentByteRange, "bytes 0-1023/2048", "valid Content-Range");
  assertAccepts(contract, schemas.ContentByteRange, "bytes */2048", "valid unsatisfied Content-Range");
  assertAccepts(contract, schemas.InternalRedirectURI, "/_protected_media/abc", "valid redirect");
  assertRejects(contract, schemas.ULID, "not-a-ulid", "malformed ULID");
  assertRejects(contract, schemas.SHA256, "A".repeat(64), "uppercase digest");
  assertRejects(contract, schemas.SHA256, "abc", "short digest");
  assert(schemas.Milliseconds.minimum === 0 && schemas.Milliseconds.description.includes("end_ms"), "timing boundary is incomplete");
  assert(schemas.SourceAssetCreate.properties.size_bytes.minimum === 0, "create size_bytes can be negative");
  assert(schemas.SourceAssetUpdate.properties.size_bytes.minimum === 0, "update size_bytes can be negative");
  assert(schemas.LanguageTag.description.includes("production BCP-47 parser"), "language parser boundary is missing");
}

assertCP12Matrix(api);
const malformed = structuredClone(api);
delete malformed.paths["/api/v1/libraries/{id}"].get.responses["400"];
let negativeControlRejected = false;
try {
  assertCP12Matrix(malformed);
} catch {
  negativeControlRejected = true;
}
assert(negativeControlRejected, "malformed in-memory contract passed the semantic matrix");

// Sensitivity control: an overrestrictive MediaType clone that requires a MIME
// type/subtype slash must be rejected by the same production-pattern check.
const overrestrictive = structuredClone(api);
overrestrictive.components.schemas = {
  ...overrestrictive.components.schemas,
  MediaType: {
    type: "string",
    pattern: "^[^\\s/]+/[^\\s/]+$",
    description: "overrestrictive clone that demands a slash",
  },
};
let sensitivityRejected = false;
try {
  assertAccepts(overrestrictive, overrestrictive.components.schemas.MediaType, "opaque-media", "opaque vs overrestrictive");
} catch {
  sensitivityRejected = true;
}
assert(sensitivityRejected, "overrestrictive MediaType clone accepted opaque-media — sensitivity control is broken");

console.log("OpenAPI contract and CP12 semantic matrix validated.");
